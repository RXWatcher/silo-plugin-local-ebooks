package store_test

import (
	"context"
	"testing"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

// Regression: the by-id getters must not serve content from a disabled
// library (or a soft-deleted ebook). Disabling a library is the mechanism to
// take content offline; a client that knows/guesses an id must not bypass it.
func TestByIDGetters_ExcludeDisabledLibrary(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	ctx := context.Background()

	var disID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO library_path (path, enabled) VALUES ('/tmp/dis', FALSE) RETURNING id
	`).Scan(&disID); err != nil {
		t.Fatalf("seed disabled lib: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime, title, author)
		VALUES ('hid', $1, '/tmp/dis/h.epub', 'epub', 1, now(), 'Hidden', 'N')
	`, disID); err != nil {
		t.Fatalf("seed ebook: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO cover (ebook_id, content_type, bytes, source) VALUES ('hid', 'image/jpeg', '\xffd8ff', 'embedded')
	`); err != nil {
		t.Fatalf("seed cover: %v", err)
	}

	if _, err := s.GetEbookByID(ctx, "hid"); err != store.ErrNotFound {
		t.Errorf("GetEbookByID(disabled-lib) = %v, want ErrNotFound", err)
	}
	if _, _, err := s.GetCover(ctx, "hid"); err != store.ErrNotFound {
		t.Errorf("GetCover(disabled-lib) = %v, want ErrNotFound", err)
	}
	if _, _, err := s.GetEbookPath(ctx, "hid"); err != store.ErrNotFound {
		t.Errorf("GetEbookPath(disabled-lib) = %v, want ErrNotFound", err)
	}

	// Soft-deleted ebook in an enabled library is likewise not served.
	var enID int64
	_ = s.Pool().QueryRow(ctx, `INSERT INTO library_path (path) VALUES ('/tmp/en') RETURNING id`).Scan(&enID)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime, title, author, deleted)
		VALUES ('del', $1, '/tmp/en/d.epub', 'epub', 1, now(), 'Del', 'N', TRUE)
	`, enID); err != nil {
		t.Fatalf("seed deleted ebook: %v", err)
	}
	if _, err := s.GetEbookByID(ctx, "del"); err != store.ErrNotFound {
		t.Errorf("GetEbookByID(deleted) = %v, want ErrNotFound", err)
	}
	if _, _, err := s.GetEbookPath(ctx, "del"); err != store.ErrNotFound {
		t.Errorf("GetEbookPath(deleted) = %v, want ErrNotFound", err)
	}
}

// Regression: BulkEnqueueBackfill must re-arm a previously-FAILED job (the
// old ON CONFLICT DO NOTHING silently skipped every exhausted book).
func TestBulkEnqueueBackfill_ReArmsFailed(t *testing.T) {
	s, cleanup := newCatalogPool(t)
	defer cleanup()
	ctx := context.Background()

	var lib int64
	_ = s.Pool().QueryRow(ctx, `INSERT INTO library_path (path) VALUES ('/tmp/l') RETURNING id`).Scan(&lib)
	for _, id := range []string{"failed1", "done1", "pending1"} {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime, title, author)
			VALUES ($1, $2, '/tmp/l/'||$1, 'epub', 1, now(), $1, 'A')
		`, id, lib); err != nil {
			t.Fatalf("seed ebook %s: %v", id, err)
		}
	}
	_, _ = s.Pool().Exec(ctx, `INSERT INTO metadata_enrichment_job (ebook_id, status, attempts, last_error) VALUES ('failed1','failed',5,'boom')`)
	_, _ = s.Pool().Exec(ctx, `INSERT INTO metadata_enrichment_job (ebook_id, status) VALUES ('done1','completed')`)
	_, _ = s.Pool().Exec(ctx, `INSERT INTO metadata_enrichment_job (ebook_id, status) VALUES ('pending1','pending')`)

	if _, err := s.BulkEnqueueBackfill(ctx); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	get := func(id string) (status string, attempts int, lastErr string) {
		_ = s.Pool().QueryRow(ctx,
			`SELECT status, attempts, last_error FROM metadata_enrichment_job WHERE ebook_id=$1`, id).
			Scan(&status, &attempts, &lastErr)
		return
	}
	if st, att, le := get("failed1"); st != "pending" || att != 0 || le != "" {
		t.Errorf("failed job not re-armed: status=%q attempts=%d last_error=%q", st, att, le)
	}
	if st, _, _ := get("done1"); st != "completed" {
		t.Errorf("completed job disturbed: status=%q", st)
	}
	if st, _, _ := get("pending1"); st != "pending" {
		t.Errorf("pending job disturbed: status=%q", st)
	}
}
