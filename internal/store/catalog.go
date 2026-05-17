package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Paged is a generic paginated result.
type Paged[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
}

// ListParams controls list/browse pagination + search.
type ListParams struct {
	Page      int
	Limit     int
	Search    string
	LibraryID int64
	// Library, when non-empty, restricts results to the library path with the
	// given filesystem path. Empty matches all.
	Library string
}

// Ebook is a summary row for catalog listing.
type Ebook struct {
	ID          string   `json:"id"`
	LibraryID   int64    `json:"library_id,omitempty"`
	LibraryName string   `json:"library_name,omitempty"`
	MediaType   string   `json:"media_type,omitempty"`
	Title       string   `json:"title"`
	Authors     []string `json:"authors,omitempty"`
	Series      string   `json:"series,omitempty"`
	SeriesIndex string   `json:"series_index,omitempty"`
	Year        string   `json:"year,omitempty"`
	Language    string   `json:"language,omitempty"`
	HasCover    bool     `json:"has_cover"`
	Format      string   `json:"format,omitempty"`
}

// EbookDetail is the full record returned by GetEbookByID.
type EbookDetail struct {
	Ebook
	Description string   `json:"description,omitempty"`
	ISBN        string   `json:"isbn,omitempty"`
	ASIN        string   `json:"asin,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	PageCount   int      `json:"page_count,omitempty"`
	FileSize    int64    `json:"file_size,omitempty"`
	Path        string   `json:"-"`
}

// Author / Series / Genre are aggregate rows for browse endpoints.
type Author struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Series struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Genre struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// CatalogStats summarizes the local catalog for admin diagnostics.
type CatalogStats struct {
	Total       int            `json:"total"`
	Active      int            `json:"active"`
	Deleted     int            `json:"deleted"`
	WithCovers  int            `json:"with_covers"`
	ByFormat    map[string]int `json:"by_format"`
	ByMediaType map[string]int `json:"by_media_type"`
	ByLibrary   map[string]int `json:"by_library"`
}

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// normalizeLimit clamps a requested limit to [1, 200] with default 50.
func normalizeLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 200 {
		return 200
	}
	return n
}

func normalizePage(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// splitCSV splits a comma- or semicolon-separated field into trimmed tokens,
// dropping empties.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	// Accept comma OR semicolon as separators; some scanners use ; for authors.
	rep := strings.ReplaceAll(s, ";", ",")
	parts := strings.Split(rep, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ListEbooks returns a paginated list of non-deleted ebooks, optionally
// filtered by search (matches title/author ILIKE) and library path.
func (s *Store) ListEbooks(ctx context.Context, p ListParams) (Paged[Ebook], error) {
	limit := normalizeLimit(p.Limit)
	page := normalizePage(p.Page)
	offset := (page - 1) * limit

	where := []string{"e.deleted = FALSE"}
	args := []any{}
	if p.Search != "" {
		args = append(args, "%"+p.Search+"%")
		where = append(where, fmt.Sprintf("(e.title ILIKE $%d OR e.author ILIKE $%d)", len(args), len(args)))
	}
	if p.Library != "" {
		args = append(args, p.Library)
		where = append(where, fmt.Sprintf("lp.path = $%d", len(args)))
	}
	if p.LibraryID > 0 {
		args = append(args, p.LibraryID)
		where = append(where, fmt.Sprintf("lp.id = $%d", len(args)))
	}

	whereClause := strings.Join(where, " AND ")

	// Total count.
	var total int
	countSQL := `SELECT count(*) FROM ebook e JOIN library_path lp ON lp.id = e.library_path_id WHERE ` + whereClause
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return Paged[Ebook]{}, fmt.Errorf("count: %w", err)
	}

	// Page query.
	args = append(args, limit, offset)
	rowsSQL := `
		SELECT e.id, lp.id, lp.name, lp.media_type,
		       e.title, e.author, e.series, e.series_pos, e.year, e.language, e.format,
		       EXISTS(SELECT 1 FROM cover c WHERE c.ebook_id = e.id) AS has_cover
		FROM ebook e
		JOIN library_path lp ON lp.id = e.library_path_id
		WHERE ` + whereClause + `
		ORDER BY e.title ASC, e.id ASC
		LIMIT $` + fmt.Sprintf("%d", len(args)-1) + ` OFFSET $` + fmt.Sprintf("%d", len(args))
	rows, err := s.pool.Query(ctx, rowsSQL, args...)
	if err != nil {
		return Paged[Ebook]{}, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	items := []Ebook{}
	for rows.Next() {
		var (
			b              Ebook
			authorCSV      string
			series, pos    string
			year, language string
			format         string
			hasCover       bool
		)
		if err := rows.Scan(
			&b.ID, &b.LibraryID, &b.LibraryName, &b.MediaType,
			&b.Title, &authorCSV, &series, &pos, &year, &language, &format, &hasCover,
		); err != nil {
			return Paged[Ebook]{}, fmt.Errorf("scan: %w", err)
		}
		b.Authors = splitCSV(authorCSV)
		b.Series = series
		b.SeriesIndex = pos
		b.Year = year
		b.Language = language
		b.Format = format
		b.HasCover = hasCover
		items = append(items, b)
	}
	if err := rows.Err(); err != nil {
		return Paged[Ebook]{}, err
	}

	return Paged[Ebook]{Items: items, Total: total, Page: page, Limit: limit}, nil
}

// GetEbookByID fetches a single ebook by id. Returns ErrNotFound if missing.
func (s *Store) GetEbookByID(ctx context.Context, id string) (EbookDetail, error) {
	var (
		d         EbookDetail
		authorCSV string
		genreCSV  string
		hasCover  bool
	)
	err := s.pool.QueryRow(ctx, `
		SELECT e.id, lp.id, lp.name, lp.media_type,
		       e.title, e.author, e.series, e.series_pos, e.year, e.language, e.format,
		       e.description, e.isbn, e.asin, e.publisher, e.genre, e.page_count, e.file_size, e.path,
		       EXISTS(SELECT 1 FROM cover c WHERE c.ebook_id = e.id) AS has_cover
		FROM ebook e
		JOIN library_path lp ON lp.id = e.library_path_id
		WHERE e.id = $1 AND e.deleted = FALSE
	`, id).Scan(
		&d.ID, &d.LibraryID, &d.LibraryName, &d.MediaType,
		&d.Title, &authorCSV, &d.Series, &d.SeriesIndex, &d.Year, &d.Language, &d.Format,
		&d.Description, &d.ISBN, &d.ASIN, &d.Publisher, &genreCSV, &d.PageCount, &d.FileSize, &d.Path,
		&hasCover,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return EbookDetail{}, ErrNotFound
	}
	if err != nil {
		return EbookDetail{}, err
	}
	d.Authors = splitCSV(authorCSV)
	d.Genres = splitCSV(genreCSV)
	d.HasCover = hasCover
	return d, nil
}

// GetCover returns the raw cover bytes + content-type for an ebook.
// Returns ErrNotFound if the cover (or ebook) is missing.
func (s *Store) GetCover(ctx context.Context, id string) ([]byte, string, error) {
	var (
		bytes       []byte
		contentType string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT bytes, content_type FROM cover WHERE ebook_id = $1
	`, id).Scan(&bytes, &contentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return bytes, contentType, nil
}

