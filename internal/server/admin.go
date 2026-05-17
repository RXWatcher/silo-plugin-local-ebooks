package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	pluginrt "github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/runtime"
	"github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/store"
)

// BackfillStore is the surface admin.go needs from *store.Store.
type BackfillStore interface {
	BulkEnqueueBackfill(ctx context.Context) (int64, error)
}

// AdminStore is the store surface used by admin status endpoints.
type AdminStore interface {
	BackfillStore
	Ping(ctx context.Context) error
	ListLibraryPaths(ctx context.Context) ([]store.LibraryPath, error)
	RecentScanEvents(ctx context.Context, limit int) ([]store.ScanEvent, error)
	CatalogStats(ctx context.Context) (store.CatalogStats, error)
	MetadataQueueStats(ctx context.Context) (store.MetadataQueueStats, error)
}

// AdminDeps registers operational endpoints for status, scans, and enrichment.
type AdminDeps struct {
	Store          AdminStore
	ScanFn         func(context.Context) (int64, error)
	ConfigSnapshot func() pluginrt.Config
}

// MountAdmin registers /admin/* routes on mux.
func MountAdmin(mux *http.ServeMux, st BackfillStore) {
	MountAdminWithDeps(mux, AdminDeps{Store: nil})
	mux.HandleFunc("POST /admin/metadata/backfill", func(w http.ResponseWriter, r *http.Request) {
		n, err := st.BulkEnqueueBackfill(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"queued": n})
	})
}

// MountAdminWithDeps registers the complete admin surface.
func MountAdminWithDeps(mux *http.ServeMux, deps AdminDeps) {
	if deps.Store != nil {
		mux.HandleFunc("GET /admin/diagnostics", func(w http.ResponseWriter, r *http.Request) {
			handleDiagnostics(w, r, deps)
		})
		mux.HandleFunc("GET /admin/library-paths", func(w http.ResponseWriter, r *http.Request) {
			paths, err := deps.Store.ListLibraryPaths(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": paths})
		})
		mux.HandleFunc("GET /admin/scans", func(w http.ResponseWriter, r *http.Request) {
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			events, err := deps.Store.RecentScanEvents(r.Context(), limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": events})
		})
		mux.HandleFunc("POST /admin/metadata/backfill", func(w http.ResponseWriter, r *http.Request) {
			n, err := deps.Store.BulkEnqueueBackfill(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]int64{"queued": n})
		})
		mux.HandleFunc("GET /admin/metadata/queue", func(w http.ResponseWriter, r *http.Request) {
			st, err := deps.Store.MetadataQueueStats(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, st)
		})
	}
	if deps.ScanFn != nil {
		mux.HandleFunc("POST /admin/scan", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
			defer cancel()
			id, err := deps.ScanFn(ctx)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"scan_event_id": id})
		})
	}
}

func handleDiagnostics(w http.ResponseWriter, r *http.Request, deps AdminDeps) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	db := map[string]any{"ok": false, "message": "not configured"}
	if err := deps.Store.Ping(ctx); err != nil {
		db["message"] = err.Error()
	} else {
		db["ok"] = true
		db["message"] = "database reachable"
	}
	paths, err := deps.Store.ListLibraryPaths(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	scans, err := deps.Store.RecentScanEvents(ctx, 10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	catalog, err := deps.Store.CatalogStats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	queue, err := deps.Store.MetadataQueueStats(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	cfg := pluginrt.Config{}
	if deps.ConfigSnapshot != nil {
		cfg = deps.ConfigSnapshot()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plugin_id":    "continuum.local-ebooks",
		"role":         "library_source_and_metadata_provider",
		"database":     db,
		"libraries":    paths,
		"catalog":      catalog,
		"metadata":     queue,
		"recent_scans": scans,
		"configuration": map[string]any{
			"metadata_sources_enabled": len(cfg.MetadataSourcesEnabled),
			"metadata_default_region":  cfg.MetadataDefaultRegion,
			"metadata_scan_source":     cfg.MetadataScanSource,
			"metadata_cache_ttl_days":  cfg.MetadataCacheTTLDays,
			"metadata_rate_limit_rps":  cfg.MetadataRateLimitRPS,
			"scan_inline_enrich":       cfg.ScanInlineEnrich,
		},
		"features": []string{
			"multi_library",
			"scan_status",
			"manual_scan",
			"metadata_backfill",
			"metadata_queue_status",
			"catalog_health",
		},
	})
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
