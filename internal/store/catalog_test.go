package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/migrate"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

func newCatalogPool(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dsn := os.Getenv("EBOOKSDB_TEST_DSN")
	if dsn == "" {
		t.Skip("EBOOKSDB_TEST_DSN unset; skipping catalog integration test")
	}
	ctx := context.Background()
	if err := migrate.Run(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	// Clean slate.
	if _, err := pool.Exec(ctx, `TRUNCATE cover, ebook, library_path RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cleanup := func() {
		pool.Exec(ctx, `TRUNCATE cover, ebook, library_path RESTART IDENTITY CASCADE`)
		pool.Close()
	}
	return store.New(pool), cleanup
}

// seedLibraryAndEbooks inserts a library row + two ebooks (one with a cover).
// Returns the library path id and ebook ids for assertions.
func seedLibraryAndEbooks(t *testing.T, s *store.Store) (libID int64, idA, idB string) {
	t.Helper()
	ctx := context.Background()
	err := s.Pool().QueryRow(ctx, `
		INSERT INTO library_path (path) VALUES ('/tmp/lib') RETURNING id
	`).Scan(&libID)
	if err != nil {
		t.Fatalf("seed library: %v", err)
	}
	idA = "ebookA"
	idB = "ebookB"
	for _, row := range []struct {
		id, title, author, genre, series, format string
	}{
		{idA, "Atlas Shrugged", "Ayn Rand, Editor X", "Philosophy, Fiction", "Foundation", "epub"},
		{idB, "Foundation", "Isaac Asimov", "Sci-Fi", "Foundation", "pdf"},
	} {
		_, err := s.Pool().Exec(ctx, `
			INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime,
			                   title, author, genre, series)
			VALUES ($1,$2,$3,$4,1024, now(), $5, $6, $7, $8)
		`, row.id, libID, "/tmp/lib/"+row.id+"."+row.format, row.format,
			row.title, row.author, row.genre, row.series)
		if err != nil {
			t.Fatalf("seed ebook %s: %v", row.id, err)
		}
	}
	// Cover for idA only.
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO cover (ebook_id, content_type, bytes, source)
		VALUES ($1, 'image/jpeg', $2, 'embedded')
	`, idA, []byte{0xff, 0xd8, 0xff})
	if err != nil {
		t.Fatalf("seed cover: %v", err)
	}
	return libID, idA, idB
}

func TestListEbooks_ReturnsBothBooks(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	seedLibraryAndEbooks(t, s)

	out, err := s.ListEbooks(context.Background(), store.ListParams{})
	if err != nil {
		t.Fatalf("ListEbooks: %v", err)
	}
	if out.Total != 2 || len(out.Items) != 2 {
		t.Fatalf("got %+v", out)
	}
	// Items are ordered by title — Atlas before Foundation.
	if out.Items[0].Title != "Atlas Shrugged" {
		t.Errorf("first item = %q; want Atlas Shrugged", out.Items[0].Title)
	}
	// Authors must be split from CSV.
	if len(out.Items[0].Authors) != 2 {
		t.Errorf("authors split failed: %+v", out.Items[0].Authors)
	}
	if !out.Items[0].HasCover {
		t.Errorf("expected HasCover=true for seeded cover")
	}
	if out.Items[1].HasCover {
		t.Errorf("expected HasCover=false for second book")
	}
}

// TestListEbooks_ExcludesDisabledLibrary guards the leak: a disabled
// library's books must not appear in an unfiltered catalog pull (the portal
// hides disabled libraries; their books must be hidden too), and filtering
// by a disabled library id must return nothing.
func TestListEbooks_ExcludesDisabledLibrary(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	seedLibraryAndEbooks(t, s) // 2 books in an enabled library

	ctx := context.Background()
	var disabledID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO library_path (path, enabled) VALUES ('/tmp/disabled', FALSE) RETURNING id
	`).Scan(&disabledID); err != nil {
		t.Fatalf("seed disabled library: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime, title, author)
		VALUES ('hidden', $1, '/tmp/disabled/h.epub', 'epub', 1, now(), 'Hidden Book', 'Nobody')
	`, disabledID); err != nil {
		t.Fatalf("seed hidden ebook: %v", err)
	}

	out, err := s.ListEbooks(ctx, store.ListParams{})
	if err != nil {
		t.Fatalf("ListEbooks: %v", err)
	}
	if out.Total != 2 || len(out.Items) != 2 {
		t.Fatalf("disabled-library book leaked: total=%d items=%d", out.Total, len(out.Items))
	}
	for _, it := range out.Items {
		if it.Title == "Hidden Book" {
			t.Fatal("Hidden Book (disabled library) must not appear in the catalog")
		}
	}
	f, err := s.ListEbooks(ctx, store.ListParams{LibraryID: disabledID})
	if err != nil {
		t.Fatalf("ListEbooks(libraryID): %v", err)
	}
	if f.Total != 0 {
		t.Fatalf("filtering by a disabled library must return 0, got %d", f.Total)
	}
}

func TestListEbooks_SearchFiltersByTitle(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	seedLibraryAndEbooks(t, s)

	out, err := s.ListEbooks(context.Background(), store.ListParams{Search: "atlas"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].Title != "Atlas Shrugged" {
		t.Errorf("got %+v", out)
	}
}

func TestGetEbookByID_HappyAndNotFound(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	_, idA, _ := seedLibraryAndEbooks(t, s)

	d, err := s.GetEbookByID(context.Background(), idA)
	if err != nil {
		t.Fatalf("GetEbookByID: %v", err)
	}
	if d.Title != "Atlas Shrugged" || len(d.Genres) != 2 {
		t.Errorf("got %+v", d)
	}

	if _, err := s.GetEbookByID(context.Background(), "nope"); err != store.ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetCover_HappyAndNotFound(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	_, idA, idB := seedLibraryAndEbooks(t, s)

	bytes, ct, err := s.GetCover(context.Background(), idA)
	if err != nil {
		t.Fatalf("GetCover: %v", err)
	}
	if len(bytes) != 3 || ct != "image/jpeg" {
		t.Errorf("got bytes=%d ct=%q", len(bytes), ct)
	}

	if _, _, err := s.GetCover(context.Background(), idB); err != store.ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestGetEbookPath(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	_, idA, _ := seedLibraryAndEbooks(t, s)
	p, fmt, err := s.GetEbookPath(context.Background(), idA)
	if err != nil {
		t.Fatal(err)
	}
	if fmt != "epub" || p != "/tmp/lib/ebookA.epub" {
		t.Errorf("got path=%q format=%q", p, fmt)
	}
}

func TestListAuthors_AggregatesAcrossCSV(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	seedLibraryAndEbooks(t, s)

	out, err := s.ListAuthors(context.Background(), store.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	// 3 distinct authors: Ayn Rand, Editor X, Isaac Asimov.
	if out.Total != 3 {
		t.Errorf("got total=%d items=%+v", out.Total, out.Items)
	}
}

func TestListSeries_AndGenres(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	seedLibraryAndEbooks(t, s)

	sx, err := s.ListSeries(context.Background(), store.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	// Both books share "Foundation" series — single row with count=2.
	if sx.Total != 1 || sx.Items[0].Name != "Foundation" || sx.Items[0].Count != 2 {
		t.Errorf("series: %+v", sx)
	}

	gx, err := s.ListGenres(context.Background(), store.ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	// Genres across CSV: Philosophy, Fiction, Sci-Fi = 3 distinct.
	if gx.Total != 3 {
		t.Errorf("genres total=%d items=%+v", gx.Total, gx.Items)
	}
}
