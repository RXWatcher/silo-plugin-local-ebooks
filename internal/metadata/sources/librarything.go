package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

const libraryThingID = "librarything"
const libraryThingBaseURL = "https://www.librarything.com"

// ltMaxResults caps the number of <tr class="searchresult"> rows emitted
// per search. Matches booklore-ng's `.slice(0, 10)` ceiling.
const ltMaxResults = 10

// ltISBNRE validates the input to Get: hyphen-stripped ISBN-10 or
// ISBN-13. Non-ISBN input → (nil, nil) without a network call.
var ltISBNRE = regexp.MustCompile(`^(?:\d{9}[\dXx]|\d{13})$`)

// ltSearchRowRE splits the search results page into <tr class="searchresult">…</tr>
// blocks. Each row is parsed independently with the field-specific regexes
// below. Booklore-ng filters by the `searchresult` class to skip the
// header row and other table chrome.
var ltSearchRowRE = regexp.MustCompile(`(?is)<tr[^>]*class="[^"]*searchresult[^"]*"[^>]*>.*?</tr>`)

// ltSearchTitleRE captures the title text from a /work/<id> anchor on a
// search row.
var ltSearchTitleRE = regexp.MustCompile(`(?i)<a[^>]*href="/work/\d+[^"]*"[^>]*>([^<]+)</a>`)

// ltAuthorLinkRE captures an author name from any /author/<slug> anchor.
// Used on both the search row (single match) and the detail page
// (FindAllStringSubmatch for multi-author works).
var ltAuthorLinkRE = regexp.MustCompile(`(?i)<a[^>]*href="/author/[^"]*"[^>]*>([^<]+)</a>`)

// ltYearParenRE captures the first parenthesised 4-digit year in a
// search row, e.g. "(1965)". Stored verbatim in Candidate.PublishedAt.
var ltYearParenRE = regexp.MustCompile(`\((\d{4})\)`)

// ltDetailTitleRE captures the work title from the <h1> tag on a work
// page. Fallback is og:title.
var ltDetailTitleRE = regexp.MustCompile(`(?is)<h1[^>]*>([^<]+)</h1>`)

// ltDetailOGTitleRE is the og:title fallback when the <h1> tag is
// missing or empty.
var ltDetailOGTitleRE = regexp.MustCompile(`(?i)<meta\s+property="og:title"\s+content="([^"]+)"`)

// ltDescriptionBlockRE captures the inner HTML of the
// <div id="…description…"> block. The captured fragment is then cleaned
// (br→\n, tag-strip, entity decode, collapse triple+ newlines).
var ltDescriptionBlockRE = regexp.MustCompile(`(?is)<div[^>]*id="[^"]*description[^"]*"[^>]*>(.*?)</div>`)

// ltCoverRE prefers <img class="…cover…" src="…"> — the canonical work
// cover on LibraryThing. og:image is the fallback.
var ltCoverRE = regexp.MustCompile(`(?i)<img[^>]*class="[^"]*cover[^"]*"[^>]*src="([^"]+)"`)

// ltCoverOGRE is the og:image fallback.
var ltCoverOGRE = regexp.MustCompile(`(?i)<meta\s+property="og:image"\s+content="([^"]+)"`)

// ltSeriesRE captures the series name inside the "Series:" label's
// anchor.
var ltSeriesRE = regexp.MustCompile(`(?i)Series:\s*<a[^>]*>([^<]+)</a>`)

// ltBrRE matches <br>, <br/>, <br /> case-insensitively. Used by the
// description cleaner to convert HTML line breaks to '\n'.
var ltBrRE = regexp.MustCompile(`(?i)<br\s*/?>`)

// ltTagStripRE removes HTML tags from a captured fragment.
var ltTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// ltWSRE collapses runs of whitespace into single spaces. NOT used on
// descriptions (newlines are meaningful there) — only on inline fields.
var ltWSRE = regexp.MustCompile(`\s+`)

// ltMultiNLRE collapses 3+ consecutive newlines down to 2. Mirrors
// booklore-ng's cleanDescription paragraph-spacing normaliser.
var ltMultiNLRE = regexp.MustCompile(`\n{3,}`)

