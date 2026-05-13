package store

import (
	"context"
	"time"
)

// LibraryPath is a configured root.
type LibraryPath struct {
	ID            int64
	Path          string
	Enabled       bool
	LastScannedAt *time.Time
}

// UpsertLibraryPath inserts the path if missing (returning the new id) or
// returns the existing id. Disabled rows are re-enabled on upsert.
func (s *Store) UpsertLibraryPath(ctx context.Context, path string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO library_path (path, enabled)
		VALUES ($1, TRUE)
		ON CONFLICT (path) DO UPDATE SET enabled = TRUE
		RETURNING id
	`, path).Scan(&id)
	return id, err
}

// ListLibraryPaths returns all configured roots.
func (s *Store) ListLibraryPaths(ctx context.Context) ([]LibraryPath, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, path, enabled, last_scanned_at
		FROM library_path
		ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LibraryPath
	for rows.Next() {
		var lp LibraryPath
		if err := rows.Scan(&lp.ID, &lp.Path, &lp.Enabled, &lp.LastScannedAt); err != nil {
			return nil, err
		}
		out = append(out, lp)
	}
	return out, rows.Err()
}

// MarkLibraryScanned updates last_scanned_at to now().
func (s *Store) MarkLibraryScanned(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE library_path SET last_scanned_at = now() WHERE id = $1`, id)
	return err
}

// InsertScanEvent creates a started scan_event row; library_path_id is
// optional. Returns the new row id.
func (s *Store) InsertScanEvent(ctx context.Context, libraryPathID *int64) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO scan_event (library_path_id) VALUES ($1) RETURNING id
	`, libraryPathID).Scan(&id)
	return id, err
}

// FinishScanEvent closes a scan_event row with its result tallies and any
// error text.
func (s *Store) FinishScanEvent(ctx context.Context, id int64, added, changed, deleted int, errText string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE scan_event
		   SET finished_at = now(),
		       books_added = $2,
		       books_changed = $3,
		       books_deleted = $4,
		       error_text = NULLIF($5, '')
		 WHERE id = $1
	`, id, added, changed, deleted, errText)
	return err
}
