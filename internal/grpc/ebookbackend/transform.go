package ebookbackend

import (
	"strings"
	"time"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

// ToBook converts a store.Ebook row into the wire summary shape. The cover URL
// is left blank — the caller (HTTP handler) fills it in with a route-relative
// URL because the store layer doesn't know the request base path.
func ToBook(e store.Ebook) Book {
	formats := []string{}
	if e.Format != "" {
		formats = append(formats, strings.ToLower(e.Format))
	}
	return Book{
		ID:          e.ID,
		LibraryID:   e.LibraryID,
		LibraryName: e.LibraryName,
		MediaType:   e.MediaType,
		Title:       e.Title,
		Authors:     e.Authors,
		Series:      e.Series,
		SeriesIndex: e.SeriesIndex,
		Year:        e.Year,
		Language:    e.Language,
		HasCover:    e.HasCover,
		Formats:     formats,
	}
}

// ToBookDetail converts a store.EbookDetail into the wire detail shape. A
// single File entry is generated for the on-disk format (the schema only
// tracks one format per row today). Files is always non-nil to keep JSON
// output stable for clients.
func ToBookDetail(d store.EbookDetail) BookDetail {
	out := BookDetail{
		Book:        ToBook(d.Ebook),
		Description: d.Description,
		ISBN:        d.ISBN,
		ASIN:        d.ASIN,
		Publisher:   d.Publisher,
		Genres:      d.Genres,
		PageCount:   d.PageCount,
		Files:       []File{},
	}
	if d.Format != "" {
		out.Files = append(out.Files, File{
			Format:    strings.ToLower(d.Format),
			SizeBytes: d.FileSize,
			MimeType:  FormatToMime(d.Format),
		})
	}
	return out
}

// ToAuthor / ToSeries / ToGenre are 1:1 store→wire projections.
func ToAuthor(a store.Author) Author { return Author{Name: a.Name, Count: a.Count} }
func ToSeries(s store.Series) Series { return Series{Name: s.Name, Count: s.Count} }
func ToGenre(g store.Genre) Genre    { return Genre{Name: g.Name, Count: g.Count} }

func ToLibrary(l store.LibraryPath) Library {
	out := Library{
		ID:        l.ID,
		Name:      l.Name,
		Path:      l.Path,
		MediaType: l.MediaType,
		Enabled:   l.Enabled,
	}
	if l.Name == "" {
		out.Name = l.Path
	}
	if out.MediaType == "" {
		out.MediaType = "book"
	}
	if l.LastScannedAt != nil {
		out.LastScannedAt = l.LastScannedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// FormatToMime maps an ebook format to its IANA media type.
func FormatToMime(format string) string {
	switch strings.ToLower(format) {
	case "epub":
		return "application/epub+zip"
	case "pdf":
		return "application/pdf"
	case "mobi":
		return "application/x-mobipocket-ebook"
	case "azw", "azw3":
		return "application/vnd.amazon.ebook"
	case "djvu":
		return "image/vnd.djvu"
	case "fb2":
		return "application/x-fictionbook+xml"
	case "cbz":
		return "application/vnd.comicbook+zip"
	case "cbr":
		return "application/vnd.comicbook-rar"
	case "txt":
		return "text/plain"
	}
	return "application/octet-stream"
}

// ExtForFormat returns the canonical lowercase extension for a format, used
// for Content-Disposition filenames.
func ExtForFormat(format string) string {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return "bin"
	}
	return f
}
