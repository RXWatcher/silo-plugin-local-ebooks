package metadata

import "strings"

// EbookRow is the subset of ebook columns ApplyMatch reads/writes.
type EbookRow struct {
	ID          string
	Format      string
	Title       string
	Author      string // comma-joined
	Publisher   string
	Year        string
	Language    string
	Genre       string // comma-joined
	ISBN        string
	ASIN        string
	Description string
	PageCount   int
	Series      string
	SeriesPos   string
}

// ApplyMatch returns a new EbookRow with `candidate`'s fields overwriting
// `row`'s, preserving non-empty existing fields when the candidate is empty,
// and preserving the existing ID and Format always. Format is set at scan
// time and never overwritten by external metadata.
func ApplyMatch(row EbookRow, candidate Candidate) EbookRow {
	out := row
	if candidate.Title != "" {
		out.Title = candidate.Title
	}
	if a := strings.Join(candidate.Authors, ", "); a != "" {
		out.Author = a
	}
	if candidate.Publisher != "" {
		out.Publisher = candidate.Publisher
	}
	if y := yearOf(candidate.PublishedAt); y != "" {
		out.Year = y
	}
	if candidate.Language != "" {
		out.Language = candidate.Language
	}
	if g := strings.Join(candidate.Genres, ", "); g != "" {
		out.Genre = g
	}
	if candidate.ISBN != "" {
		out.ISBN = candidate.ISBN
	}
	if candidate.ASIN != "" {
		out.ASIN = candidate.ASIN
	}
	if candidate.Description != "" {
		out.Description = candidate.Description
	}
	if candidate.PageCount > 0 {
		out.PageCount = candidate.PageCount
	}
	if candidate.Series != "" {
		out.Series = candidate.Series
	}
	if candidate.SeriesPos != "" {
		out.SeriesPos = candidate.SeriesPos
	}
	// Format intentionally preserved — set at scan, never overwritten by external metadata
	return out
}

func yearOf(s string) string {
	if len(s) >= 4 {
		return s[:4]
	}
	return ""
}
