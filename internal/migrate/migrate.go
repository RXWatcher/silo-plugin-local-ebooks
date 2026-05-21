// Package migrate runs the plugin's SQL migrations against the configured
// Postgres database. Migrations are embedded at build time; the runner is
// idempotent (uses a single bookkeeping table to track applied versions).
package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/migrate/files"
)

const bookkeepingDDL = `
CREATE TABLE IF NOT EXISTS schema_migration (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Run applies all .up.sql files in lexical order that haven't been recorded
// in the schema_migration table. Safe to call repeatedly.
func Run(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, bookkeepingDDL); err != nil {
		return fmt.Errorf("bookkeeping: %w", err)
	}

	entries, err := fs.ReadDir(files.FS, ".")
	if err != nil {
		return fmt.Errorf("read embed: %w", err)
	}
	var ups []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			ups = append(ups, e.Name())
		}
	}
	sort.Strings(ups)

	for _, name := range ups {
		version := strings.TrimSuffix(name, ".up.sql")
		var exists int
		err := conn.QueryRow(ctx,
			`SELECT 1 FROM schema_migration WHERE version = $1`, version).Scan(&exists)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			// A real query/connection error must not be misread as
			// "not applied" and trigger a re-apply with a confusing failure.
			return fmt.Errorf("check applied %s: %w", version, err)
		}
		body, err := fs.ReadFile(files.FS, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migration(version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}
