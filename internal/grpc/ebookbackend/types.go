// Package ebookbackend implements the ebook_backend.v1 contract HTTP surface
// backed by the local Postgres ebook store. Response shapes follow the shared
// ebook backend catalog contract so downstream consumers can treat compatible
// providers as interchangeable.
package ebookbackend

// Book is the summary entry returned by /catalog list endpoints.
type Book struct {
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
	CoverURL    string   `json:"cover_url,omitempty"`
	HasCover    bool     `json:"has_cover"`
	Formats     []string `json:"formats"`
}

// Library is one configured catalog root exposed to portal clients.
type Library struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path,omitempty"`
	MediaType     string `json:"media_type"`
	Enabled       bool   `json:"enabled"`
	LastScannedAt string `json:"last_scanned_at,omitempty"`
}

// BookDetail extends Book with rich descriptive metadata + downloadable files.
type BookDetail struct {
	Book
	Description string   `json:"description,omitempty"`
	ISBN        string   `json:"isbn,omitempty"`
	ASIN        string   `json:"asin,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	PageCount   int      `json:"page_count,omitempty"`
	Files       []File   `json:"files"`
}

// File describes a downloadable rendition of a book.
type File struct {
	Format    string `json:"format"`
	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type"`
	URL       string `json:"url,omitempty"`
}

// Author is a browse-endpoint row.
type Author struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Series is a browse-endpoint row.
type Series struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Genre is a browse-endpoint row.
type Genre struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Page is the paginated wrapper for all list responses.
type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
}
