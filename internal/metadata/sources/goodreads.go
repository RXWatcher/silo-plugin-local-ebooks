package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const goodreadsID = "goodreads"
const goodreadsBaseURL = "https://www.goodreads.com"

// grMaxResults caps scraped search rows (matches the package convention).
const grMaxResults = 10

// numericRE matches a non-empty string of only ASCII digits.
var numericRE = regexp.MustCompile(`^\d+$`)

// grSearchRowRE extracts one search result row worth of data via simple regex.
// Fields: (1) book ID, (2) title.
var grSearchRowRE = regexp.MustCompile(`(?i)href="/book/show/(\d+)[^"]*"[^>]*>\s*<span[^>]*itemprop="name"[^>]*>([^<]+)</span>`)

// grAuthorRE extracts the author name for a search row.
var grAuthorRE = regexp.MustCompile(`(?i)class="authorName"[^>]*>[^<]*<span[^>]*itemprop="name"[^>]*>([^<]+)</span>`)

// grCoverRE extracts the book cover image for a search row.
var grCoverRE = regexp.MustCompile(`(?i)<img[^>]*class="bookCover"[^>]*src="([^"]+)"`)

// grJSONLDRE matches <script type="application/ld+json"> blocks.
var grJSONLDRE = regexp.MustCompile(`(?i)<script[^>]*type="application/ld\+json"[^>]*>([\s\S]*?)</script>`)

// grNextDataRE matches the __NEXT_DATA__ script block embedded by Next.js.
var grNextDataRE = regexp.MustCompile(`(?i)<script[^>]*id="__NEXT_DATA__"[^>]*>([\s\S]*?)</script>`)

// Goodreads is the Source impl for goodreads.com (HTML scraping).
type Goodreads struct {
	http    *HTTPClient
	baseURL string
}

// NewGoodreads constructs the source with the production base URL.
func NewGoodreads(ua string) *Goodreads {
	return NewGoodreadsAt(goodreadsBaseURL, ua)
}

