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

const internetArchiveID = "internetarchive"
const internetArchiveBaseURL = "https://archive.org"

// iaYearRE pulls a 4-digit year out of an Internet Archive `date` value
// (e.g. "1869-01-01" or "c1818"). Mirrors booklore-ng's behaviour at
// internetarchive.ts line 216.
var iaYearRE = regexp.MustCompile(`(\d{4})`)

// iaTagRE strips HTML tags from descriptions. Internet Archive frequently
// returns HTML-formatted descriptions (cf. booklore-ng cleanDescription).
var iaTagRE = regexp.MustCompile(`<[^>]+>`)

// InternetArchive is the Source impl for the archive.org JSON APIs.
// It uses /metadata/<id> for Get and /advancedsearch.php for Search.
// No API key is required.
type InternetArchive struct {
	http *HTTPClient
}

// NewInternetArchive constructs a production Internet Archive source.
func NewInternetArchive(ua string) *InternetArchive {
	return NewInternetArchiveAt(internetArchiveBaseURL, ua)
}

// NewInternetArchiveAt constructs an Internet Archive source with a custom
// base URL (for tests).
func NewInternetArchiveAt(baseURL, ua string) *InternetArchive {
	return &InternetArchive{http: NewHTTPClient(baseURL, ua)}
}

func (i *InternetArchive) ID() string                       { return internetArchiveID }
func (i *InternetArchive) Enabled(cfg map[string]bool) bool { return cfg[internetArchiveID] }

// Get fetches a single archive.org item by its identifier slug
// (e.g. "frankenstein00mary"). Returns (nil, nil) when id is empty.
// Returns (nil, ErrNotFound) on 404.
func (i *InternetArchive) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}

	u := fmt.Sprintf("%s/metadata/%s", i.http.BaseURL, url.PathEscape(id))
	body, err := i.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	var resp iaMetadataResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	// archive.org returns 200 with an empty JSON object ({}) for unknown ids;
	// only the .metadata sub-object carries the fields we need.
	if len(resp.Metadata) == 0 {
		return nil, ErrNotFound
	}

	var item iaItem
	if err := UnmarshalInto(resp.Metadata, &item); err != nil {
		return nil, err
	}
	// The /metadata endpoint omits identifier inside .metadata in some
	// responses; the caller always knows it, so backfill it here.
	if item.Identifier == "" {
		item.Identifier = id
	}

	c := iaToCandidate(item, i.http.BaseURL, region, body)
	return &c, nil
}

// Search runs an advanced-search query restricted to mediatype:texts.
// Returns (nil, nil) on empty query or empty result set.
func (i *InternetArchive) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	// archive.org's advancedsearch uses a Lucene-ish query syntax; we
	// AND mediatype:texts onto the user query. The fl[] params are
	// repeated query params, which url.Values handles natively.
	v := url.Values{}
	v.Set("q", q+" AND mediatype:texts")
	v["fl[]"] = []string{
		"identifier", "title", "creator", "publisher",
		"date", "language", "description", "isbn", "imagecount",
	}
	v.Set("output", "json")
	v.Set("rows", "10")
	u := fmt.Sprintf("%s/advancedsearch.php?%s", i.http.BaseURL, v.Encode())

	body, err := i.http.GetJSON(ctx, u)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var resp iaSearchResponse
	if err := UnmarshalInto(body, &resp); err != nil {
		return nil, err
	}
	if len(resp.Response.Docs) == 0 {
		return nil, nil
	}

	raw := json.RawMessage(body)
	out := make([]metadata.Candidate, 0, len(resp.Response.Docs))
	for _, d := range resp.Response.Docs {
		out = append(out, iaToCandidate(d, i.http.BaseURL, region, raw))
	}
	return out, nil
}

// --- JSON types ---

// iaSearchResponse is the advancedsearch.php envelope.
type iaSearchResponse struct {
	Response struct {
		NumFound int      `json:"numFound"`
		Docs     []iaItem `json:"docs"`
	} `json:"response"`
}

// iaMetadataResponse is the /metadata/<id> envelope. Only .metadata is used.
type iaMetadataResponse struct {
	Metadata json.RawMessage `json:"metadata"`
}

// iaItem mirrors the search-doc / metadata-sub-object shape. Several fields
// may be either a JSON string or an array of strings, depending on whether
// the item has one or many values; we accept both via json.RawMessage and
// unpack them in iaStrings / iaFirstString.
type iaItem struct {
	Identifier  string          `json:"identifier"`
	Title       string          `json:"title"`
	Creator     json.RawMessage `json:"creator"`
	Publisher   json.RawMessage `json:"publisher"`
	Date        string          `json:"date"`
	Language    json.RawMessage `json:"language"`
	Description json.RawMessage `json:"description"`
	ISBN        json.RawMessage `json:"isbn"`
}

// iaStrings normalises a string-or-array-of-strings field to []string,
// dropping empties. Unknown shapes yield nil.
func iaStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// Try array first.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := arr[:0:0]
		seen := map[string]struct{}{}
		for _, s := range arr {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	}
	// Fall back to scalar string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		return []string{s}
	}
	return nil
}

// iaFirstString returns the first non-empty string value, treating the input
// as either a scalar or an array of strings.
func iaFirstString(raw json.RawMessage) string {
	ss := iaStrings(raw)
	if len(ss) == 0 {
		return ""
	}
	return ss[0]
}

// iaCleanHTML strips simple HTML and decodes a handful of named entities,
// matching booklore-ng's cleanDescription.
func iaCleanHTML(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = iaTagRE.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	return strings.TrimSpace(s)
}

// iaToCandidate flattens an iaItem to a Candidate, computing the cover URL
// from the identifier. baseURL lets tests point cover URLs at the fake server.
func iaToCandidate(it iaItem, baseURL, region string, raw []byte) metadata.Candidate {
	authors := iaStrings(it.Creator)

	publisher := iaFirstString(it.Publisher)

	language := iaFirstString(it.Language)

	description := ""
	if descs := iaStrings(it.Description); len(descs) > 0 {
		description = iaCleanHTML(strings.Join(descs, "\n\n"))
	}

	isbn := iaFirstString(it.ISBN)

	published := ""
	if m := iaYearRE.FindStringSubmatch(it.Date); len(m) > 1 {
		published = m[1]
	}

	cover := ""
	if it.Identifier != "" {
		cover = fmt.Sprintf("%s/services/img/%s", baseURL, url.PathEscape(it.Identifier))
	}

	return metadata.Candidate{
		Source:      internetArchiveID,
		ExternalID:  it.Identifier,
		Title:       it.Title,
		Authors:     authors,
		Description: description,
		ISBN:        isbn,
		PublishedAt: published,
		Publisher:   publisher,
		Language:    language,
		CoverURL:    cover,
		Region:      region,
		Raw:         raw,
	}
}
