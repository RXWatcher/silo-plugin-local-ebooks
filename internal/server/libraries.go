package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/libcfg"
	"github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/store"
)

// LibraryStore is the store surface the library admin handlers need.
type LibraryStore interface {
	ListLibraryPaths(ctx context.Context) ([]store.LibraryPath, error)
	CreateLibrary(ctx context.Context, in store.LibraryInput) (int64, error)
	UpdateLibrary(ctx context.Context, id int64, u store.LibraryUpdate) error
	DeleteLibrary(ctx context.Context, id int64) error
}

// LibraryDeps wires the handlers. DirExists is injected so tests don't touch
// the filesystem; production passes a real os.Stat-based check. ScanOne, when
// set, triggers a scan of one library_path id.
type LibraryDeps struct {
	Store     LibraryStore
	DirExists func(path string) bool
	ScanOne   func(ctx context.Context, libraryPathID int64) (int64, error)
}

// MountLibraryRoutes registers /admin/libraries* routes on mux.
func MountLibraryRoutes(mux *http.ServeMux, deps LibraryDeps) {
	dirExists := deps.DirExists
	if dirExists == nil {
		dirExists = func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		}
	}

	mux.HandleFunc("GET /admin/libraries", func(w http.ResponseWriter, r *http.Request) {
		rows, err := deps.Store.ListLibraryPaths(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})

	mux.HandleFunc("GET /admin/filesystem/browse", handleFilesystemBrowse)

	mux.HandleFunc("POST /admin/libraries", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			MediaType string `json:"media_type"`
			Enabled   bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		path, err := libcfg.NormalizePath(body.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !libcfg.ValidMediaType(body.MediaType) {
			writeError(w, http.StatusBadRequest, errors.New("invalid media_type"))
			return
		}
		if !dirExists(path) {
			writeError(w, http.StatusBadRequest, errors.New("path is not an existing directory"))
			return
		}
		id, err := deps.Store.CreateLibrary(r.Context(), store.LibraryInput{
			Path: path, Name: body.Name, MediaType: body.MediaType, Enabled: body.Enabled,
		})
		if errors.Is(err, store.ErrDuplicatePath) {
			writeError(w, http.StatusConflict, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	})

	// PATCH is full-fields: callers send name+media_type+enabled together
	// (the admin SPA always does), so media_type is required/validated.
	mux.HandleFunc("PATCH /admin/libraries/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var body struct {
			Name      string `json:"name"`
			MediaType string `json:"media_type"`
			Enabled   bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !libcfg.ValidMediaType(body.MediaType) {
			writeError(w, http.StatusBadRequest, errors.New("invalid media_type"))
			return
		}
		err := deps.Store.UpdateLibrary(r.Context(), id, store.LibraryUpdate{
			Name: body.Name, MediaType: body.MediaType, Enabled: body.Enabled,
		})
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("DELETE /admin/libraries/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		err := deps.Store.DeleteLibrary(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /admin/libraries/{id}/scan", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if deps.ScanOne == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("scan not available"))
			return
		}
		evID, err := deps.ScanOne(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"scan_event_id": evID})
	})
}

type filesystemBrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type filesystemBrowseResponse struct {
	Path    string                  `json:"path"`
	Parent  string                  `json:"parent"`
	Entries []filesystemBrowseEntry `json:"entries"`
}

func handleFilesystemBrowse(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = string(filepath.Separator)
	}
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, errors.New("path must be an absolute path"))
		return
	}

	cleaned := filepath.Clean(path)
	info, err := os.Stat(cleaned)
	if err != nil {
		if os.IsNotExist(err) {
			writeError(w, http.StatusNotFound, errors.New("directory not found"))
		} else {
			writeError(w, http.StatusBadRequest, errors.New("invalid path"))
		}
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, errors.New("path must point to a directory"))
		return
	}

	entries, err := os.ReadDir(cleaned)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("failed to read directory"))
		return
	}

	result := make([]filesystemBrowseEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		result = append(result, filesystemBrowseEntry{Name: name, Path: filepath.Join(cleaned, name)})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Path < result[j].Path
		}
		return result[i].Name < result[j].Name
	})

	parent := filepath.Dir(cleaned)
	if cleaned == string(filepath.Separator) || parent == "." || parent == cleaned {
		parent = cleaned
	}
	writeJSON(w, http.StatusOK, filesystemBrowseResponse{Path: cleaned, Parent: parent, Entries: result})
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid id"))
		return 0, false
	}
	return id, true
}
