// Command continuum-plugin-local-ebooks is the plugin entrypoint.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	publicmanifest "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/manifest"
	sdkruntime "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/grpc/ebookbackend"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/grpc/metadataprovider"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/httproutes"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata/sources"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/migrate"
	pluginrt "github.com/RXWatcher/continuum-plugin-local-ebooks/internal/runtime"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/scanner"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/scheduler"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/server"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

//go:embed manifest.json
var manifestRaw []byte

func main() {
	logger := hclog.New(&hclog.LoggerOptions{Name: "continuum-plugin-local-ebooks"})
	slogger := slog.Default()

	manifest, err := loadManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load manifest: %v\n", err)
		os.Exit(1)
	}

	httpSrv := httproutes.NewServer()

	var (
		poolPtr   atomic.Pointer[pgxpool.Pool]
		storePtr  atomic.Pointer[store.Store]
		cfgPtr    atomic.Pointer[pluginrt.Config]
		appCfgPtr atomic.Pointer[store.AppConfig]
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
		eventID, err := st.InsertScanEvent(ctx, nil)
		if err != nil {
			// Without an audit row a scan that runs (and may partially fail)
			// would report HTTP 200 {"scan_event_id":0} — abort instead.
			return 0, fmt.Errorf("insert scan_event: %w", err)
		}
		var totalAdded, totalChanged, totalDeleted, totalFailed int
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
				if ferr := st.FinishScanEvent(ctx, eventID, totalAdded, totalChanged, totalDeleted, walkErr.Error()); ferr != nil {
					logger.Warn("finish scan_event", "err", ferr)
				}
				return eventID, walkErr
			}
			totalAdded += res.Added
			totalChanged += res.Changed
			totalDeleted += res.Deleted
			totalFailed += res.Failed
			_ = st.MarkLibraryScanned(ctx, lp.ID)
		}
		// Record per-file degradation in the audit row instead of reporting a
		// clean success when files actually failed to ingest.
		scanErrText := ""
		if totalFailed > 0 {
			scanErrText = fmt.Sprintf("%d file(s) failed to ingest", totalFailed)
		}
		if ferr := st.FinishScanEvent(ctx, eventID, totalAdded, totalChanged, totalDeleted, scanErrText); ferr != nil {
			logger.Warn("finish scan_event", "err", ferr)
		}

		if c := appCfgPtr.Load(); c != nil && c.ScanInlineEnrich {
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

	runScanOne := func(ctx context.Context, lpID int64) (int64, error) {
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
		var target *store.LibraryPath
		for i := range paths {
			if paths[i].ID == lpID {
				target = &paths[i]
				break
			}
		}
		if target == nil {
			return 0, fmt.Errorf("library %d not found", lpID)
		}
		// Intentional: a per-library admin scan is an explicit operator
		// override and runs even if the library is disabled (disabled only
		// means "not exposed to the portal"). Bulk runScan skips disabled.
		eventID, err := st.InsertScanEvent(ctx, &lpID)
		if err != nil {
			return 0, fmt.Errorf("insert scan_event: %w", err)
		}
		res, walkErr := scanner.Walk(ctx, target.Path, target.ID, scanner.Deps{
			Store:           st,
			EnrichmentQueue: queuePtr.Load(),
			Logger:          slogger,
		})
		errText := ""
		if walkErr != nil {
			errText = walkErr.Error()
		} else if res.Failed > 0 {
			errText = fmt.Sprintf("%d file(s) failed to ingest", res.Failed)
		}
		if ferr := st.FinishScanEvent(ctx, eventID, res.Added, res.Changed, res.Deleted, errText); ferr != nil {
			logger.Warn("finish scan_event", "err", ferr)
		}
		if walkErr == nil {
			_ = st.MarkLibraryScanned(ctx, target.ID)
		}
		return eventID, walkErr
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
	configureMetadata := func(p *pgxpool.Pool, st *store.Store, appCfg store.AppConfig) {
		ua := "continuum-local-ebooks/" + manifest.GetVersion()
		reg := sources.NewRegistry()
		reg.Register(sources.NewOpenLibrary(ua))
		reg.Register(sources.NewGoogleBooks(appCfg.GoogleBooksAPIKey, ua))
		reg.Register(sources.NewISBNdb(appCfg.ISBNdbAPIKey, ua))
		reg.Register(sources.NewHardcover(appCfg.HardcoverAPIKey, ua))
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

		ttl := time.Duration(appCfg.MetadataCacheTTLDays) * 24 * time.Hour
		cache := metadata.NewCache(p, ttl)
		cachePtr.Store(cache)
		aggRegAdapter := newAggregatorRegistryAdapter(reg)
		agg := metadata.NewAggregator(aggRegAdapter, cache, appCfg.MetadataRateLimitRPS)

		q := metadata.NewQueue(p)
		workerRegAdapter := newWorkerRegistryAdapter(reg)
		worker := metadata.NewEnrichmentWorker(q, st, workerRegAdapter,
			appCfg.MetadataScanSource, appCfg.MetadataDefaultRegion, logger)

		queuePtr.Store(q)
		workerPtr.Store(worker)

		enabledFn := func() map[string]bool {
			m := map[string]bool{}
			if c := appCfgPtr.Load(); c != nil {
				for _, id := range c.MetadataSourcesEnabled {
					m[id] = true
				}
			}
			return m
		}
		regionFn := func() string {
			if c := appCfgPtr.Load(); c != nil {
				return c.MetadataDefaultRegion
			}
			return "us"
		}

		metaSrv.SetAggregator(agg)
		metaSrv.SetRegistry(reg)
		metaSrv.SetEnabled(enabledFn)
		metaSrv.SetRegion(regionFn)
	}

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
		if _, err := st.ImportLegacyAppConfig(ctx, appConfigFromRuntimeConfig(cfg)); err != nil {
			p.Close()
			return fmt.Errorf("import legacy app config: %w", err)
		}
		appCfg, err := st.GetAppConfig(ctx)
		if err != nil {
			p.Close()
			return fmt.Errorf("load app config: %w", err)
		}
		appCfgPtr.Store(&appCfg)

		for _, lib := range cfg.Libraries {
			if err := st.SeedLibraryPath(ctx, store.LibraryPathConfig{
				Path:      lib.Path,
				Name:      lib.Name,
				MediaType: lib.MediaType,
			}); err != nil {
				logger.Warn("seed library_path", "path", lib.Path, "err", err)
			}
		}
		configureMetadata(p, st, appCfg)

		mux := http.NewServeMux()
		catalogSrv := ebookbackend.NewServer(st, slogger, cfg.StreamSigningSecret)
		server.MountCatalog(mux, catalogSrv)
		server.MountAdminWithDeps(mux, server.AdminDeps{
			Store:       st,
			ConfigStore: st,
			ScanFn:      runScan,
			ConfigSnapshot: func() pluginrt.Config {
				if c := cfgPtr.Load(); c != nil {
					return *c
				}
				return pluginrt.Config{}
			},
			OnConfigUpdate: func(updated store.AppConfig) {
				appCfgPtr.Store(&updated)
				configureMetadata(p, st, updated)
			},
		})
		server.MountLibraryRoutes(mux, server.LibraryDeps{
			Store: st,
			DirExists: func(p string) bool {
				fi, err := os.Stat(p)
				return err == nil && fi.IsDir()
			},
			ScanOne: runScanOne,
		})

		server.MountAdminHome(mux)

		httpSrv.SetHandler(mux)

		storePtr.Store(st)
		if old := poolPtr.Swap(p); old != nil {
			old.Close()
		}

		cfgCopy := cfg
		cfgPtr.Store(&cfgCopy)

		logger.Info("configured",
			"library_paths", cfg.LibraryPaths,
			"sources_enabled", len(appCfg.MetadataSourcesEnabled))
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

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestRaw)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	executablePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	binaryData, err := os.ReadFile(executablePath)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", executablePath, err)
	}
	checksum := sha256.Sum256(binaryData)
	manifest.Checksum = hex.EncodeToString(checksum[:])
	if len(manifest.GetSupportedPlatforms()) == 0 {
		manifest.SupportedPlatforms = []*pluginv1.SupportedPlatform{
			{Os: goruntime.GOOS, Arch: goruntime.GOARCH},
		}
	}
	return manifest, nil
}

func appConfigFromRuntimeConfig(cfg pluginrt.Config) store.AppConfig {
	return store.AppConfig{
		MetadataSourcesEnabled: append([]string(nil), cfg.MetadataSourcesEnabled...),
		MetadataDefaultRegion:  cfg.MetadataDefaultRegion,
		MetadataCacheTTLDays:   cfg.MetadataCacheTTLDays,
		MetadataRateLimitRPS:   cfg.MetadataRateLimitRPS,
		ScanInlineEnrich:       cfg.ScanInlineEnrich,
		MetadataScanSource:     cfg.MetadataScanSource,
		GoogleBooksAPIKey:      cfg.GoogleBooksAPIKey,
		ISBNdbAPIKey:           cfg.ISBNdbAPIKey,
		HardcoverAPIKey:        cfg.HardcoverAPIKey,
	}.WithDefaults()
}

