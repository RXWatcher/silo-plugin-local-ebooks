// Package store is the data-access layer over Postgres.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/ebookparse"
)

// ErrDuplicatePath is returned when an insert would violate the unique path
// constraint on library_path.
var ErrDuplicatePath = errors.New("duplicate path")

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Store wraps a pgxpool. Construct one per process; safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for callers that need transactions.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping is a health check.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// UpsertEbook inserts or updates an ebook row. Returns wasKnown=true if the
// (library_path_id, path) pair was already present.
func (s *Store) UpsertEbook(ctx context.Context, libraryPathID int64, ebookID, path, format string,
	fileSize int64, mtime time.Time, p ebookparse.Parsed) (wasKnown bool, err error) {
	row := s.pool.QueryRow(ctx, `
        INSERT INTO ebook (id, library_path_id, path, format, file_size, mtime,
                           title, author, publisher, year, language, genre, isbn, asin,
                           description, page_count, series, series_pos, scanned_at, updated_at)
        VALUES ($1,$2,$3,$4,$5,$6,
                $7,$8,$9,$10,$11,$12,$13,$14,
                $15,$16,$17,$18, now(), now())
        ON CONFLICT (library_path_id, path) DO UPDATE
          SET id=EXCLUDED.id, mtime=EXCLUDED.mtime, file_size=EXCLUDED.file_size,
              format=EXCLUDED.format,
              title=CASE WHEN ebook.title='' THEN EXCLUDED.title ELSE ebook.title END,
              author=CASE WHEN ebook.author='' THEN EXCLUDED.author ELSE ebook.author END,
              publisher=CASE WHEN ebook.publisher='' THEN EXCLUDED.publisher ELSE ebook.publisher END,
              year=CASE WHEN ebook.year='' THEN EXCLUDED.year ELSE ebook.year END,
              language=CASE WHEN ebook.language='' THEN EXCLUDED.language ELSE ebook.language END,
              genre=CASE WHEN ebook.genre='' THEN EXCLUDED.genre ELSE ebook.genre END,
              isbn=CASE WHEN ebook.isbn='' THEN EXCLUDED.isbn ELSE ebook.isbn END,
              asin=CASE WHEN ebook.asin='' THEN EXCLUDED.asin ELSE ebook.asin END,
              description=CASE WHEN ebook.description='' THEN EXCLUDED.description ELSE ebook.description END,
              page_count=CASE WHEN ebook.page_count=0 THEN EXCLUDED.page_count ELSE ebook.page_count END,
              series=CASE WHEN ebook.series='' THEN EXCLUDED.series ELSE ebook.series END,
              series_pos=CASE WHEN ebook.series_pos='' THEN EXCLUDED.series_pos ELSE ebook.series_pos END,
              deleted=FALSE, deleted_at=NULL,
              scanned_at=now(), updated_at=now()
        RETURNING (xmax = 0) AS inserted
    `,
		ebookID, libraryPathID, path, format, fileSize, mtime,
		p.Title, strings.Join(p.Authors, ", "), p.Publisher, yearOf(p.PublishedAt),
		p.Language, strings.Join(p.Genres, ", "), p.ISBN, p.ASIN,
		p.Description, p.PageCount, p.Series, p.SeriesPos,
	)
	var inserted bool
	if err := row.Scan(&inserted); err != nil {
		return false, err
	}
	return !inserted, nil
}

func (s *Store) UpsertCover(ctx context.Context, ebookID, contentType, source string, bytes []byte) error {
	_, err := s.pool.Exec(ctx, `
        INSERT INTO cover (ebook_id, content_type, bytes, source)
        VALUES ($1, $2, $3, $4)
        ON CONFLICT (ebook_id) DO UPDATE
          SET content_type=EXCLUDED.content_type, bytes=EXCLUDED.bytes, source=EXCLUDED.source
    `, ebookID, contentType, bytes, source)
	return err
}

func (s *Store) ListPaths(ctx context.Context, libraryPathID int64) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT id, path FROM ebook WHERE library_path_id = $1 AND deleted = FALSE
    `, libraryPathID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		out[id] = path
	}
	return out, rows.Err()
}

func (s *Store) SoftDelete(ctx context.Context, ebookID string) error {
	_, err := s.pool.Exec(ctx, `
        UPDATE ebook SET deleted=TRUE, deleted_at=now(), updated_at=now()
        WHERE id=$1
    `, ebookID)
	return err
}

func yearOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%04d", t.Year())
}