// ltNumEntityRE matches numeric decimal entities (e.g. &#39;, &#8217;).
var ltNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// LibraryThing is the Source impl for librarything.com (HTML scraping).
//
// LibraryThing is a community-maintained book cataloging site. The
// ThingISBN JSON/XML API requires a key AND only returns related-ISBN
// lists (no metadata) — useless for our Candidate population. We
// therefore mirror booklore-ng's HTML-scrape approach: /isbn/<isbn>
// for direct lookup and /search.php for free-text search.
//
// Plan deviation: Task 23 originally said "ThingISBN API, no key".
// That's wrong — ThingISBN requires a key and only returns ISBN lists.
// We scrape HTML per booklore-ng's librarything provider.
//
// Scope decisions that deviate from booklore-ng:
//
//   - The `rating` field is dropped: LibraryThing's rating selector
//     (`rating[:\s]*\d`) is fragile and the Candidate type has no
//     Rating field. Not worth the maintenance cost.
//
//   - The `categories` (tag-link) list is dropped: LibraryThing tags
//     are folksonomic noise that doesn't map cleanly onto Candidate.Genres
//     (which is reserved for canonical subject taxonomies).
type LibraryThing struct {
	http    *HTTPClient
	baseURL string
}

// NewLibraryThing constructs the source with the production base URL.
func NewLibraryThing(ua string) *LibraryThing {
	return NewLibraryThingAt(libraryThingBaseURL, ua)
}

