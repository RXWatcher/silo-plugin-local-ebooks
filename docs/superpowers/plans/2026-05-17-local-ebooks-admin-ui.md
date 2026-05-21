# Local Ebooks Admin Operator Console — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an admin operator console (web UI + REST API) to `continuum-plugin-local-ebooks` that owns multi-library / multi-media-type configuration, making the plugin DB the source of truth and `Configure` a non-destructive seed.

**Architecture:** Plugin DB `library_path` table is authoritative; an admin REST API does CRUD; `Configure` switches to insert-if-absent seed; a React/Vite SPA (mirroring `continuum-ebooks/web`) is embedded via `go:embed` and served under the host-admin-gated `/admin/*` route.

**Tech Stack:** Go (pgx, stdlib `net/http`), React 19 + Vite 5 + TypeScript + Tailwind 4 + shadcn/ui + @tanstack/react-query + react-router.

Spec: `docs/superpowers/specs/2026-05-17-local-ebooks-admin-ui-design.md`

Conventions: run all Go commands from the repo root `/opt/continuum_plugins/continuum-plugin-local-ebooks`. DB-backed store tests skip automatically when Postgres is unreachable (existing `internal/store` harness) — that is expected, not a failure.

---

## File Structure

Created:
- `internal/migrate/files/0003_library_path_timestamps.up.sql` / `.down.sql` — add `created_at`/`updated_at`.
- `internal/libcfg/libcfg.go` (+ `libcfg_test.go`) — pure validation (media type, path) shared by API and seed.
- `internal/server/libraries.go` (+ `libraries_test.go`) — admin library CRUD + per-library scan HTTP handlers.
- `web/` — SPA (scaffold copied from `continuum-ebooks/web`), `web/embed.go`, `web/src/{App.tsx,main.tsx,lib/api.ts,pages/*}`.
- `docs/superpowers/plans/...` (this file).

Modified:
- `internal/store/library.go` — add `CreateLibrary`, `UpdateLibrary`, `DeleteLibrary`, `SeedLibraryPath`; add `LibraryInput`/`LibraryUpdate` types; add timestamps to `LibraryPath`.
- `internal/server/admin.go` — extend `AdminStore` interface + `AdminDeps`; mount library routes; expose metadata-queue endpoint.
- `cmd/continuum-plugin-local-ebooks/main.go` — Configure uses `SeedLibraryPath`; wire library handlers + per-library scan fn + SPA static serving.
- `cmd/continuum-plugin-local-ebooks/manifest.json` — add `assets` route; mark `admin` navigable.
- `Makefile` — add web build target feeding `web/dist` before `go build`.

---

## Phase 1 — Backend

### Task 1: Migration — library_path timestamps

**Files:**
- Create: `internal/migrate/files/0003_library_path_timestamps.up.sql`
- Create: `internal/migrate/files/0003_library_path_timestamps.down.sql`

- [ ] **Step 1: Write the up migration**

Create `internal/migrate/files/0003_library_path_timestamps.up.sql`:

```sql
ALTER TABLE library_path
  ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
```

- [ ] **Step 2: Write the down migration (non-destructive)**

Create `internal/migrate/files/0003_library_path_timestamps.down.sql`:

```sql
-- no-op: dropping audit columns on rollback is not worth the data churn;
-- the runner only applies *.up.sql, this exists for symmetry.
```

- [ ] **Step 3: Verify build still compiles (embed picks up new files)**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add internal/migrate/files/0003_library_path_timestamps.up.sql internal/migrate/files/0003_library_path_timestamps.down.sql
git commit -m "feat(migrate): add created_at/updated_at to library_path"
```

---

### Task 2: Pure validation package `internal/libcfg`

**Files:**
- Create: `internal/libcfg/libcfg.go`
- Test: `internal/libcfg/libcfg_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/libcfg/libcfg_test.go`:

```go
package libcfg

import "testing"

