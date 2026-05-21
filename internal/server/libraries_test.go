package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

type fakeLibStore struct {
	created   store.LibraryInput
	createErr error
	updateErr error
	deleteErr error
	updated   bool
	deleted   bool
}

func (f *fakeLibStore) ListLibraryPaths(context.Context) ([]store.LibraryPath, error) {
	return []store.LibraryPath{{ID: 1, Path: "/a", Name: "A", MediaType: "book", Enabled: true}}, nil
}
func (f *fakeLibStore) CreateLibrary(_ context.Context, in store.LibraryInput) (int64, error) {
	f.created = in
	return 7, f.createErr
}
func (f *fakeLibStore) UpdateLibrary(context.Context, int64, store.LibraryUpdate) error {
	f.updated = true
	return f.updateErr
}
func (f *fakeLibStore) DeleteLibrary(context.Context, int64) error {
	f.deleted = true
	return f.deleteErr
}

func newLibMux(fs *fakeLibStore) *http.ServeMux {
	mux := http.NewServeMux()
	MountLibraryRoutes(mux, LibraryDeps{Store: fs, DirExists: func(string) bool { return true }})
	return mux
}

func TestCreateLibrary_OK(t *testing.T) {
	fs := &fakeLibStore{}
	mux := newLibMux(fs)
	body, _ := json.Marshal(map[string]any{"path": "/srv/comics/", "name": "Comics", "media_type": "comics", "enabled": true})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/libraries", bytes.NewReader(body)))
	if rec.Code != 201 {
		t.Fatalf("status = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	if fs.created.Path != "/srv/comics" || fs.created.MediaType != "comics" {
		t.Fatalf("created = %+v (path must be normalized)", fs.created)
	}
}

func TestCreateLibrary_BadMediaType(t *testing.T) {
	mux := newLibMux(&fakeLibStore{})
	body, _ := json.Marshal(map[string]any{"path": "/srv/x", "media_type": "audiobook"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/libraries", bytes.NewReader(body)))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateLibrary_NonDirIs400(t *testing.T) {
	mux := http.NewServeMux()
	MountLibraryRoutes(mux, LibraryDeps{Store: &fakeLibStore{}, DirExists: func(string) bool { return false }})
	body, _ := json.Marshal(map[string]any{"path": "/srv/missing", "media_type": "book"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/libraries", bytes.NewReader(body)))
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCreateLibrary_DuplicateIs409(t *testing.T) {
	mux := newLibMux(&fakeLibStore{createErr: store.ErrDuplicatePath})
	body, _ := json.Marshal(map[string]any{"path": "/srv/x", "media_type": "book"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/libraries", bytes.NewReader(body)))
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestDeleteLibrary_NotFoundIs404(t *testing.T) {
	mux := newLibMux(&fakeLibStore{deleteErr: store.ErrNotFound})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/admin/libraries/9", nil))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	// (delete path still calls the store before mapping the error)
}

func TestUpdateLibrary_OK(t *testing.T) {
	fs := &fakeLibStore{}
	mux := newLibMux(fs)
	body, _ := json.Marshal(map[string]any{"name": "New", "media_type": "book", "enabled": true})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/admin/libraries/1", bytes.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if !fs.updated {
		t.Fatal("UpdateLibrary was not called")
	}
}

func TestUpdateLibrary_NotFoundIs404(t *testing.T) {
	mux := newLibMux(&fakeLibStore{updateErr: store.ErrNotFound})
	body, _ := json.Marshal(map[string]any{"name": "x", "media_type": "book", "enabled": true})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PATCH", "/admin/libraries/1", bytes.NewReader(body)))
	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestFilesystemBrowseListsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Ebooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "book.epub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := newLibMux(&fakeLibStore{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/filesystem/browse?path="+root, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"path":"`+root+`"`) {
		t.Fatalf("body missing current path: %s", body)
	}
	if !strings.Contains(body, `"name":"Ebooks"`) {
		t.Fatalf("body missing child directory: %s", body)
	}
	if strings.Contains(body, "book.epub") {
		t.Fatalf("body included file entry: %s", body)
	}
}

func TestFilesystemBrowseRejectsRelativePath(t *testing.T) {
	mux := newLibMux(&fakeLibStore{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/filesystem/browse?path=relative", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}
