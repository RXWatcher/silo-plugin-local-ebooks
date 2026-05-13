// Package metadata holds the ebook metadata aggregator: source-agnostic
// Candidate/Match types, the external-ID format, the confidence formula,
// the cache + queue stores, and the parallel aggregator.
package metadata

import "encoding/json"

// Candidate is the source-agnostic representation of a single metadata
// record. Each Source returns these; the aggregator wraps them in Match.
type Candidate struct {
	Source      string          `json:"source"`
	ExternalID  string          `json:"external_id"`
	Title       string          `json:"title"`
	Authors     []string        `json:"authors,omitempty"`
	Description string          `json:"description,omitempty"`
	ASIN        string          `json:"asin,omitempty"`
	ISBN        string          `json:"isbn,omitempty"`
	CoverURL    string          `json:"cover_url,omitempty"`
	PublishedAt string          `json:"published_at,omitempty"`
	Publisher   string          `json:"publisher,omitempty"`
	Language    string          `json:"language,omitempty"`
	Genres      []string        `json:"genres,omitempty"`
	PageCount   int             `json:"page_count,omitempty"`
	Series      string          `json:"series,omitempty"`
	SeriesPos   string          `json:"series_pos,omitempty"`
	Region      string          `json:"region,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Match wraps a Candidate with its confidence score.
type Match struct {
	Source     string
	Confidence int
	Candidate  Candidate
}