func TestValidMediaType(t *testing.T) {
	for _, ok := range []string{"book", "comics", "manga", "documents"} {
		if !ValidMediaType(ok) {
			t.Errorf("ValidMediaType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Book", "audiobook", "x"} {
		if ValidMediaType(bad) {
			t.Errorf("ValidMediaType(%q) = true, want false", bad)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	good := map[string]string{
		"/srv/ebooks":        "/srv/ebooks",
		"/srv/ebooks/":       "/srv/ebooks",
		"/srv//a/../ebooks":  "/srv/ebooks",
	}
	for in, want := range good {
		got, err := NormalizePath(in)
		if err != nil || got != want {
			t.Errorf("NormalizePath(%q) = (%q,%v), want (%q,nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "relative/path", "ebooks", "/srv/a\x00b"} {
		if _, err := NormalizePath(bad); err == nil {
			t.Errorf("NormalizePath(%q) = nil error, want error", bad)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/libcfg/`
Expected: build failure — `undefined: ValidMediaType`, `undefined: NormalizePath`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/libcfg/libcfg.go`:

```go
// Package libcfg holds pure library-configuration validation shared by the
// admin API and the Configure seed. No I/O — filesystem existence checks
// live in the HTTP handler.
package libcfg

import (
	"errors"
	"path/filepath"
	"strings"
)

// MediaTypes is the closed set of allowed library media types (matches the
// manifest library_paths json_schema enum).
var MediaTypes = []string{"book", "comics", "manga", "documents"}

// ValidMediaType reports whether mt is one of MediaTypes.
func ValidMediaType(mt string) bool {
	for _, v := range MediaTypes {
		if mt == v {
			return true
		}
	}
	return false
}

// NormalizePath validates and cleans a library root path. It must be a
// non-empty, absolute path with no NUL byte. Returns the cleaned path.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains NUL")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	return filepath.Clean(p), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/libcfg/`
Expected: `ok  github.com/RXWatcher/continuum-plugin-local-ebooks/internal/libcfg`.

- [ ] **Step 5: Commit**

```bash
git add internal/libcfg/
git commit -m "feat(libcfg): pure media-type/path validation"
```

---

### Task 3: Store — `LibraryPath` timestamps + types

**Files:**
- Modify: `internal/store/library.go:9-22`

- [ ] **Step 1: Add timestamp fields and the input/update types**

In `internal/store/library.go`, replace the `LibraryPath` struct and add the new types directly after `LibraryPathConfig`:

```go
// LibraryPath is a configured root.
type LibraryPath struct {
	ID            int64
	Path          string
	Name          string
	MediaType     string
	Enabled       bool
	LastScannedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type LibraryPathConfig struct {
	Path      string
	Name      string
	MediaType string
}

// LibraryInput is a create request from the admin API.
type LibraryInput struct {
	Path      string
	Name      string
	MediaType string
	Enabled   bool
}

// LibraryUpdate is a mutable-fields patch (path is immutable).
type LibraryUpdate struct {
	Name      string
	MediaType string
	Enabled   bool
}
```

- [ ] **Step 2: Update `ListLibraryPaths` scan to include timestamps**

In `internal/store/library.go`, replace the `ListLibraryPaths` SQL + scan:

```go
func (s *Store) ListLibraryPaths(ctx context.Context) ([]LibraryPath, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, path, name, media_type, enabled, last_scanned_at, created_at, updated_at
		FROM library_path
		ORDER BY name ASC, id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LibraryPath
	for rows.Next() {
		var lp LibraryPath
		if err := rows.Scan(&lp.ID, &lp.Path, &lp.Name, &lp.MediaType, &lp.Enabled,
			&lp.LastScannedAt, &lp.CreatedAt, &lp.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, lp)
	}
	return out, rows.Err()
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add internal/store/library.go
git commit -m "feat(store): library_path timestamps + Library input/update types"
```

---

### Task 4: Store — CRUD + seed methods

**Files:**
- Modify: `internal/store/library.go` (append methods)

- [ ] **Step 1: Add `SeedLibraryPath`, `CreateLibrary`, `UpdateLibrary`, `DeleteLibrary`**

Append to `internal/store/library.go` (before `MarkLibraryScanned`):

```go
// SeedLibraryPath is the non-destructive Configure seed: it creates the row
// only if the path is absent and never modifies an existing (UI-managed) row.
func (s *Store) SeedLibraryPath(ctx context.Context, cfg LibraryPathConfig) error {
	name := cfg.Name
	if name == "" {
		name = cfg.Path
	}
	mt := cfg.MediaType
	if mt == "" {
		mt = "book"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO library_path (path, name, media_type, enabled)
		VALUES ($1, $2, $3, TRUE)
		ON CONFLICT (path) DO NOTHING
	`, cfg.Path, name, mt)
	return err
}

// CreateLibrary inserts a new library. Returns ErrDuplicatePath when path
// already exists (the caller maps it to HTTP 409).
func (s *Store) CreateLibrary(ctx context.Context, in LibraryInput) (int64, error) {
	name := in.Name
	if name == "" {
		name = in.Path
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO library_path (path, name, media_type, enabled)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, in.Path, name, in.MediaType, in.Enabled).Scan(&id)
	if err != nil && isUniqueViolation(err) {
		return 0, ErrDuplicatePath
	}
	return id, err
}

// UpdateLibrary mutates name/media_type/enabled (path is immutable).
// Returns ErrNotFound when no row matches id.
func (s *Store) UpdateLibrary(ctx context.Context, id int64, u LibraryUpdate) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE library_path
		   SET name = $2, media_type = $3, enabled = $4, updated_at = now()
		 WHERE id = $1
	`, id, u.Name, u.MediaType, u.Enabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteLibrary removes a library and its derived catalog rows in one
// transaction (metadata jobs + covers for its ebooks, then ebooks, then the
// library_path row). On-disk files are untouched. Returns ErrNotFound when
// no row matches id.
func (s *Store) DeleteLibrary(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		DELETE FROM metadata_enrichment_job
		 WHERE ebook_id IN (SELECT id FROM ebook WHERE library_path_id = $1)
	`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM cover
		 WHERE ebook_id IN (SELECT id FROM ebook WHERE library_path_id = $1)
	`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM ebook WHERE library_path_id = $1`, id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM library_path WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}
```

- [ ] **Step 2: Add the sentinel errors + unique-violation helper**

Open `internal/store/store.go` and confirm whether `ErrNotFound` exists (it is used by `internal/grpc/ebookbackend`). Run:

`grep -n "ErrNotFound\|ErrDuplicatePath\|pgconn" internal/store/store.go`

If `ErrNotFound` is **not** declared in `internal/store`, add this block to `internal/store/store.go` after the imports; if `ErrNotFound` already exists, add only `ErrDuplicatePath` and the helper:

```go
import "github.com/jackc/pgx/v5/pgconn" // add to the existing import group

var (
	ErrNotFound      = errors.New("not found")       // omit if already declared
	ErrDuplicatePath = errors.New("duplicate path")
)

// isUniqueViolation reports whether err is a Postgres unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

(Ensure `errors` is imported in `store.go`.)

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: exit 0. If it reports `ErrNotFound` redeclared, delete the duplicate line from Step 2.

- [ ] **Step 4: Run store tests (DB-backed; skip OK)**

Run: `go test ./internal/store/`
Expected: `ok` or `SKIP: postgres unreachable` — not FAIL/compile error.

- [ ] **Step 5: Commit**

```bash
git add internal/store/library.go internal/store/store.go
git commit -m "feat(store): library CRUD + non-destructive seed"
```

---

### Task 5: Configure uses the non-destructive seed

**Files:**
- Modify: `cmd/continuum-plugin-local-ebooks/main.go:160-166`

- [ ] **Step 1: Replace the destructive upsert loop with the seed**

In `cmd/continuum-plugin-local-ebooks/main.go`, replace the library config loop (currently calling `st.UpsertLibraryPathConfig`):

```go
		for _, lib := range cfg.Libraries {
			if err := st.SeedLibraryPath(ctx, store.LibraryPathConfig{
				Path:      lib.Path,
				Name:      lib.Name,
				MediaType: lib.MediaType,
			}); err != nil {
				logger.Warn("seed library_path", "path", lib.Path, "err", err)
			}
		}
```

(The exact surrounding field names — `lib.Path`/`lib.Name`/`lib.MediaType` — match `runtime.LibraryConfig`. Keep the original `if` arity: it previously used `if _, err := st.UpsertLibraryPathConfig(...)`; the new call returns only `error`.)

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add cmd/continuum-plugin-local-ebooks/main.go
git commit -m "feat: Configure seeds libraries non-destructively (plugin DB is source of truth)"
```

---

### Task 6: Admin library CRUD HTTP handlers

**Files:**
- Create: `internal/server/libraries.go`
- Test: `internal/server/libraries_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/server/libraries_test.go`:

```go
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestCreateLibrary`
Expected: build failure — `undefined: MountLibraryRoutes`, `LibraryDeps`.

- [ ] **Step 3: Write the implementation**

Create `internal/server/libraries.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/libcfg"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
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
	mux.HandleFunc("GET /admin/libraries", func(w http.ResponseWriter, r *http.Request) {
		rows, err := deps.Store.ListLibraryPaths(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": rows})
	})

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
		if deps.DirExists != nil && !deps.DirExists(path) {
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

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, errors.New("invalid id"))
		return 0, false
	}
	return id, true
}
```

(`writeJSON`/`writeError` already exist in `internal/server/admin.go`, same package.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestCreateLibrary|TestDeleteLibrary'`
Expected: PASS.

- [ ] **Step 5: Run vet + full server tests**

Run: `go vet ./... && go test ./internal/server/`
Expected: vet clean; PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/libraries.go internal/server/libraries_test.go
git commit -m "feat(server): admin library CRUD + per-library scan endpoints"
```

---

### Task 7: Wire library routes + per-library scan + metadata-queue endpoint into main

**Files:**
- Modify: `cmd/continuum-plugin-local-ebooks/main.go` (Configure callback, near `server.MountAdminWithDeps`, ~line 205)
- Modify: `internal/server/admin.go` (add `GET /admin/metadata/queue`)

- [ ] **Step 1: Add the metadata-queue endpoint**

In `internal/server/admin.go`, inside `MountAdminWithDeps`, in the `if deps.Store != nil {` block, add:

```go
		mux.HandleFunc("GET /admin/metadata/queue", func(w http.ResponseWriter, r *http.Request) {
			st, err := deps.Store.MetadataQueueStats(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, st)
		})
```

(`MetadataQueueStats` is already in the `AdminStore` interface — confirm with `grep -n MetadataQueueStats internal/server/admin.go`.)

- [ ] **Step 2: Wire library routes + per-library scan in main**

In `cmd/continuum-plugin-local-ebooks/main.go`, immediately after the existing `server.MountAdminWithDeps(mux, server.AdminDeps{...})` call, add:

```go
		server.MountLibraryRoutes(mux, server.LibraryDeps{
			Store:     st,
			DirExists: func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
			ScanOne: func(ctx context.Context, lpID int64) (int64, error) {
				return runScanOne(ctx, lpID)
			},
		})
```

- [ ] **Step 3: Add `runScanOne` next to `runScan`**

In `cmd/continuum-plugin-local-ebooks/main.go`, factor a single-library scan beside `runScan` (reuses `scanner.Walk` + the scan_event audit path). Add this function near `runScan`:

```go
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
```

(`scanMu`, `storePtr`, `queuePtr`, `slogger`, `logger`, `scanner`, `store`, `fmt`, `os`, `context` are already in scope/imported in `main.go` — verify imports include `os` and `context`; add if missing. `runScanOne` must be declared before the `LibraryDeps` block that references it; place it just above the `server.MountLibraryRoutes` call.)

- [ ] **Step 4: Verify build + vet + tests**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: build/vet clean; all packages `ok` (store may SKIP without Postgres).

- [ ] **Step 5: Commit**

```bash
git add cmd/continuum-plugin-local-ebooks/main.go internal/server/admin.go
git commit -m "feat: wire library admin routes, per-library scan, metadata queue endpoint"
```

---

## Phase 2 — Frontend scaffold

### Task 8: Copy the proven SPA toolchain from the ebooks portal

**Files:**
- Create: `web/` (config + scaffold copied from `/opt/continuum_plugins/continuum-plugin-ebooks/web`)
- Create: `web/embed.go`

- [ ] **Step 1: Copy build config + embed (verbatim, proven setup)**

```bash
cd /opt/continuum_plugins/continuum-plugin-local-ebooks
mkdir -p web/src
cp /opt/continuum_plugins/continuum-plugin-ebooks/web/{package.json,pnpm-lock.yaml,vite.config.ts,tsconfig.json,tsconfig.node.json,components.json,index.html,embed.go,.gitignore} web/ 2>/dev/null || true
cp -r /opt/continuum_plugins/continuum-plugin-ebooks/web/src/components/ui web/src/components/ui
cp /opt/continuum_plugins/continuum-plugin-ebooks/web/src/index.css web/src/index.css
cp /opt/continuum_plugins/continuum-plugin-ebooks/web/src/lib/utils.ts web/src/lib/utils.ts
cp /opt/continuum_plugins/continuum-plugin-ebooks/web/src/lib/queryClient.ts web/src/lib/queryClient.ts
```

- [ ] **Step 2: Confirm `web/embed.go` package + go:embed**

Open `web/embed.go`; it must declare `package web`, `//go:embed all:dist`, and export `FSEmbed()`/`FS()`. It is identical to the ebooks portal's; no edits needed. If `web/.gitignore` was copied, confirm it ignores `node_modules` and `dist`.

- [ ] **Step 3: Install deps and verify the toolchain builds an empty app**

Create a minimal `web/src/main.tsx` placeholder so the build has an entry:

```tsx
import { createRoot } from "react-dom/client";
createRoot(document.getElementById("root")!).render(<div>Local Ebooks Admin</div>);
```

Run:

```bash
cd web && (command -v pnpm >/dev/null && pnpm install --frozen-lockfile || npm install) && (pnpm run build 2>/dev/null || npm run build)
```

Expected: `dist/` produced, `✓ built`.

- [ ] **Step 4: Verify Go embed compiles against produced dist**

Run: `cd /opt/continuum_plugins/continuum-plugin-local-ebooks && go build ./...`
Expected: exit 0 (embed finds `web/dist`).

- [ ] **Step 5: Commit**

```bash
cd /opt/continuum_plugins/continuum-plugin-local-ebooks
git add web/.gitignore web/package.json web/pnpm-lock.yaml web/vite.config.ts web/tsconfig.json web/tsconfig.node.json web/components.json web/index.html web/embed.go web/src/
git commit -m "chore(web): scaffold SPA toolchain (copied from ebooks portal)"
```

---

### Task 9: Serve the SPA + manifest routes + Makefile

**Files:**
- Modify: `cmd/continuum-plugin-local-ebooks/main.go` (mux: static + SPA fallback)
- Modify: `cmd/continuum-plugin-local-ebooks/manifest.json`
- Modify: `Makefile`

- [ ] **Step 1: Add static + SPA fallback to the mux**

In `cmd/continuum-plugin-local-ebooks/main.go`, after all `mux.HandleFunc(...)` admin/api registrations and before `httpSrv.SetHandler(mux)`, add (import `web "github.com/RXWatcher/continuum-plugin-local-ebooks/web"` and `io/fs`, `strings`, `net/http` as needed):

```go
		webFS := web.FS()
		fileSrv := http.FileServer(webFS)
		mux.Handle("GET /assets/", fileSrv)
		mux.HandleFunc("GET /admin/", func(w http.ResponseWriter, r *http.Request) {
			// Asset requests under /admin/assets/ map to the bundle root.
			p := strings.TrimPrefix(r.URL.Path, "/admin")
			if strings.HasPrefix(p, "/assets/") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = p
				fileSrv.ServeHTTP(w, r2)
				return
			}
			// SPA entrypoint for every other /admin* path.
			f, err := webFS.Open("index.html")
			if err != nil {
				http.Error(w, "ui not built", http.StatusInternalServerError)
				return
			}
			_ = f.Close()
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/index.html"
			fileSrv.ServeHTTP(w, r2)
		})
```

(Place these registrations LAST so the specific `/admin/libraries`, `/admin/scan`, etc. patterns win under Go 1.22 ServeMux precedence. `GET /admin/{$}` and `GET /admin/` are less specific than `GET /admin/libraries`.)

- [ ] **Step 2: Update the manifest**

In `cmd/continuum-plugin-local-ebooks/manifest.json`, in `http_routes`, add an `assets` route and mark `admin` navigable. The `http_routes` array becomes:

```json
  "http_routes": [
    {"id": "assets", "method": "GET", "path": "/assets/*", "access": "public"},
    {"id": "api",    "method": "*",   "path": "/api/v1/*", "access": "authenticated"},
    {"id": "admin",  "method": "*",   "path": "/admin/*",  "access": "admin", "navigable": true, "navigation_label": "Local Ebooks", "navigation_kind": "admin"}
  ]
```

- [ ] **Step 3: Makefile web build target**

In `Makefile`, ensure the default build builds the web bundle first. Add (or fold into the existing build target):

```make
.PHONY: web
web:
	cd web && (command -v pnpm >/dev/null && pnpm install --frozen-lockfile && pnpm run build || npm install && npm run build)

build: web
	go build ./...
```

(If `build` already exists, add `web` as its first prerequisite rather than duplicating.)

- [ ] **Step 4: Verify**

Run: `cd /opt/continuum_plugins/continuum-plugin-local-ebooks && make web && go build ./... && go vet ./...`
Expected: web `✓ built`; Go build/vet exit 0.

- [ ] **Step 5: Commit**

```bash
git add cmd/continuum-plugin-local-ebooks/main.go cmd/continuum-plugin-local-ebooks/manifest.json Makefile
git commit -m "feat: serve embedded admin SPA under /admin; manifest assets+navigable"
```

---

## Phase 3 — Frontend app

### Task 10: API client

**Files:**
- Create: `web/src/lib/api.ts`

- [ ] **Step 1: Write the API client**

Create `web/src/lib/api.ts`:

```ts
// Admin API base: routes are mounted at /admin on the plugin proxy path
// /api/v1/plugins/{installId}. Detect that prefix at runtime.
function base(): string {
  const m = window.location.pathname.match(/^(\/api\/v1\/plugins\/\d+)/);
  return (m ? m[1] : "") + "/admin";
}

let token: string | null = null;
(function captureToken() {
  const p = new URLSearchParams(window.location.search);
  const t = p.get("token");
  if (t) {
    token = t;
    p.delete("token");
    window.history.replaceState(null, "", window.location.pathname +
      (p.toString() ? "?" + p.toString() : "") + window.location.hash);
  }
})();

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`${base()}${path}`, {
    method, headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: "include",
  });
  if (!res.ok) {
    const e = await res.json().catch(() => ({ error: res.statusText }));
    throw new Error(e.error?.message ?? e.error ?? `Request failed (${res.status})`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export type Library = {
  ID: number; Path: string; Name: string; MediaType: string;
  Enabled: boolean; LastScannedAt?: string | null;
};
export type ScanEvent = {
  id: number; library_name?: string; started_at: string;
  finished_at?: string | null; books_added: number; books_changed: number;
  books_deleted: number; error_text?: string;
};

export const listLibraries = () => call<{ items: Library[] }>("GET", "/libraries");
export const createLibrary = (b: { path: string; name: string; media_type: string; enabled: boolean }) =>
  call<{ id: number }>("POST", "/libraries", b);
export const updateLibrary = (id: number, b: { name: string; media_type: string; enabled: boolean }) =>
  call("PATCH", `/libraries/${id}`, b);
export const deleteLibrary = (id: number) => call("DELETE", `/libraries/${id}`);
export const scanLibrary = (id: number) => call<{ scan_event_id: number }>("POST", `/libraries/${id}/scan`);
export const scanAll = () => call<{ scan_event_id: number }>("POST", "/scan");
export const listScans = () => call<{ items: ScanEvent[] }>("GET", "/scans");
export const metadataQueue = () => call<Record<string, number>>("GET", "/metadata/queue");
export const metadataBackfill = () => call<{ queued: number }>("POST", "/metadata/backfill");
export const diagnostics = () => call<Record<string, unknown>>("GET", "/diagnostics");

export const MEDIA_TYPES = ["book", "comics", "manga", "documents"] as const;
```

- [ ] **Step 2: Typecheck**

Run: `cd web && (pnpm run build || npm run build)`
Expected: builds (no app yet referencing it is fine; tsc passes).

- [ ] **Step 3: Commit**

```bash
cd /opt/continuum_plugins/continuum-plugin-local-ebooks
git add web/src/lib/api.ts
git commit -m "feat(web): admin API client"
```

---

### Task 11: App shell + Libraries page

**Files:**
- Create: `web/src/main.tsx` (replace placeholder)
- Create: `web/src/App.tsx`
- Create: `web/src/pages/Libraries.tsx`

- [ ] **Step 1: main.tsx (query client + render)**

Replace `web/src/main.tsx`:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { queryClient } from "@/lib/queryClient";
import { Toaster } from "@/components/ui/sonner";
import App from "./App";
import "./index.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
      <Toaster />
    </QueryClientProvider>
  </StrictMode>,
);
```

- [ ] **Step 2: App.tsx (tabs shell)**

Create `web/src/App.tsx`:

```tsx
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import Libraries from "./pages/Libraries";

export default function App() {
  return (
    <div className="mx-auto max-w-6xl space-y-4 p-6">
      <h1 className="text-2xl font-semibold">Local Ebooks — Operator Console</h1>
      <Tabs defaultValue="libraries">
        <TabsList>
          <TabsTrigger value="libraries">Libraries</TabsTrigger>
          <TabsTrigger value="scans">Scans</TabsTrigger>
          <TabsTrigger value="metadata">Metadata</TabsTrigger>
          <TabsTrigger value="diagnostics">Diagnostics</TabsTrigger>
        </TabsList>
        <TabsContent value="libraries"><Libraries /></TabsContent>
        <TabsContent value="scans"><div className="text-sm text-muted-foreground">See Task 12.</div></TabsContent>
        <TabsContent value="metadata"><div className="text-sm text-muted-foreground">See Task 12.</div></TabsContent>
        <TabsContent value="diagnostics"><div className="text-sm text-muted-foreground">See Task 12.</div></TabsContent>
      </Tabs>
    </div>
  );
}
```

- [ ] **Step 3: Libraries.tsx (CRUD + per-library scan)**

Create `web/src/pages/Libraries.tsx`:

```tsx
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  createLibrary, deleteLibrary, listLibraries, scanLibrary,
  updateLibrary, MEDIA_TYPES, type Library,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";

export default function Libraries() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["libraries"], queryFn: listLibraries });
  const [form, setForm] = useState({ path: "", name: "", media_type: "book", enabled: true });

  const invalidate = () => qc.invalidateQueries({ queryKey: ["libraries"] });
  const create = useMutation({
    mutationFn: () => createLibrary(form),
    onSuccess: () => { toast.success("Library created"); setForm({ path: "", name: "", media_type: "book", enabled: true }); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const patch = useMutation({
    mutationFn: (l: Library) => updateLibrary(l.ID, { name: l.Name, media_type: l.MediaType, enabled: l.Enabled }),
    onSuccess: () => { toast.success("Saved"); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const remove = useMutation({
    mutationFn: (id: number) => deleteLibrary(id),
    onSuccess: () => { toast.success("Library removed"); invalidate(); },
    onError: (e: Error) => toast.error(e.message),
  });
  const scan = useMutation({
    mutationFn: (id: number) => scanLibrary(id),
    onSuccess: () => toast.success("Scan started"),
    onError: (e: Error) => toast.error(e.message),
  });

  if (q.isLoading) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (q.error) return <p className="text-sm text-destructive">{(q.error as Error).message}</p>;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-2 rounded-md border p-3">
        <Input placeholder="/srv/comics" value={form.path}
          onChange={(e) => setForm({ ...form, path: e.target.value })} className="w-64" />
        <Input placeholder="Name" value={form.name}
          onChange={(e) => setForm({ ...form, name: e.target.value })} className="w-40" />
        <select className="h-9 rounded-md border px-2 text-sm" value={form.media_type}
          onChange={(e) => setForm({ ...form, media_type: e.target.value })}>
          {MEDIA_TYPES.map((m) => <option key={m} value={m}>{m}</option>)}
        </select>
        <label className="flex items-center gap-1 text-sm">
          <input type="checkbox" checked={form.enabled}
            onChange={(e) => setForm({ ...form, enabled: e.target.checked })} /> enabled
        </label>
        <Button onClick={() => create.mutate()} disabled={create.isPending}>Add library</Button>
      </div>

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Path</TableHead><TableHead>Name</TableHead>
            <TableHead>Media type</TableHead><TableHead>Enabled</TableHead>
            <TableHead>Last scanned</TableHead><TableHead></TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(q.data?.items ?? []).map((l) => (
            <TableRow key={l.ID}>
              <TableCell className="font-mono text-xs">{l.Path}</TableCell>
              <TableCell>
                <Input defaultValue={l.Name} className="h-8"
                  onBlur={(e) => e.target.value !== l.Name && patch.mutate({ ...l, Name: e.target.value })} />
              </TableCell>
              <TableCell>
                <select className="h-8 rounded-md border px-2 text-sm" value={l.MediaType}
                  onChange={(e) => patch.mutate({ ...l, MediaType: e.target.value })}>
                  {MEDIA_TYPES.map((m) => <option key={m} value={m}>{m}</option>)}
                </select>
              </TableCell>
              <TableCell>
                <input type="checkbox" checked={l.Enabled}
                  onChange={(e) => patch.mutate({ ...l, Enabled: e.target.checked })} />
              </TableCell>
              <TableCell className="text-xs">{l.LastScannedAt ?? "never"}</TableCell>
              <TableCell className="flex gap-1">
                <Button size="sm" variant="outline" onClick={() => scan.mutate(l.ID)}>Scan</Button>
                <Button size="sm" variant="destructive"
                  onClick={() => {
                    if (confirm(`Remove "${l.Name}"? This deletes its catalog entries (files on disk are untouched).`))
                      remove.mutate(l.ID);
                  }}>Remove</Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
```

- [ ] **Step 4: Build/typecheck**

Run: `cd web && (pnpm run build || npm run build)`
Expected: `✓ built`, no TS errors. (If `@/components/ui/sonner` or `table` is missing, it was not copied in Task 8 — re-copy from the ebooks `web/src/components/ui/`.)

- [ ] **Step 5: Verify Go embed builds with the real bundle**

Run: `cd /opt/continuum_plugins/continuum-plugin-local-ebooks && go build ./...`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add web/src/main.tsx web/src/App.tsx web/src/pages/Libraries.tsx
git commit -m "feat(web): app shell + Libraries CRUD page"
```

---

### Task 12: Scans, Metadata, Diagnostics pages

**Files:**
- Create: `web/src/pages/Scans.tsx`, `web/src/pages/Metadata.tsx`, `web/src/pages/Diagnostics.tsx`
- Modify: `web/src/App.tsx` (wire the three tabs)

- [ ] **Step 1: Scans page**

Create `web/src/pages/Scans.tsx`:

```tsx
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { listScans, scanAll } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export default function Scans() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["scans"], queryFn: listScans });
  const all = useMutation({
    mutationFn: scanAll,
    onSuccess: () => { toast.success("Scan started"); qc.invalidateQueries({ queryKey: ["scans"] }); },
    onError: (e: Error) => toast.error(e.message),
  });
  if (q.error) return <p className="text-sm text-destructive">{(q.error as Error).message}</p>;
  return (
    <div className="space-y-3">
      <Button onClick={() => all.mutate()} disabled={all.isPending}>Scan all libraries</Button>
      <Table>
        <TableHeader><TableRow>
          <TableHead>Library</TableHead><TableHead>Started</TableHead><TableHead>Finished</TableHead>
          <TableHead>+/~/-</TableHead><TableHead>Error</TableHead>
        </TableRow></TableHeader>
        <TableBody>
          {(q.data?.items ?? []).map((s) => (
            <TableRow key={s.id}>
              <TableCell>{s.library_name || "all"}</TableCell>
              <TableCell className="text-xs">{s.started_at}</TableCell>
              <TableCell className="text-xs">{s.finished_at ?? "running"}</TableCell>
              <TableCell>{s.books_added}/{s.books_changed}/{s.books_deleted}</TableCell>
              <TableCell className="text-xs text-destructive">{s.error_text}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
```

- [ ] **Step 2: Metadata page**

Create `web/src/pages/Metadata.tsx`:

```tsx
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { metadataBackfill, metadataQueue } from "@/lib/api";
import { Button } from "@/components/ui/button";

export default function Metadata() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["meta-queue"], queryFn: metadataQueue });
  const backfill = useMutation({
    mutationFn: metadataBackfill,
    onSuccess: (r) => { toast.success(`Queued ${r.queued}`); qc.invalidateQueries({ queryKey: ["meta-queue"] }); },
    onError: (e: Error) => toast.error(e.message),
  });
  if (q.error) return <p className="text-sm text-destructive">{(q.error as Error).message}</p>;
  return (
    <div className="space-y-3">
      <pre className="rounded-md border bg-muted/30 p-3 text-xs">{JSON.stringify(q.data ?? {}, null, 2)}</pre>
      <Button onClick={() => backfill.mutate()} disabled={backfill.isPending}>Backfill all</Button>
    </div>
  );
}
```

- [ ] **Step 3: Diagnostics page**

Create `web/src/pages/Diagnostics.tsx`:

```tsx
import { useQuery } from "@tanstack/react-query";
import { diagnostics } from "@/lib/api";

export default function Diagnostics() {
  const q = useQuery({ queryKey: ["diagnostics"], queryFn: diagnostics });
  if (q.error) return <p className="text-sm text-destructive">{(q.error as Error).message}</p>;
  return (
    <pre className="rounded-md border bg-muted/30 p-3 text-xs">
      {JSON.stringify(q.data ?? {}, null, 2)}
    </pre>
  );
}
```

- [ ] **Step 4: Wire the tabs in App.tsx**

In `web/src/App.tsx`, replace the three placeholder `TabsContent` bodies and add imports:

```tsx
import Scans from "./pages/Scans";
import Metadata from "./pages/Metadata";
import Diagnostics from "./pages/Diagnostics";
```

```tsx
        <TabsContent value="scans"><Scans /></TabsContent>
        <TabsContent value="metadata"><Metadata /></TabsContent>
        <TabsContent value="diagnostics"><Diagnostics /></TabsContent>
```

- [ ] **Step 5: Build/typecheck + Go build**

Run: `cd web && (pnpm run build || npm run build) && cd .. && go build ./...`
Expected: `✓ built`, no TS errors; Go exit 0.

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/Scans.tsx web/src/pages/Metadata.tsx web/src/pages/Diagnostics.tsx web/src/App.tsx
git commit -m "feat(web): Scans, Metadata, Diagnostics pages"
```

---

## Phase 4 — Final verification

### Task 13: Full verification

- [ ] **Step 1: Backend**

Run: `cd /opt/continuum_plugins/continuum-plugin-local-ebooks && go build ./... && go vet ./... && go test ./...`
Expected: build/vet clean; all packages `ok` (`internal/store` may SKIP without Postgres — acceptable).

- [ ] **Step 2: Frontend**

Run: `cd web && (pnpm run build || npm run build)`
Expected: `✓ built`, zero TS errors.

- [ ] **Step 3: Manifest sanity**

Run: `python3 -c "import json,sys; json.load(open('cmd/continuum-plugin-local-ebooks/manifest.json'))" && echo OK`
Expected: `OK`.

- [ ] **Step 4: Final commit (if anything uncommitted)**

```bash
git status --porcelain
git add -A && git commit -m "chore: local-ebooks admin console — final verification" || true
```

---

## Self-Review (completed by plan author)

- **Spec coverage:** §3 serving → Tasks 8,9; §4 source-of-truth/seed → Tasks 4,5; §5 data model/migration → Tasks 1,3,4; §6 REST API + 4 decisions → Tasks 4,6,7 (immutable path = no path in `LibraryUpdate`; hard cascade = `DeleteLibrary` tx; seed = `SeedLibraryPath`; per-library scan = `runScanOne`); §7 SPA pages → Tasks 11,12; §8 testing → Tasks 2,6 (pure + handler tests; store DB-skip harness); §9 build impact → Tasks 8,9. No uncovered spec requirement.
- **Placeholder scan:** no TBD/TODO; every code step shows full code; copy steps reference exact source paths. The App.tsx "See Task 12" strings are interim UI text intentionally replaced in Task 12, not plan placeholders.
- **Type consistency:** `LibraryInput`/`LibraryUpdate` (Task 3) used identically in Tasks 4,6,7; `LibraryStore`/`LibraryDeps`/`MountLibraryRoutes` consistent Tasks 6↔7; store sentinels `ErrNotFound`/`ErrDuplicatePath` consistent Tasks 4,6; API client names match handler routes Task 6↔10; `runScanOne` declared before use (Task 7).
