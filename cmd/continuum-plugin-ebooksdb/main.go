// Command continuum-plugin-ebooksdb is the plugin entrypoint.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/grpc/ebookbackend"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/grpc/metadataprovider"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/httproutes"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/metadata"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/metadata/sources"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/migrate"
	pluginrt "github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/runtime"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/scanner"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/scheduler"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/server"
	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/store"
)

//go:embed manifest.json
var manifestRaw []byte

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "continuum-plugin-ebooksdb"})
	slogger := slog.Default()

	manifest, err := publicmanifest.Load(manifestRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load manifest: %v\n", err)
		os.Exit(1)
	}

	httpSrv := httproutes.NewServer()

	var (
		poolPtr   atomic.Pointer[pgxpool.Pool]
		storePtr  atomic.Pointer[store.Store]
		cfgPtr    atomic.Pointer[pluginrt.Config]
		workerPtr atomic.Pointer[metadata.EnrichmentWorker]
		queuePtr  atomic.Pointer[metadata.Queue]
		cachePtr  atomic.Pointer[metadata.Cache]
	)

	scanMu := sync.Mutex{}

	runScan := func(ctx context.Context) (int64, error) {
		scanMu.Lock()
		defer scanMu.Unlock()
		st := storePtr.Load()
		if st == nil {
			return 0, fmt.Errorf("store not configured")
		}
		paths, err := st.ListLibraryPaths(ctx)
		if err != nil {
			return 0, err
		}
		eventID, _ := st.InsertScanEvent(ctx, nil)
		var totalAdded, totalChanged, totalDeleted int
		for _, lp := range paths {
			if !lp.Enabled {
				continue
			}
			res, walkErr := scanner.Walk(ctx, lp.Path, lp.ID, scanner.Deps{
				Store:           st,
				EnrichmentQueue: queuePtr.Load(),
				Logger:          slogger,
			})
			if walkErr != nil {
				_ = st.FinishScanEvent(ctx, eventID, totalAdded, totalChanged, totalDeleted, walkErr.Error())
				return eventID, walkErr
			}
			totalAdded += res.Added
			totalChanged += res.Changed
			totalDeleted += res.Deleted
			_ = st.MarkLibraryScanned(ctx, lp.ID)
		}
		_ = st.FinishScanEvent(ctx, eventID, totalAdded, totalChanged, totalDeleted, "")

		if c := cfgPtr.Load(); c != nil && c.ScanInlineEnrich {
			if w := workerPtr.Load(); w != nil {
				if drainErr := w.Drain(ctx); drainErr != nil {
					logger.Warn("inline enrichment drain", "err", drainErr)
				}
			}
		}
		if cache := cachePtr.Load(); cache != nil {
			if _, evictErr := cache.EvictExpired(ctx); evictErr != nil {
				logger.Warn("metadata cache eviction", "err", evictErr)
			}
		}
		return eventID, nil
	}

	drainWorker := func(ctx context.Context) error {
		if w := workerPtr.Load(); w != nil {
			return w.Drain(ctx)
		}
		return nil
	}

	tasks := &scheduler.Tasks{ScanFn: runScan, DrainFn: drainWorker}
	schedSrv := scheduler.New(tasks)

	metaSrv := &metadataprovider.Server{}

	rt := pluginrt.New(manifest, func(cfg pluginrt.Config) error {
		ctx := context.Background()

		pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
		if err != nil {
			return fmt.Errorf("parse db: %w", err)
		}
		if pcfg.MaxConns < 16 {
			pcfg.MaxConns = 16
		}
		p, err := pgxpool.NewWithConfig(ctx, pcfg)
		if err != nil {
			return fmt.Errorf("pgxpool: %w", err)
		}
		if err := migrate.Run(ctx, cfg.DatabaseURL); err != nil {
			p.Close()
			return fmt.Errorf("migrate: %w", err)
		}
		st := store.New(p)

		for _, path := range cfg.LibraryPaths {
			if _, err := st.UpsertLibraryPath(ctx, path); err != nil {
				logger.Warn("upsert library_path", "path", path, "err", err)
			}
		}

		ua := "continuum-ebooksdb/" + manifest.GetVersion()
		reg := sources.NewRegistry()
		reg.Register(sources.NewOpenLibrary(ua))
		reg.Register(sources.NewGoogleBooks(cfg.GoogleBooksAPIKey, ua))
		reg.Register(sources.NewISBNdb(cfg.ISBNdbAPIKey, ua))
		reg.Register(sources.NewHardcover(cfg.HardcoverAPIKey, ua))
		reg.Register(sources.NewGoodreads(ua))
		reg.Register(sources.NewAmazon(ua))
		reg.Register(sources.NewAnnasArchive(ua))
		reg.Register(sources.NewGutenberg(ua))
		reg.Register(sources.NewBookBrainz(ua))
		reg.Register(sources.NewFantasticFiction(ua))
		reg.Register(sources.NewISFDB(ua))
		reg.Register(sources.NewLibraryThing(ua))
		reg.Register(sources.NewInternetArchive(ua))
		reg.Register(sources.NewWorldCat(ua))
		reg.Register(sources.NewDouban(ua))

		ttl := time.Duration(cfg.MetadataCacheTTLDays) * 24 * time.Hour
		cache := metadata.NewCache(p, ttl)
		cachePtr.Store(cache)
		aggRegAdapter := newAggregatorRegistryAdapter(reg)
		agg := metadata.NewAggregator(aggRegAdapter, cache, cfg.MetadataRateLimitRPS)

		q := metadata.NewQueue(p)
		workerRegAdapter := newWorkerRegistryAdapter(reg)
		worker := metadata.NewEnrichmentWorker(q, st, workerRegAdapter,
			cfg.MetadataScanSource, cfg.MetadataDefaultRegion, logger)

		queuePtr.Store(q)
		workerPtr.Store(worker)

		mux := http.NewServeMux()
		catalogSrv := ebookbackend.NewServer(st, slogger)
		server.MountCatalog(mux, catalogSrv)
		server.MountAdmin(mux, st)
		httpSrv.SetHandler(mux)

		storePtr.Store(st)
		if old := poolPtr.Swap(p); old != nil {
			old.Close()
		}

		cfgCopy := cfg
		cfgPtr.Store(&cfgCopy)

		enabledFn := func() map[string]bool {
			m := map[string]bool{}
			if c := cfgPtr.Load(); c != nil {
				for _, id := range c.MetadataSourcesEnabled {
					m[id] = true
				}
			}
			return m
		}
		regionFn := func() string {
			if c := cfgPtr.Load(); c != nil {
				return c.MetadataDefaultRegion
			}
			return "us"
		}

		metaSrv.SetAggregator(agg)
		metaSrv.SetRegistry(reg)
		metaSrv.SetEnabled(enabledFn)
		metaSrv.SetRegion(regionFn)

		logger.Info("configured",
			"library_paths", cfg.LibraryPaths,
			"sources_enabled", len(cfg.MetadataSourcesEnabled))
		return nil
	})

	sdkruntime.Serve(sdkruntime.ServeConfig{
		Logger: logger,
		Servers: sdkruntime.CapabilityServers{
			Runtime:          rt,
			HttpRoutes:       httpSrv,
			ScheduledTask:    schedSrv,
			MetadataProvider: metaSrv,
		},
	})
}
