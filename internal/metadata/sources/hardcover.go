package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"unicode"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const hardcoverID = "hardcover"
const hardcoverBaseURL = "https://api.hardcover.app/v1/graphql"

// Hardcover is the Source impl for the Hardcover GraphQL API.
type Hardcover struct {
	http   *HTTPClient
	apiKey string
}

// NewHardcover constructs a production Hardcover source.
func NewHardcover(apiKey, ua string) *Hardcover {
	return NewHardcoverAt(hardcoverBaseURL, apiKey, ua)
}

// NewHardcoverAt constructs a Hardcover source with a custom base URL (for tests).
func NewHardcoverAt(baseURL, apiKey, ua string) *Hardcover {
	return &Hardcover{
		http:   NewHTTPClient(baseURL, ua),
		apiKey: apiKey,
	}
}

func (h *Hardcover) ID() string { return hardcoverID }

// Enabled returns true only when the source is toggled on AND an API key is present.
func (h *Hardcover) Enabled(cfg map[string]bool) bool {
	return cfg[hardcoverID] && h.apiKey != ""
}

// gqlQuery sends a GraphQL POST request and returns the raw response body.
func (h *Hardcover) gqlQuery(ctx context.Context, query string, vars map[string]any) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.http.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.apiKey)
	if h.http.UA != "" {
		req.Header.Set("User-Agent", h.http.UA)
	}
	resp, err := h.http.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, SoftLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("source: POST %s status %d", h.http.BaseURL, resp.StatusCode)
	}
	return body, nil
}

// isNumeric returns true if s consists entirely of ASCII digits.
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

const hardcoverBookFields = `
  id
  title
  description
  release_date
  pages
  contributions { author { name } }
  editions { isbn_13 isbn_10 }
  image { url }
  slug
`

// Get fetches a single book by Hardcover numeric book ID.
// Returns (nil, nil) for non-numeric IDs.
func (h *Hardcover) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	if !isNumeric(id) {
		return nil, nil
	}
	bookID, err := strconv.Atoi(id)
	if err != nil {
		return nil, nil
	}

	const q = `query GetBook($id: Int!) { books_by_pk(id: $id) {` + hardcoverBookFields + `} }`
	body, err := h.gqlQuery(ctx, q, map[string]any{"id": bookID})
	if err != nil {
		return nil, err
	}

	var resp hardcoverGetResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	if resp.Data.BookByPK == nil {
		return nil, ErrNotFound
	}
	c := resp.Data.BookByPK.toCandidate(region, body)
	return &c, nil
}

// Search queries Hardcover for books matching query by title.
func (h *Hardcover) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	if query == "" {
		return nil, nil
	}

	const q = `query SearchBooks($q: String!) { books(where: {title: {_ilike: $q}}, limit: 20) {` + hardcoverBookFields + `} }`
	body, err := h.gqlQuery(ctx, q, map[string]any{"q": "%" + query + "%"})
	if err != nil {
		return nil, err
	}

	var resp hardcoverSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}

	out := make([]metadata.Candidate, 0, len(resp.Data.Books))
	for i, b := range resp.Data.Books {
		rawOne, _ := json.Marshal(resp.Data.Books[i])
		out = append(out, b.toCandidate(region, rawOne))
	}
	return out, nil
}

// --- JSON types ---

type hardcoverGetResponse struct {
	Data struct {
		BookByPK *hardcoverBook `json:"books_by_pk"`
	} `json:"data"`
}

type hardcoverSearchResponse struct {
	Data struct {
		Books []hardcoverBook `json:"books"`
	} `json:"data"`
}

type hardcoverBook struct {
	ID            int                     `json:"id"`
	Title         string                  `json:"title"`
	Description   string                  `json:"description"`
	ReleaseDate   string                  `json:"release_date"`
	Pages         int                     `json:"pages"`
	Contributions []hardcoverContribution `json:"contributions"`
	Editions      []hardcoverEdition      `json:"editions"`
	Image         *hardcoverImage         `json:"image"`
	Slug          string                  `json:"slug"`
}

type hardcoverContribution struct {
	Author struct {
		Name string `json:"name"`
	} `json:"author"`
}

type hardcoverEdition struct {
	ISBN13 string `json:"isbn_13"`
	ISBN10 string `json:"isbn_10"`
}

type hardcoverImage struct {
	URL string `json:"url"`
}

func (b hardcoverBook) toCandidate(region string, raw []byte) metadata.Candidate {
	authors := make([]string, 0, len(b.Contributions))
	for _, c := range b.Contributions {
		if c.Author.Name != "" {
			authors = append(authors, c.Author.Name)
		}
	}

	// First non-empty ISBN-13, fall back to ISBN-10.
	var isbn string
	for _, ed := range b.Editions {
		if ed.ISBN13 != "" {
			isbn = ed.ISBN13
			break
		}
	}
	if isbn == "" {
		for _, ed := range b.Editions {
			if ed.ISBN10 != "" {
				isbn = ed.ISBN10
				break
			}
		}
	}

	var coverURL string
	if b.Image != nil {
		coverURL = b.Image.URL
	}

	return metadata.Candidate{
		Source:      hardcoverID,
		ExternalID:  strconv.Itoa(b.ID),
		Title:       b.Title,
		Authors:     authors,
		Description: b.Description,
		ISBN:        isbn,
		CoverURL:    coverURL,
		PublishedAt: b.ReleaseDate,
		PageCount:   b.Pages,
		Region:      region,
		Raw:         raw,
	}
}