// NewGoodreadsAt constructs the source against a custom base URL (tests).
func NewGoodreadsAt(baseURL, ua string) *Goodreads {
	return &Goodreads{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (g *Goodreads) ID() string                       { return goodreadsID }
func (g *Goodreads) Enabled(cfg map[string]bool) bool { return cfg[goodreadsID] }

// Get fetches a single book by numeric Goodreads book ID.
// Returns (nil, nil) for non-numeric input.
func (g *Goodreads) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if !numericRE.MatchString(id) {
		return nil, nil
	}

	bookURL := fmt.Sprintf("%s/book/show/%s", g.baseURL, id)
	body, err := g.http.GetJSON(ctx, bookURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseGoodreadsBookPage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	if c.ExternalID == "" {
		c.ExternalID = id
	}
	c.Source = goodreadsID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries Goodreads for books matching the given text.
func (g *Goodreads) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	searchURL := fmt.Sprintf("%s/search?q=%s", g.baseURL, encodeQuery(q))
	body, err := g.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	books := parseGoodreadsSearchPage(body)
	out := make([]metadata.Candidate, 0, len(books))
	for i := range books {
		books[i].Source = goodreadsID
		books[i].Region = region
		if books[i].Raw == nil {
			books[i].Raw = json.RawMessage(body)
		}
		out = append(out, books[i])
	}
	return out, nil
}

// encodeQuery percent-encodes a search query (spaces as +).
func encodeQuery(q string) string {
	return strings.ReplaceAll(q, " ", "+")
}

// ---------------------------------------------------------------------------
// Book page parser
// ---------------------------------------------------------------------------

// parseGoodreadsBookPage extracts a Candidate from an HTML page body.
// Strategy: JSON-LD first, then __NEXT_DATA__ fallback.
func parseGoodreadsBookPage(html []byte) *metadata.Candidate {
	s := string(html)

	if c := parseGoodreadsJSONLD(s); c != nil {
		return c
	}

	if c := parseGoodreadsNextData(s); c != nil {
		return c
	}

	return nil
}

// parseGoodreadsJSONLD looks for a JSON-LD <script> block with @type "Book".
func parseGoodreadsJSONLD(html string) *metadata.Candidate {
	allMatches := grJSONLDRE.FindAllStringSubmatch(html, -1)
	var doc map[string]json.RawMessage
	found := false
	for _, matches := range allMatches {
		if len(matches) < 2 {
			continue
		}
		var d map[string]json.RawMessage
		if err := json.Unmarshal([]byte(matches[1]), &d); err != nil {
			continue
		}
		var typ string
		if err := json.Unmarshal(d["@type"], &typ); err != nil {
			continue
		}
		if typ == "Book" {
			doc = d
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	c := &metadata.Candidate{}

	if nameRaw, ok := doc["name"]; ok {
		var name string
		if json.Unmarshal(nameRaw, &name) == nil {
			c.Title = name
		}
	}

	if descRaw, ok := doc["description"]; ok {
		var desc string
		if json.Unmarshal(descRaw, &desc) == nil {
			c.Description = desc
		}
	}

	if isbnRaw, ok := doc["isbn"]; ok {
		var isbn string
		if json.Unmarshal(isbnRaw, &isbn) == nil {
			c.ISBN = isbn
		}
	}

	if imgRaw, ok := doc["image"]; ok {
		var img string
		if json.Unmarshal(imgRaw, &img) == nil {
			c.CoverURL = img
		}
	}

	if dateRaw, ok := doc["datePublished"]; ok {
		var date string
		if json.Unmarshal(dateRaw, &date) == nil {
			if len(date) > 10 {
				date = date[:10]
			}
			c.PublishedAt = date
		}
	}

	if langRaw, ok := doc["inLanguage"]; ok {
		var lang string
		if json.Unmarshal(langRaw, &lang) == nil {
			c.Language = lang
		}
	}

	if pubRaw, ok := doc["publisher"]; ok {
		var pubName string
		if json.Unmarshal(pubRaw, &pubName) == nil {
			c.Publisher = pubName
		} else {
			var pubObj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(pubRaw, &pubObj) == nil {
				c.Publisher = pubObj.Name
			}
		}
	}

	if authRaw, ok := doc["author"]; ok {
		c.Authors = grExtractNames(authRaw)
	}

	// numberOfPages — may appear in JSON-LD on some Goodreads pages
	if pagesRaw, ok := doc["numberOfPages"]; ok {
		var pages int
		if json.Unmarshal(pagesRaw, &pages) == nil && pages > 0 {
			c.PageCount = pages
		}
	}

	// url — derive ExternalID from numeric book ID in path
	if urlRaw, ok := doc["url"]; ok {
		var pageURL string
		if json.Unmarshal(urlRaw, &pageURL) == nil {
			c.ExternalID = goodreadsIDFromURL(pageURL)
		}
	}

	if c.Title == "" {
		return nil
	}
	return c
}

// goodreadsIDFromURL extracts the numeric book ID from a Goodreads book URL.
// e.g. "https://www.goodreads.com/book/show/54493401-project-hail-mary" → "54493401"
func goodreadsIDFromURL(u string) string {
	// last path segment before any hyphen
	parts := strings.Split(strings.TrimSuffix(u, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	// numeric part before first hyphen
	if idx := strings.Index(last, "-"); idx >= 0 {
		last = last[:idx]
	}
	if numericRE.MatchString(last) {
		return last
	}
	return ""
}

// ---------------------------------------------------------------------------
// __NEXT_DATA__ fallback parser
// ---------------------------------------------------------------------------

// parseGoodreadsNextData attempts to extract a single Book from __NEXT_DATA__.
func parseGoodreadsNextData(html string) *metadata.Candidate {
	m := grNextDataRE.FindStringSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	var data interface{}
	if err := json.Unmarshal([]byte(m[1]), &data); err != nil {
		return nil
	}
	var out []metadata.Candidate
	traverseGoodreadsNextData(data, &out, 0)
	if len(out) > 0 {
		return &out[0]
	}
	return nil
}

// maxGoodreadsTraverseDepth bounds recursion when walking the scraped
// __NEXT_DATA__ JSON tree. The page is attacker-influenceable; an unbounded
// walk over deeply-nested JSON (~500k levels fits in the 1 MB SoftLimit)
// grows the goroutine stack until the process fatally panics.
const maxGoodreadsTraverseDepth = 64

func traverseGoodreadsNextData(v interface{}, out *[]metadata.Candidate, depth int) {
	if depth > maxGoodreadsTraverseDepth {
		return
	}
	switch val := v.(type) {
	case []interface{}:
		for _, item := range val {
			traverseGoodreadsNextData(item, out, depth+1)
		}
	case map[string]interface{}:
		if c := goodreadsNextDataBookToCandidate(val); c != nil {
			*out = append(*out, *c)
			return
		}
		for _, child := range val {
			traverseGoodreadsNextData(child, out, depth+1)
		}
	}
}

// goodreadsNextDataBookToCandidate tries to map a __NEXT_DATA__ object to a Candidate.
// Goodreads embeds book data with fields like "title", "description", "imageUrl", "genres".
func goodreadsNextDataBookToCandidate(m map[string]interface{}) *metadata.Candidate {
	title := grStringField(m, "title")
	if title == "" {
		return nil
	}
	// Heuristic: must also have at least one of these fields to be a book record.
	_, hasDesc := m["description"]
	_, hasImage := m["imageUrl"]
	_, hasISBN := m["isbn"]
	_, hasGenres := m["genres"]
	if !hasDesc && !hasImage && !hasISBN && !hasGenres {
		return nil
	}

	c := &metadata.Candidate{Title: title}
	c.Description = grStringField(m, "description")
	c.ISBN = grStringField(m, "isbn")
	c.CoverURL = grStringField(m, "imageUrl")
	c.Publisher = grStringField(m, "publisher")
	c.Language = grStringField(m, "language")

	pub := grStringField(m, "publicationDate")
	if len(pub) > 10 {
		pub = pub[:10]
	}
	c.PublishedAt = pub

	// ExternalID
	if id, ok := m["legacyId"].(float64); ok {
		c.ExternalID = fmt.Sprintf("%d", int(id))
	}
	if c.ExternalID == "" {
		c.ExternalID = grStringField(m, "id")
	}

	// pageCount
	if p, ok := m["numPages"].(float64); ok && p > 0 {
		c.PageCount = int(p)
	}

	// authors array: [{name: "..."}, ...]
	if arr, ok := m["authors"].([]interface{}); ok {
		for _, a := range arr {
			if obj, ok := a.(map[string]interface{}); ok {
				if name := grStringField(obj, "name"); name != "" {
					c.Authors = append(c.Authors, name)
				}
			}
		}
	}

	// genres: array of strings or objects with "name"
	if arr, ok := m["genres"].([]interface{}); ok {
		for _, g := range arr {
			switch gv := g.(type) {
			case string:
				if gv != "" {
					c.Genres = append(c.Genres, gv)
				}
			case map[string]interface{}:
				if name := grStringField(gv, "name"); name != "" {
					c.Genres = append(c.Genres, name)
				}
			}
		}
	}

	return c
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseGoodreadsSearchPage extracts Candidates from a Goodreads search results page.
// Uses simple regex over the tableList HTML rows.
func parseGoodreadsSearchPage(html []byte) []metadata.Candidate {
	s := string(html)

	// Extract all book rows from the .tableList table.
	// Each row contains a bookTitle link, an authorName link, and a bookCover img.
	titleMatches := grSearchRowRE.FindAllStringSubmatch(s, -1)
	if len(titleMatches) == 0 {
		return nil
	}
	// Cap result rows like every other scraper in this package
	// (wc/is/lt/ff/db all use 10): each Candidate aliases the full body via
	// Raw, so an unbounded hostile result page amplifies memory by row count.
	if len(titleMatches) > grMaxResults {
		titleMatches = titleMatches[:grMaxResults]
	}

	authorMatches := grAuthorRE.FindAllStringSubmatch(s, -1)
	coverMatches := grCoverRE.FindAllStringSubmatch(s, -1)

	out := make([]metadata.Candidate, 0, len(titleMatches))
	for i, tm := range titleMatches {
		if len(tm) < 3 {
			continue
		}
		bookID := strings.TrimSpace(tm[1])
		title := strings.TrimSpace(tm[2])
		if title == "" || bookID == "" {
			continue
		}

		c := metadata.Candidate{
			ExternalID: bookID,
			Title:      title,
		}

		if i < len(authorMatches) && len(authorMatches[i]) >= 2 {
			author := strings.TrimSpace(authorMatches[i][1])
			if author != "" {
				c.Authors = []string{author}
			}
		}

		if i < len(coverMatches) && len(coverMatches[i]) >= 2 {
			c.CoverURL = strings.TrimSpace(coverMatches[i][1])
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Local helpers (not shared with other sources in this package)
// ---------------------------------------------------------------------------

// grStringField returns a string map value or "" if missing / not a string.
func grStringField(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// grExtractNames decodes a JSON value that may be a single {name} object
// or an array of {name} objects, or a plain string.
func grExtractNames(raw json.RawMessage) []string {
	// Try array of {name} objects.
	var arr []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		names := make([]string, 0, len(arr))
		for _, a := range arr {
			if a.Name != "" {
				names = append(names, a.Name)
			}
		}
		if len(names) > 0 {
			return names
		}
	}
	// Try single {name} object.
	var single struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Name != "" {
		return []string{single.Name}
	}
	// Try plain string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []string{s}
	}
	return nil
}
