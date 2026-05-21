package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const gutenbergID = "gutenberg"
const gutenbergBaseURL = "https://gutendex.com"

// gutNumericRE matches a positive integer Gutenberg book ID.
var gutNumericRE = regexp.MustCompile(`^\d+$`)

// Gutenberg is the Source impl for the Gutendex JSON API over Project Gutenberg.
type Gutenberg struct {
	http *HTTPClient
}

// NewGutenberg constructs a production Gutenberg source.
func NewGutenberg(ua string) *Gutenberg {
	return NewGutenbergAt(gutenbergBaseURL, ua)
}

// NewGutenbergAt constructs a Gutenberg source with a custom base URL (for tests).
func NewGutenbergAt(baseURL, ua string) *Gutenberg {
	return &Gutenberg{http: NewHTTPClient(baseURL, ua)}
}

func (g *Gutenberg) ID() string                       { return gutenbergID }
func (g *Gutenberg) Enabled(cfg map[string]bool) bool { return cfg[gutenbergID] }

// Get fetches a single Gutenberg book by its numeric ID.
// Returns (nil, nil) when id is empty or non-numeric.
func (g *Gutenberg) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	if !gutNumericRE.MatchString(id) {
		return nil, nil
	}

	u := fmt.Sprintf("%s/books/%s", g.http.BaseURL, url.PathEscape(id))
	body, err := g.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var book gutenbergBook
	if err := UnmarshalInto(body, &book); err != nil {
		return nil, err
	}
	c := book.toCandidate(region, body)
	return &c, nil
}

// Search runs a Gutendex text search.
func (g *Gutenberg) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	u := fmt.Sprintf("%s/books?search=%s", g.http.BaseURL, url.QueryEscape(q))
	body, err := g.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp gutenbergSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}

	raw := json.RawMessage(body)
	out := make([]metadata.Candidate, 0, len(resp.Results))
	for _, b := range resp.Results {
		out = append(out, b.toCandidate(region, raw))
	}
	return out, nil
}

// --- JSON types ---

type gutenbergSearchResponse struct {
	Count   int             `json:"count"`
	Results []gutenbergBook `json:"results"`
}

type gutenbergBook struct {
	ID        int               `json:"id"`
	Title     string            `json:"title"`
	Authors   []gutenbergAuthor `json:"authors"`
	Subjects  []string          `json:"subjects"`
	Languages []string          `json:"languages"`
	Formats   map[string]string `json:"formats"`
}

type gutenbergAuthor struct {
	Name string `json:"name"`
}

func (b gutenbergBook) toCandidate(region string, raw []byte) metadata.Candidate {
	authors := make([]string, 0, len(b.Authors))
	for _, a := range b.Authors {
		if a.Name != "" {
			authors = append(authors, a.Name)
		}
	}

	// Mirror booklore: cap subjects at 5 (the array can be very large).
	genres := b.Subjects
	if len(genres) > 5 {
		genres = genres[:5]
	}

	lang := ""
	if len(b.Languages) > 0 {
		lang = b.Languages[0]
	}

	cover := b.Formats["image/jpeg"]
	if cover == "" {
		cover = b.Formats["image/png"]
	}

	return metadata.Candidate{
		Source:     gutenbergID,
		ExternalID: strconv.Itoa(b.ID),
		Title:      b.Title,
		Authors:    authors,
		Language:   lang,
		Genres:     genres,
		CoverURL:   cover,
		Region:     region,
		Raw:        raw,
	}
}
