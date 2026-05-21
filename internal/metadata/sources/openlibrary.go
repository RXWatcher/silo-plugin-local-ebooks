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

const openLibraryID = "openlibrary"
const openLibraryBaseURL = "https://openlibrary.org"
const openLibraryCoversURL = "https://covers.openlibrary.org"

// isbnRE recognizes ISBN-10 and ISBN-13 (digits only, hyphens stripped before match).
var isbnRE = regexp.MustCompile(`^(?:978\d|979\d)\d{9}$|^\d{9}[\dX]$`)

// OpenLibrary is the Source impl for openlibrary.org.
type OpenLibrary struct {
	http       *HTTPClient
	coversBase string
}

func NewOpenLibrary(ua string) *OpenLibrary {
	return NewOpenLibraryAt(openLibraryBaseURL, openLibraryCoversURL, ua)
}

func NewOpenLibraryAt(baseURL, coversURL, ua string) *OpenLibrary {
	return &OpenLibrary{http: NewHTTPClient(baseURL, ua), coversBase: coversURL}
}

func (o *OpenLibrary) ID() string                       { return openLibraryID }
func (o *OpenLibrary) Enabled(cfg map[string]bool) bool { return cfg[openLibraryID] }

// Get looks up by ISBN-10/13 or "OLxxxxxxM" work key.
func (o *OpenLibrary) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}

	var path string
	switch {
	case isbnRE.MatchString(strings.ReplaceAll(id, "-", "")):
		path = fmt.Sprintf("/isbn/%s.json", url.PathEscape(strings.ReplaceAll(id, "-", "")))
	case strings.HasPrefix(id, "OL") && strings.HasSuffix(id, "M"):
		path = fmt.Sprintf("/books/%s.json", url.PathEscape(id))
	default:
		return nil, nil
	}

	body, err := o.http.GetJSON(ctx, o.http.BaseURL+path)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var book openLibraryEdition
	if err := UnmarshalInto(body, &book); err != nil {
		return nil, err
	}
	c := book.toCandidate(o.coversBase, region, body)
	return &c, nil
}

// Search runs ISBN lookup (if query is ISBN-shaped) or text search.
func (o *OpenLibrary) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	bare := strings.ReplaceAll(q, "-", "")
	if isbnRE.MatchString(bare) {
		c, err := o.Get(ctx, bare, region)
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, nil
		}
		return []metadata.Candidate{*c}, nil
	}
	u := fmt.Sprintf("%s/search.json?q=%s&limit=20", o.http.BaseURL, url.QueryEscape(q))
	body, err := o.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var resp openLibrarySearchResp
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	out := make([]metadata.Candidate, 0, len(resp.Docs))
	for _, d := range resp.Docs {
		rawOne, _ := json.Marshal(d)
		out = append(out, d.toCandidate(o.coversBase, region, rawOne))
	}
	return out, nil
}

type openLibraryEdition struct {
	Key         string                 `json:"key"`
	Title       string                 `json:"title"`
	Subtitle    string                 `json:"subtitle"`
	AuthorsRefs []openLibraryAuthorRef `json:"authors"`
	Authors     []string               `json:"author_name"`
	Description any                    `json:"description"`
	Publishers  []string               `json:"publishers"`
	PublishDate string                 `json:"publish_date"`
	Languages   []openLibraryLangRef   `json:"languages"`
	Subjects    []string               `json:"subjects"`
	ISBN10      []string               `json:"isbn_10"`
	ISBN13      []string               `json:"isbn_13"`
	NumPages    int                    `json:"number_of_pages"`
	Covers      []int                  `json:"covers"`
}

type openLibraryAuthorRef struct {
	Key string `json:"key"`
}
type openLibraryLangRef struct {
	Key string `json:"key"`
}

type openLibrarySearchResp struct {
	Docs []openLibrarySearchDoc `json:"docs"`
}

type openLibrarySearchDoc struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	AuthorName   []string `json:"author_name"`
	FirstPublish int      `json:"first_publish_year"`
	ISBN         []string `json:"isbn"`
	Language     []string `json:"language"`
	Subject      []string `json:"subject"`
	CoverID      int      `json:"cover_i"`
	NumPages     int      `json:"number_of_pages_median"`
	Publisher    []string `json:"publisher"`
}

func (e openLibraryEdition) toCandidate(coversBase, region string, raw []byte) metadata.Candidate {
	desc := ""
	switch d := e.Description.(type) {
	case string:
		desc = d
	case map[string]any:
		if v, ok := d["value"].(string); ok {
			desc = v
		}
	}
	isbn := ""
	if len(e.ISBN13) > 0 {
		isbn = e.ISBN13[0]
	} else if len(e.ISBN10) > 0 {
		isbn = e.ISBN10[0]
	}
	cover := ""
	if len(e.Covers) > 0 {
		cover = fmt.Sprintf("%s/b/id/%d-L.jpg", coversBase, e.Covers[0])
	}
	lang := ""
	if len(e.Languages) > 0 {
		lang = strings.TrimPrefix(e.Languages[0].Key, "/languages/")
	}
	publisher := ""
	if len(e.Publishers) > 0 {
		publisher = e.Publishers[0]
	}
	return metadata.Candidate{
		Source:      openLibraryID,
		ExternalID:  strings.TrimPrefix(e.Key, "/books/"),
		Title:       e.Title,
		Authors:     e.Authors,
		Description: desc,
		ISBN:        isbn,
		CoverURL:    cover,
		PublishedAt: e.PublishDate,
		Publisher:   publisher,
		Language:    lang,
		Genres:      e.Subjects,
		PageCount:   e.NumPages,
		Region:      region,
		Raw:         raw,
	}
}

func (d openLibrarySearchDoc) toCandidate(coversBase, region string, raw []byte) metadata.Candidate {
	isbn := ""
	if len(d.ISBN) > 0 {
		isbn = d.ISBN[0]
	}
	cover := ""
	if d.CoverID > 0 {
		cover = fmt.Sprintf("%s/b/id/%d-L.jpg", coversBase, d.CoverID)
	}
	publisher := ""
	if len(d.Publisher) > 0 {
		publisher = d.Publisher[0]
	}
	lang := ""
	if len(d.Language) > 0 {
		lang = d.Language[0]
	}
	pub := ""
	if d.FirstPublish > 0 {
		pub = fmt.Sprintf("%d", d.FirstPublish)
	}
	return metadata.Candidate{
		Source:      openLibraryID,
		ExternalID:  strings.TrimPrefix(d.Key, "/works/"),
		Title:       d.Title,
		Authors:     d.AuthorName,
		ISBN:        isbn,
		CoverURL:    cover,
		PublishedAt: pub,
		Publisher:   publisher,
		Language:    lang,
		Genres:      d.Subject,
		PageCount:   d.NumPages,
		Region:      region,
		Raw:         raw,
	}
}
