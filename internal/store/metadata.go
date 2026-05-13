package store

import (
	"context"

	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/metadata"
)

// LoadEbookRow reads the enrichment-relevant subset of an ebook row.
func (s *Store) LoadEbookRow(ctx context.Context, id string) (metadata.EbookRow, error) {
	var r metadata.EbookRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, format, title, author, publisher, year, language, genre,
		       isbn, asin, description, page_count, series, series_pos
		FROM ebook WHERE id = $1
	`, id).Scan(&r.ID, &r.Format, &r.Title, &r.Author, &r.Publisher, &r.Year,
		&r.Language, &r.Genre, &r.ISBN, &r.ASIN, &r.Description, &r.PageCount,
		&r.Series, &r.SeriesPos)
	return r, err
}

// UpdateEbookMetadata writes back the enrichment-relevant fields. Format is
// intentionally not in the column list — it's set at scan time only.
func (s *Store) UpdateEbookMetadata(ctx context.Context, row metadata.EbookRow) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE ebook SET
		  title=$2, author=$3, publisher=$4, year=$5, language=$6, genre=$7,
		  isbn=$8, asin=$9, description=$10, page_count=$11, series=$12, series_pos=$13,
		  updated_at=now()
		WHERE id=$1
	`, row.ID, row.Title, row.Author, row.Publisher, row.Year, row.Language, row.Genre,
		row.ISBN, row.ASIN, row.Description, row.PageCount, row.Series, row.SeriesPos)
	return err
}

// BulkEnqueueBackfill enqueues all non-deleted ebooks for metadata enrichment.
// Existing job rows are left untouched (ON CONFLICT DO NOTHING).
func (s *Store) BulkEnqueueBackfill(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO metadata_enrichment_job (ebook_id)
		SELECT id FROM ebook WHERE deleted = FALSE
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