// GetEbookPath returns the on-disk path + format for streaming the book file.
// Returns ErrNotFound if missing.
func (s *Store) GetEbookPath(ctx context.Context, id string) (string, string, error) {
	var path, format string
	err := s.pool.QueryRow(ctx, `
		SELECT e.path, e.format FROM ebook e
		WHERE e.id = $1 AND e.deleted = FALSE
	`, id).Scan(&path, &format)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	return path, format, nil
}

// CatalogStats returns small aggregate counters for admin status screens.
func (s *Store) CatalogStats(ctx context.Context) (CatalogStats, error) {
	stats := CatalogStats{
		ByFormat:    map[string]int{},
		ByMediaType: map[string]int{},
		ByLibrary:   map[string]int{},
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE deleted = FALSE)::int,
		       count(*) FILTER (WHERE deleted = TRUE)::int,
		       count(c.ebook_id)::int
		  FROM ebook e
		  LEFT JOIN cover c ON c.ebook_id = e.id
	`).Scan(&stats.Total, &stats.Active, &stats.Deleted, &stats.WithCovers); err != nil {
		return stats, err
	}
	formatRows, err := s.pool.Query(ctx, `
		SELECT format, count(*)::int
		  FROM ebook
		 WHERE deleted = FALSE
		 GROUP BY format
		 ORDER BY format
	`)
	if err != nil {
		return stats, err
	}
	defer formatRows.Close()
	for formatRows.Next() {
		var key string
		var count int
		if err := formatRows.Scan(&key, &count); err != nil {
			return stats, err
		}
		stats.ByFormat[key] = count
	}
	if err := formatRows.Err(); err != nil {
		return stats, err
	}
	mediaRows, err := s.pool.Query(ctx, `
		SELECT lp.media_type, count(*)::int
		  FROM ebook e
		  JOIN library_path lp ON lp.id = e.library_path_id
		 WHERE e.deleted = FALSE
		 GROUP BY lp.media_type
		 ORDER BY lp.media_type
	`)
	if err != nil {
		return stats, err
	}
	defer mediaRows.Close()
	for mediaRows.Next() {
		var key string
		var count int
		if err := mediaRows.Scan(&key, &count); err != nil {
			return stats, err
		}
		stats.ByMediaType[key] = count
	}
	if err := mediaRows.Err(); err != nil {
		return stats, err
	}
	libraryRows, err := s.pool.Query(ctx, `
		SELECT lp.name, count(e.id)::int
		  FROM library_path lp
		  LEFT JOIN ebook e ON e.library_path_id = lp.id AND e.deleted = FALSE
		 GROUP BY lp.id, lp.name
		 ORDER BY lp.name
	`)
	if err != nil {
		return stats, err
	}
	defer libraryRows.Close()
	for libraryRows.Next() {
		var key string
		var count int
		if err := libraryRows.Scan(&key, &count); err != nil {
			return stats, err
		}
		stats.ByLibrary[key] = count
	}
	return stats, libraryRows.Err()
}

// splitColumnSQL produces a SELECT that splits a CSV column on , and ;,
// trims whitespace, and groups by the resulting name producing (name, count).
// Empty / whitespace-only names are filtered out.
func splitColumnSQL(column string, p ListParams) (string, []any) {
	args, libraryWhere := libraryAggregateWhere(p)
	// nested SELECT with unnest, then aggregate.
	return `
		SELECT name, count(*)::int AS count FROM (
		  SELECT trim(t) AS name
		  FROM ebook e
		  JOIN library_path lp ON lp.id = e.library_path_id,
		       LATERAL unnest(string_to_array(replace(e.` + column + `, ';', ','), ',')) AS t
		  WHERE e.deleted = FALSE AND e.` + column + ` <> ''` + libraryWhere + `
		) sub
		WHERE name <> ''
		GROUP BY name
	`, args
}

// ListAuthors enumerates distinct authors (split from the CSV `author` column)
// with their book counts. Pagination is post-aggregation.
func (s *Store) ListAuthors(ctx context.Context, p ListParams) (Paged[Author], error) {
	limit := normalizeLimit(p.Limit)
	page := normalizePage(p.Page)
	inner, args := splitColumnSQL("author", p)
	return paginateAggregate[Author](ctx, s,
		inner, args, p.Search, page, limit,
		func(name string, count int) Author { return Author{Name: name, Count: count} },
	)
}

// ListSeries enumerates distinct series with their book counts. The `series`
// column is a single value per row (not CSV), so no splitting required.
func (s *Store) ListSeries(ctx context.Context, p ListParams) (Paged[Series], error) {
	limit := normalizeLimit(p.Limit)
	page := normalizePage(p.Page)
	args, libraryWhere := libraryAggregateWhere(p)
	return paginateAggregate[Series](ctx, s,
		`SELECT e.series AS name, count(*)::int AS count
		 FROM ebook e
		 JOIN library_path lp ON lp.id = e.library_path_id
		 WHERE e.deleted = FALSE AND e.series <> ''`+libraryWhere+`
		 GROUP BY e.series`,
		args, p.Search, page, limit,
		func(name string, count int) Series { return Series{Name: name, Count: count} },
	)
}

// ListGenres enumerates distinct genres (split from CSV `genre` column).
func (s *Store) ListGenres(ctx context.Context, p ListParams) (Paged[Genre], error) {
	limit := normalizeLimit(p.Limit)
	page := normalizePage(p.Page)
	inner, args := splitColumnSQL("genre", p)
	return paginateAggregate[Genre](ctx, s,
		inner, args, p.Search, page, limit,
		func(name string, count int) Genre { return Genre{Name: name, Count: count} },
	)
}

func libraryAggregateWhere(p ListParams) ([]any, string) {
	args := []any{}
	where := ""
	if p.Library != "" {
		args = append(args, p.Library)
		where += fmt.Sprintf(" AND lp.path = $%d", len(args))
	}
	if p.LibraryID > 0 {
		args = append(args, p.LibraryID)
		where += fmt.Sprintf(" AND lp.id = $%d", len(args))
	}
	return args, where
}

// paginateAggregate wraps an aggregation subquery (yielding columns name,
// count) with optional name-filter + pagination + total count, projecting
// each row via mk.
func paginateAggregate[T any](
	ctx context.Context, s *Store, innerSQL string, baseArgs []any, search string,
	page, limit int, mk func(name string, count int) T,
) (Paged[T], error) {
	offset := (page - 1) * limit
	args := append([]any{}, baseArgs...)
	where := ""
	if search != "" {
		args = append(args, "%"+search+"%")
		where = fmt.Sprintf(" WHERE name ILIKE $%d", len(args))
	}

	countSQL := `SELECT count(*) FROM (` + innerSQL + `) agg` + where
	var total int
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return Paged[T]{}, fmt.Errorf("count: %w", err)
	}

	args = append(args, limit, offset)
	pageSQL := `SELECT name, count FROM (` + innerSQL + `) agg` + where +
		` ORDER BY name ASC LIMIT $` + fmt.Sprintf("%d", len(args)-1) +
		` OFFSET $` + fmt.Sprintf("%d", len(args))

	rows, err := s.pool.Query(ctx, pageSQL, args...)
	if err != nil {
		return Paged[T]{}, fmt.Errorf("page: %w", err)
	}
	defer rows.Close()

	items := []T{}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return Paged[T]{}, err
		}
		items = append(items, mk(name, count))
	}
	if err := rows.Err(); err != nil {
		return Paged[T]{}, err
	}
	return Paged[T]{Items: items, Total: total, Page: page, Limit: limit}, nil
}
