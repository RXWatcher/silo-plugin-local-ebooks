package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const googleBooksID = "googlebooks"
const googleBooksBaseURL = "https://www.googleapis.com/books/v1"

// gbVolumeIDRE matches Google Books volume IDs: 12 base64url characters.
// Google Books IDs are alphanumeric plus '_' and '-', always 12 chars.
var gbVolumeIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{12}$`)

// GoogleBooks is the Source impl for the Google Books API.
type GoogleBooks struct {
	http   *HTTPClient
	apiKey string
}

// NewGoogleBooks constructs a production GoogleBooks source.
func NewGoogleBooks(apiKey, ua string) *GoogleBooks {
	return NewGoogleBooksAt(googleBooksBaseURL, apiKey, ua)
}

// NewGoogleBooksAt constructs a GoogleBooks source with a custom base URL (for tests).
func NewGoogleBooksAt(baseURL, apiKey, ua string) *GoogleBooks {
	return &GoogleBooks{
		http:   NewHTTPClient(baseURL, ua),
		apiKey: apiKey,
	}
}

func (g *GoogleBooks) ID() string { return googleBooksID }

// Enabled returns true only when the source is toggled on AND an API key is present.
func (g *GoogleBooks) Enabled(cfg map[string]bool) bool {
	return cfg[googleBooksID] && g.apiKey != ""
}

// Get fetches a single volume by its Google Books volume ID.
// Returns (nil, nil) for IDs that don't match the Google Books ID shape.
func (g *GoogleBooks) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	// Only proceed if id looks like a Google Books volume ID.
	if !gbVolumeIDRE.MatchString(id) {
		return nil, nil
	}

	u := fmt.Sprintf("%s/volumes/%s?key=%s", g.http.BaseURL, url.PathEscape(id), url.QueryEscape(g.apiKey))
	body, err := g.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var vol gbVolume
	if err := UnmarshalInto(body, &vol); err != nil {
		return nil, err
	}
	c := vol.toCandidate(region, body)
	return &c, nil
}

// Search queries Google Books for the given query string.
// If the query looks like an ISBN, it is prefixed with "isbn:" per the TS reference.
func (g *GoogleBooks) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	bare := strings.ReplaceAll(q, "-", "")
	if isbnRE.MatchString(bare) {
		q = "isbn:" + bare
	}

	u := fmt.Sprintf("%s/volumes?q=%s&maxResults=20&key=%s",
		g.http.BaseURL,
		url.QueryEscape(q),
		url.QueryEscape(g.apiKey),
	)
	body, err := g.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp gbSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}

	out := make([]metadata.Candidate, 0, len(resp.Items))
	for _, item := range resp.Items {
		rawOne, _ := json.Marshal(item)
		out = append(out, item.toCandidate(region, rawOne))
	}
	return out, nil
}

// --- JSON types ---

type gbSearchResponse struct {
	TotalItems int        `json:"totalItems"`
	Items      []gbVolume `json:"items"`
}

type gbVolume struct {
	ID         string       `json:"id"`
	VolumeInfo gbVolumeInfo `json:"volumeInfo"`
}

type gbVolumeInfo struct {
	Title               string                 `json:"title"`
	Authors             []string               `json:"authors"`
	Publisher           string                 `json:"publisher"`
	PublishedDate       string                 `json:"publishedDate"`
	Description         string                 `json:"description"`
	PageCount           int                    `json:"pageCount"`
	Categories          []string               `json:"categories"`
	Language            string                 `json:"language"`
	ImageLinks          *gbImageLinks          `json:"imageLinks"`
	IndustryIdentifiers []gbIndustryIdentifier `json:"industryIdentifiers"`
}

type gbImageLinks struct {
	SmallThumbnail string `json:"smallThumbnail"`
	Thumbnail      string `json:"thumbnail"`
	Small          string `json:"small"`
	Medium         string `json:"medium"`
	Large          string `json:"large"`
	ExtraLarge     string `json:"extraLarge"`
}

type gbIndustryIdentifier struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
}

func (v gbVolume) toCandidate(region string, raw []byte) metadata.Candidate {
	info := v.VolumeInfo

	// Prefer ISBN_13, fall back to ISBN_10.
	isbn := ""
	for _, ii := range info.IndustryIdentifiers {
		if ii.Type == "ISBN_13" {
			isbn = ii.Identifier
			break
		}
	}
	if isbn == "" {
		for _, ii := range info.IndustryIdentifiers {
			if ii.Type == "ISBN_10" {
				isbn = ii.Identifier
				break
			}
		}
	}

	// Prefer largest available cover image, replace http with https.
	coverURL := ""
	if info.ImageLinks != nil {
		for _, u := range []string{
			info.ImageLinks.ExtraLarge,
			info.ImageLinks.Large,
			info.ImageLinks.Medium,
			info.ImageLinks.Small,
			info.ImageLinks.Thumbnail,
			info.ImageLinks.SmallThumbnail,
		} {
			if u != "" {
				coverURL = strings.ReplaceAll(u, "http://", "https://")
				break
			}
		}
	}

	return metadata.Candidate{
		Source:      googleBooksID,
		ExternalID:  v.ID,
		Title:       info.Title,
		Authors:     info.Authors,
		Description: info.Description,
		Publisher:   info.Publisher,
		PublishedAt: info.PublishedDate,
		Language:    info.Language,
		Genres:      info.Categories,
		ISBN:        isbn,
		CoverURL:    coverURL,
		PageCount:   info.PageCount,
		Region:      region,
		Raw:         raw,
	}
}
