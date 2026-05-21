package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const isbndbID = "isbndb"
const isbndbBaseURL = "https://api2.isbndb.com"

// ISBNdb is the Source impl for the ISBNdb API.
type ISBNdb struct {
	http   *HTTPClient
	apiKey string
}

// NewISBNdb constructs a production ISBNdb source.
func NewISBNdb(apiKey, ua string) *ISBNdb {
	return NewISBNdbAt(isbndbBaseURL, apiKey, ua)
}

// NewISBNdbAt constructs an ISBNdb source with a custom base URL (for tests).
func NewISBNdbAt(baseURL, apiKey, ua string) *ISBNdb {
	return &ISBNdb{
		http:   NewHTTPClient(baseURL, ua),
		apiKey: apiKey,
	}
}

func (i *ISBNdb) ID() string { return isbndbID }

// Enabled returns true only when the source is toggled on AND an API key is present.
func (i *ISBNdb) Enabled(cfg map[string]bool) bool {
	return cfg[isbndbID] && i.apiKey != ""
}

// authHeaders returns the Authorization header required by ISBNdb.
// ISBNdb uses a bare API key (no "Bearer" prefix).
func (i *ISBNdb) authHeaders() map[string]string {
	return map[string]string{"Authorization": i.apiKey}
}

// Get fetches a single book by ISBN. Returns (nil, nil) for non-ISBN IDs.
func (i *ISBNdb) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	bare := strings.ReplaceAll(id, "-", "")
	if !isbnRE.MatchString(bare) {
		return nil, nil
	}

	u := fmt.Sprintf("%s/book/%s", i.http.BaseURL, url.PathEscape(bare))
	body, err := i.http.GetJSONWithHeaders(ctx, u, i.authHeaders())
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var resp isbndbBookResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	if resp.Book == nil {
		return nil, ErrNotFound
	}
	c := resp.Book.toCandidate(region, body)
	return &c, nil
}

// Search queries ISBNdb for the given query string.
func (i *ISBNdb) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	u := fmt.Sprintf("%s/books/%s?pageSize=20", i.http.BaseURL, url.PathEscape(q))
	body, err := i.http.GetJSONWithHeaders(ctx, u, i.authHeaders())
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp isbndbSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}

	out := make([]metadata.Candidate, 0, len(resp.Books))
	for _, b := range resp.Books {
		rawOne, _ := json.Marshal(b)
		out = append(out, b.toCandidate(region, rawOne))
	}
	return out, nil
}

// --- JSON types ---

type isbndbBookResponse struct {
	Book *isbndbBook `json:"book"`
}

type isbndbSearchResponse struct {
	Total int          `json:"total"`
	Books []isbndbBook `json:"books"`
}

type isbndbBook struct {
	Title         string   `json:"title"`
	ISBN          string   `json:"isbn"`
	ISBN13        string   `json:"isbn13"`
	Publisher     string   `json:"publisher"`
	Language      string   `json:"language"`
	DatePublished string   `json:"date_published"`
	Synopsis      string   `json:"synopsis"`
	Overview      string   `json:"overview"`
	Image         string   `json:"image"`
	Authors       []string `json:"authors"`
	Subjects      []string `json:"subjects"`
	Pages         int      `json:"pages"`
}

func (b isbndbBook) toCandidate(region string, raw []byte) metadata.Candidate {
	// Prefer ISBN-13; fall back to ISBN field.
	isbn := b.ISBN13
	if isbn == "" {
		isbn = b.ISBN
	}
	// Use isbn13 (or isbn) as the external ID.
	externalID := isbn

	desc := b.Synopsis
	if desc == "" {
		desc = b.Overview
	}

	return metadata.Candidate{
		Source:      isbndbID,
		ExternalID:  externalID,
		Title:       b.Title,
		Authors:     b.Authors,
		Description: desc,
		Publisher:   b.Publisher,
		PublishedAt: b.DatePublished,
		Language:    b.Language,
		Genres:      b.Subjects,
		ISBN:        isbn,
		CoverURL:    b.Image,
		PageCount:   b.Pages,
		Region:      region,
		Raw:         raw,
	}
}
