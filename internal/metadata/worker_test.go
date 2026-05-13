package metadata

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeStore struct {
	row     EbookRow
	written EbookRow
	wrote   bool
}

func (f *fakeStore) LoadEbookRow(_ context.Context, _ string) (EbookRow, error) {
	return f.row, nil
}

func (f *fakeStore) UpdateEbookMetadata(_ context.Context, row EbookRow) error {
	f.written = row
	f.wrote = true
	return nil
}

type fakeWorkerSource struct {
	id  string
	out *Candidate
}

func (f *fakeWorkerSource) ID() string                     { return f.id }
func (f *fakeWorkerSource) Enabled(_ map[string]bool) bool { return true }
func (f *fakeWorkerSource) Get(_ context.Context, _, _ string) (*Candidate, error) {
	return f.out, nil
}
func (f *fakeWorkerSource) Search(_ context.Context, _, _ string) ([]Candidate, error) {
	if f.out == nil {
		return nil, nil
	}
	return []Candidate{*f.out}, nil
}

// fakeEnrichmentRegistry satisfies EnrichmentRegistry without importing sources.
type fakeEnrichmentRegistry struct {
	byID map[string]Source
}

func newFakeEnrichmentRegistry(srcs ...Source) *fakeEnrichmentRegistry {
	r := &fakeEnrichmentRegistry{byID: map[string]Source{}}
	for _, s := range srcs {
		r.byID[s.ID()] = s
	}
	return r
}

func (r *fakeEnrichmentRegistry) ForID(id string) Source { return r.byID[id] }

func newWorkerTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EBOOKSDB_TEST_DSN")
	if dsn == "" {
		t.Skip("EBOOKSDB_TEST_DSN unset")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool.Exec(context.Background(),
		`TRUNCATE metadata_enrichment_job, ebook, library_path RESTART IDENTITY CASCADE`)
	pool.Exec(context.Background(), `INSERT INTO library_path (path) VALUES ('/t')`)
	pool.Exec(context.Background(),
		`INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime, isbn)
		 VALUES ('a', 1, '/t/x.epub', 'epub', 0, now(), '9780123456789')`)
	t.Cleanup(func() {
		pool.Exec(context.Background(),
			`TRUNCATE metadata_enrichment_job, ebook, library_path RESTART IDENTITY CASCADE`)
		pool.Close()
	})
	return pool
}

func TestWorker_DrainHappyPath(t *testing.T) {
	pool := newWorkerTestPool(t)
	q := NewQueue(pool)
	q.Enqueue(context.Background(), "a")

	reg := newFakeEnrichmentRegistry(&fakeWorkerSource{id: "src", out: &Candidate{
		Source: "src", Title: "New Title", ISBN: "9780123456789",
	}})

	fs := &fakeStore{row: EbookRow{ID: "a", Format: "epub", ISBN: "9780123456789"}}
	w := NewEnrichmentWorker(q, fs, reg, "src", "us", hclog.NewNullLogger())
	if err := w.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fs.wrote {
		t.Error("expected metadata write")
	}
	if fs.written.Title != "New Title" {
		t.Errorf("title %q", fs.written.Title)
	}
	if fs.written.Format != "epub" {
		t.Errorf("format should be preserved, got %q", fs.written.Format)
	}
	var status string
	pool.QueryRow(context.Background(),
		`SELECT status FROM metadata_enrichment_job WHERE ebook_id='a'`).Scan(&status)
	if status != "completed" {
		t.Errorf("expected completed, got %q", status)
	}
}

func TestWorker_MissingConfiguredSourceErrors(t *testing.T) {
	pool := newWorkerTestPool(t)
	q := NewQueue(pool)
	q.Enqueue(context.Background(), "a")
	fs := &fakeStore{row: EbookRow{ID: "a"}}
	reg := newFakeEnrichmentRegistry() // empty — "doesnotexist" will not be found
	w := NewEnrichmentWorker(q, fs, reg, "doesnotexist", "us", hclog.NewNullLogger())
	w.Drain(context.Background()) //nolint:errcheck
	var status string
	pool.QueryRow(context.Background(),
		`SELECT status FROM metadata_enrichment_job WHERE ebook_id='a'`).Scan(&status)
	// First failure: attempts=1, MaxAttempts=5 → back to pending with backoff.
	if status != "pending" {
		t.Errorf("expected pending, got %q", status)
	}
}