// NewLibraryThingAt constructs the source against a custom base URL (tests).
func NewLibraryThingAt(baseURL, ua string) *LibraryThing {
	return &LibraryThing{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (l *LibraryThing) ID() string                       { return libraryThingID }
func (l *LibraryThing) Enabled(cfg map[string]bool) bool { return cfg[libraryThingID] }

// Get fetches a single work by ISBN. Input is hyphen-stripped and
// validated against ltISBNRE — non-ISBN input returns (nil, nil)
// without a network call. 404 surfaces as ErrNotFound. A parsed page
// with no extractable title also surfaces as ErrNotFound (200 response
// that wasn't a real work page, e.g. a redirect landing).
func (l *LibraryThing) Get(ctx context.Context, isbn, region string) (*metadata.Candidate, error) {
	bare := strings.ReplaceAll(strings.TrimSpace(isbn), "-", "")
	if !ltISBNRE.MatchString(bare) {
		return nil, nil
	}

	workURL := fmt.Sprintf("%s/isbn/%s", l.baseURL, bare)
	body, err := l.http.GetJSON(ctx, workURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseLibraryThingWorkPage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	c.ExternalID = bare
	c.ISBN = bare
	c.Source = libraryThingID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries the LibraryThing free-text search endpoint and parses
// the HTML table of results. Returns (nil, nil) for an empty query, a
// 404 response, or a page with no parseable rows. Capped at
// ltMaxResults rows. ExternalID is left empty on search rows because
// the /work/<id> link's id is a LibraryThing work ID (not the ISBN
// that Get accepts), and we don't want callers to mistakenly feed it
// back into Get and fail.
func (l *LibraryThing) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	// `searchtype=newwork_titles` is booklore-ng's choice — the
	// "new work titles" variant returns canonical work rows rather
	// than per-edition or member-tagged rows.
	searchURL := fmt.Sprintf("%s/search.php?search=%s&searchtype=newwork_titles", l.baseURL, encodeQuery(q))
	body, err := l.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows := parseLibraryThingSearchResults(body)
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(rows))
	for j := range rows {
		rows[j].Source = libraryThingID
		rows[j].Region = region
		rows[j].Raw = json.RawMessage(body)
		out = append(out, rows[j])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseLibraryThingSearchResults extracts Candidates from the table of
// search results. Rows without a title link are skipped. ExternalID is
// intentionally left empty (see Search doc-comment).
func parseLibraryThingSearchResults(html []byte) []metadata.Candidate {
	s := string(html)

	rows := ltSearchRowRE.FindAllString(s, -1)
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > ltMaxResults {
		rows = rows[:ltMaxResults]
	}

	out := make([]metadata.Candidate, 0, len(rows))
	for _, row := range rows {
		tm := ltSearchTitleRE.FindStringSubmatch(row)
		if len(tm) < 2 {
			continue
		}
		title := ltStripText(tm[1])
		if title == "" {
			continue
		}

		c := metadata.Candidate{Title: title}

		if am := ltAuthorLinkRE.FindStringSubmatch(row); len(am) >= 2 {
			if name := ltStripText(am[1]); name != "" {
				c.Authors = []string{name}
			}
		}

		if ym := ltYearParenRE.FindStringSubmatch(row); len(ym) >= 2 {
			c.PublishedAt = ym[1]
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Detail page parser
// ---------------------------------------------------------------------------

// parseLibraryThingWorkPage extracts a Candidate from a /isbn/<isbn>
// (or /work/<id>) page. Returns nil when no usable title can be
// extracted — the caller surfaces that as ErrNotFound.
func parseLibraryThingWorkPage(html []byte) *metadata.Candidate {
	s := string(html)

	title := ltStripText(ltFirstSubmatch(ltDetailTitleRE, s))
	if title == "" {
		title = ltStripText(ltFirstSubmatch(ltDetailOGTitleRE, s))
	}
	if title == "" {
		return nil
	}

	c := &metadata.Candidate{Title: title}

	// Authors — dedupe first-wins. The work page lists the same
	// author in both the header and the work-detail block.
	if am := ltAuthorLinkRE.FindAllStringSubmatch(s, -1); len(am) > 0 {
		seen := make(map[string]bool, len(am))
		authors := make([]string, 0, len(am))
		for _, a := range am {
			name := ltStripText(a[1])
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			authors = append(authors, name)
		}
		if len(authors) > 0 {
			c.Authors = authors
		}
	}

	// Description — convert <br> to \n, strip remaining tags, decode
	// entities, collapse 3+ newlines to 2. Mirrors booklore-ng's
	// cleanDescription.
	if desc := ltFirstSubmatch(ltDescriptionBlockRE, s); desc != "" {
		c.Description = ltCleanDescription(desc)
	}

	// Cover URL — class="…cover…" preferred, og:image fallback.
	if cover := ltFirstSubmatch(ltCoverRE, s); cover != "" {
		c.CoverURL = cover
	} else if cover := ltFirstSubmatch(ltCoverOGRE, s); cover != "" {
		c.CoverURL = cover
	}

	// Series name.
	if series := ltStripText(ltFirstSubmatch(ltSeriesRE, s)); series != "" {
		c.Series = series
	}

	return c
}

// ---------------------------------------------------------------------------
// Local helpers (lt-prefixed)
// ---------------------------------------------------------------------------

// ltFirstSubmatch returns submatch[1] or "" if the regex did not match.
func ltFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ltCleanDescription mirrors booklore-ng's cleanDescription: convert
// <br> to '\n', strip remaining HTML tags, decode entities, collapse
// runs of 3+ newlines down to 2. NOT whitespace-collapsed because
// paragraph structure is meaningful in a description.
func ltCleanDescription(s string) string {
	if s == "" {
		return ""
	}
	s = ltBrRE.ReplaceAllString(s, "\n")
	s = ltTagStripRE.ReplaceAllString(s, "")
	s = ltDecodeEntities(s)
	s = ltMultiNLRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// ltStripText flattens HTML tags from a fragment, decodes entities, and
// collapses whitespace. Mirrors isStripText. Used for inline fields
// (title, author name, series name) — not for descriptions.
func ltStripText(s string) string {
	if s == "" {
		return ""
	}
	s = ltTagStripRE.ReplaceAllString(s, " ")
	s = ltDecodeEntities(s)
	s = ltWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// ltDecodeEntities decodes the named + numeric HTML entities
// LibraryThing emits. Same set as ISFDB's isStripText.
func ltDecodeEntities(s string) string {
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = ltNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := ltNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	return s
}
