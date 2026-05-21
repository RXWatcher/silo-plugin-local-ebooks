package store

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAppConfigDefaults(t *testing.T) {
	cfg := (AppConfig{}).WithDefaults()
	if !reflect.DeepEqual(cfg, DefaultAppConfig()) {
		t.Fatalf("WithDefaults() = %+v, want %+v", cfg, DefaultAppConfig())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config did not validate: %v", err)
	}
	cfg.MetadataScanSource = "not-a-source"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted an invalid scan source")
	}
}

func TestAppConfigSeedAndLegacyImport(t *testing.T) {
	dsn := os.Getenv("EBOOKSDB_TEST_DSN")
	if dsn == "" {
		t.Skip("EBOOKSDB_TEST_DSN unset; skipping integration app config test")
	}
	ctx := context.Background()
	if err := migrate.Run(ctx, dsn); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	st := New(pool)
	cfg, err := st.GetAppConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, DefaultAppConfig()) {
		t.Fatalf("seeded config = %+v, want defaults", cfg)
	}

	legacy := DefaultAppConfig()
	legacy.MetadataDefaultRegion = "gb"
	legacy.GoogleBooksAPIKey = "legacy-key"
	imported, err := st.ImportLegacyAppConfig(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !imported {
		t.Fatal("legacy config was not imported into default row")
	}
	cfg, err = st.GetAppConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetadataDefaultRegion != "gb" || cfg.GoogleBooksAPIKey != "legacy-key" {
		t.Fatalf("imported config = %+v", cfg)
	}

	other := DefaultAppConfig()
	other.MetadataDefaultRegion = "de"
	imported, err = st.ImportLegacyAppConfig(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if imported {
		t.Fatal("legacy import overwrote plugin-managed config")
	}
	cfg, err = st.GetAppConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetadataDefaultRegion != "gb" {
		t.Fatalf("config was overwritten: %+v", cfg)
	}
}
