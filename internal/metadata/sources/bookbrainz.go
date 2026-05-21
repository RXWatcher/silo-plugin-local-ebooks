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

const bookbrainzID = "bookbrainz"
const bookbrainzBaseURL = "https://api.bookbrainz.org/1"

// bbUUIDRE matches a canonical BookBrainz BBID (lowercase or uppercase hex UUID).
// BookBrainz BBIDs are standard UUIDs; non-UUID Get inputs are ignored upstream.
var bbUUIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// bbYearRE pulls a 4-digit year out of a release date string ("2021-05-04" -> "2021").
var bbYearRE = regexp.MustCompile(`(\d{4})`)

// BookBrainz is the Source impl for the BookBrainz JSON API.
// It targets the /edition endpoint, which carries ISBN, publisher, release
// date, and language alongside the title/author credit. The plan referenced
// type=work, but booklore-ng (the reference impl) uses type=edition; the
// edition record is where the rich metadata lives.
type BookBrainz struct {
	http *HTTPClient
}

// NewBookBrainz constructs a production BookBrainz source.
func NewBookBrainz(ua string) *BookBrainz {
	return NewBookBrainzAt(bookbrainzBaseURL, ua)
}

// NewBookBrainzAt constructs a BookBrainz source with a custom base URL (for tests).
func NewBookBrainzAt(baseURL, ua string) *BookBrainz {
	return &BookBrainz{http: NewHTTPClient(baseURL, ua)}
}

func (b *BookBrainz) ID() string                       { return bookbrainzID }
func (b *BookBrainz) Enabled(cfg map[string]bool) bool { return cfg[bookbrainzID] }

// Get fetches a single BookBrainz edition by BBID (UUID).
// Returns (nil, nil) when id is empty or not a UUID.
// Returns (nil, ErrNotFound) on 404 or when the entity lacks a title.
func (b *BookBrainz) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	if !bbUUIDRE.MatchString(id) {
		return nil, nil
	}

	u := fmt.Sprintf("%s/edition/%s", b.http.BaseURL, url.PathEscape(id))
	body, err := b.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var entity bbEntity
	if err := UnmarshalInto(body, &entity); err != nil {
		return nil, err
	}
	c := bbToCandidate(entity, region, body)
	if c.Title == "" {
		return nil, ErrNotFound
	}
	return &c, nil
}

// Search runs a BookBrainz edition search by free text.
// Returns (nil, nil) on empty query or empty result set.
func (b *BookBrainz) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	u := fmt.Sprintf("%s/search?q=%s&type=edition&limit=10", b.http.BaseURL, url.QueryEscape(q))
	body, err := b.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp bbSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Results) == 0 {
		return nil, nil
	}

	raw := json.RawMessage(body)
	out := make([]metadata.Candidate, 0, len(resp.Results))
	for _, e := range resp.Results {
		c := bbToCandidate(e, region, raw)
		if c.Title == "" {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// --- JSON types ---

type bbSearchResponse struct {
	Count   int        `json:"count"`
	Offset  int        `json:"offset"`
	Results []bbEntity `json:"results"`
}

type bbEntity struct {
	BBID            string             `json:"bbid"`
	Type            string             `json:"type"`
	DefaultAlias    *bbAlias           `json:"defaultAlias"`
	Disambiguation  string             `json:"disambiguation"`
	Annotation      *bbAnnotation      `json:"annotation"`
	IdentifierSet   *bbIdentifierSet   `json:"identifierSet"`
	AuthorCredit    *bbAuthorCredit    `json:"authorCredit"`
	PublisherSet    *bbPublisherSet    `json:"publisherSet"`
	ReleaseEventSet *bbReleaseEventSet `json:"releaseEventSet"`
}

type bbAlias struct {
	Name     string     `json:"name"`
	SortName string     `json:"sortName"`
	Language *bbLangRef `json:"language"`
}

type bbLangRef struct {
	Name string `json:"name"`
}

type bbAnnotation struct {
	Content string `json:"content"`
}

type bbIdentifierSet struct {
	Identifiers []bbIdentifier `json:"identifiers"`
}

type bbIdentifier struct {
	Type  bbIdentifierType `json:"type"`
	Value string           `json:"value"`
}

type bbIdentifierType struct {
	Label string `json:"label"`
}

type bbAuthorCredit struct {
	Names []bbAuthorCreditName `json:"names"`
}

type bbAuthorCreditName struct {
	Author bbAuthorRef `json:"author"`
}

type bbAuthorRef struct {
	DefaultAlias *bbAlias `json:"defaultAlias"`
}

type bbPublisherSet struct {
	Publishers []bbPublisher `json:"publishers"`
}

type bbPublisher struct {
	DefaultAlias *bbAlias `json:"defaultAlias"`
}

type bbReleaseEventSet struct {
	ReleaseEvents []bbReleaseEvent `json:"releaseEvents"`
}

type bbReleaseEvent struct {
	Date string `json:"date"`
}

// bbToCandidate flattens a BookBrainz edition entity to a Candidate.
// Returns a zero-Title candidate if the entity has no defaultAlias.name;
// callers must check Title before using.
func bbToCandidate(e bbEntity, region string, raw []byte) metadata.Candidate {
	title := ""
	language := ""
	if e.DefaultAlias != nil {
		title = e.DefaultAlias.Name
		if e.DefaultAlias.Language != nil {
			language = e.DefaultAlias.Language.Name
		}
	}

	authors := []string{}
	if e.AuthorCredit != nil {
		for _, n := range e.AuthorCredit.Names {
			if n.Author.DefaultAlias != nil && n.Author.DefaultAlias.Name != "" {
				authors = append(authors, n.Author.DefaultAlias.Name)
			}
		}
	}

	publisher := ""
	if e.PublisherSet != nil && len(e.PublisherSet.Publishers) > 0 {
		if a := e.PublisherSet.Publishers[0].DefaultAlias; a != nil {
			publisher = a.Name
		}
	}

	published := ""
	if e.ReleaseEventSet != nil && len(e.ReleaseEventSet.ReleaseEvents) > 0 {
		if m := bbYearRE.FindStringSubmatch(e.ReleaseEventSet.ReleaseEvents[0].Date); len(m) > 1 {
			published = m[1]
		}
	}

	// Prefer ISBN-13; fall back to ISBN-10. Case-insensitive label match
	// mirrors booklore-ng's behaviour.
	var isbn13, isbn10 string
	if e.IdentifierSet != nil {
		for _, id := range e.IdentifierSet.Identifiers {
			label := strings.ToLower(id.Type.Label)
			switch {
			case strings.Contains(label, "isbn-13"):
				if isbn13 == "" {
					isbn13 = id.Value
				}
			case strings.Contains(label, "isbn-10"):
				if isbn10 == "" {
					isbn10 = id.Value
				}
			}
		}
	}
	isbn := isbn13
	if isbn == "" {
		isbn = isbn10
	}

	description := ""
	if e.Annotation != nil {
		description = e.Annotation.Content
	}
	if description == "" {
		description = e.Disambiguation
	}

	return metadata.Candidate{
		Source:      bookbrainzID,
		ExternalID:  e.BBID,
		Title:       title,
		Authors:     authors,
		Description: description,
		ISBN:        isbn,
		PublishedAt: published,
		Publisher:   publisher,
		Language:    language,
		Region:      region,
		Raw:         raw,
	}
}
