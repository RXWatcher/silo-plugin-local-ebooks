package migrate

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrate0001Applies(t *testing.T) {
	dsn := os.Getenv("EBOOKSDB_TEST_DSN")
	if dsn == "" {
		t.Skip("EBOOKSDB_TEST_DSN unset; skipping integration migration test")
	}
	ctx := context.Background()
	if err := Run(ctx, dsn); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var n int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
        WHERE table_schema = 'public'
          AND table_name IN ('library_path','ebook','cover','scan_event','metadata_cache','metadata_enrichment_job','app_config')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("expected 7 tables, found %d", n)
	}
}
