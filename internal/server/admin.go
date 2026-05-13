package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// BackfillStore is the surface admin.go needs from *store.Store.
type BackfillStore interface {
	BulkEnqueueBackfill(ctx context.Context) (int64, error)
}

// MountAdmin registers /admin/* routes on mux.
func MountAdmin(mux *http.ServeMux, st BackfillStore) {
	mux.HandleFunc("POST /admin/metadata/backfill", func(w http.ResponseWriter, r *http.Request) {
		n, err := st.BulkEnqueueBackfill(r.Context())
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"queued": n})
	})
}
