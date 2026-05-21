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

const fantasticFictionID = "fantasticfiction"
const fantasticFictionBaseURL = "https://www.fantasticfiction.com"

// ffMaxResults caps the number of book blocks emitted per search. Matches
// booklore-ng's `.slice(0, 10)` ceiling.
const ffMaxResults = 10

// ffBookBlockRE splits the search page into per-book <div class="…book…">
// blocks. We parse each block independently with the field-specific regexes
// below. NOTE: this is a non-greedy match to the first </div> after the
// opening tag — mirrors booklore-ng exactly, fragile on nested <div>s but
// the live site's search page is flat enough that the simple shape holds.
var ffBookBlockRE = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*book[^"]*"[^>]*>.*?</div>`)

// ffTitleRE captures the first anchor's text content inside a block. The
// title link's href is the canonical book path on Fantastic Fiction but is
// not used here: see the FantasticFiction type comment for why we don't
// expose a per-book Get.
var ffTitleRE = regexp.MustCompile(`(?i)<a[^>]*href="[^"]*"[^>]*>([^<]+)</a>`)

// ffAuthorRE captures "by <a …>Author Name</a>" — single author per block,
// mirroring booklore-ng's extraction. Multi-author rows on the live site
// usually render the second author in a separate trailing fragment that
// this pattern intentionally ignores (low signal, high noise).
var ffAuthorRE = regexp.MustCompile(`(?is)by\s+<a[^>]*>([^<]+)</a>`)

// ffYearRE captures the first parenthesised 4-digit year in a block, e.g.
// "(2021)". Stored as a 4-character string in Candidate.PublishedAt.
var ffYearRE = regexp.MustCompile(`\((\d{4})\)`)

// ffSeriesRE captures the "Series: <a>Name</a>" fragment that appears for
// books that belong to a named series.
var ffSeriesRE = regexp.MustCompile(`(?is)Series:\s*<a[^>]*>([^<]+)</a>`)

// ffTagStripRE removes HTML tags from a captured fragment.
var ffTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// ffWSRE collapses runs of whitespace into single spaces.
var ffWSRE = regexp.MustCompile(`\s+`)

// ffNumEntityRE matches numeric decimal entities (e.g. &#39;, &#8217;).
var ffNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// FantasticFiction is the Source impl for fantasticfiction.com (HTML scraping).
//
// Fantastic Fiction is a genre-fiction specialist database with no public
// API; both author and title search return HTML pages, scraped here with
// regex (mirroring booklore-ng's implementation).
//
// Scope decisions that deviate from booklore-ng:
//
//   - Get is a no-op nil-returner. Fantastic Fiction has no stable,
//     parseable per-book ID; booklore-ng itself does not implement a
//     detail-page fetch either. Rather than invent a slug-based scheme we
//     report (nil, nil) for every Get call. See Get's doc comment.
//
//   - Search uses `searchfor=book` (title search). The plugin's Search
//     takes a single free-text query, so we pick the parser pass that
//     yields book records over the one that yields author hits.
//
//   - The hardcoded `genres: ['Fiction']` that booklore-ng emits per row is
//     dropped — that's site-flavor noise, not real per-book metadata.
//
//   - The "Author: X" pseudo-result pass is dropped. Candidates are
//     supposed to represent books; emitting fake book rows titled
//     "Author: Andy Weir" is confusing to downstream consumers.
type FantasticFiction struct {
	http    *HTTPClient
	baseURL string
}

// NewFantasticFiction constructs the source with the production base URL.
func NewFantasticFiction(ua string) *FantasticFiction {
	return NewFantasticFictionAt(fantasticFictionBaseURL, ua)
}

// NewFantasticFictionAt constructs the source against a custom base URL (tests).
func NewFantasticFictionAt(baseURL, ua string) *FantasticFiction {
	return &FantasticFiction{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (f *FantasticFiction) ID() string                       { return fantasticFictionID }
func (f *FantasticFiction) Enabled(cfg map[string]bool) bool { return cfg[fantasticFictionID] }

// Get always returns (nil, nil) without making a network call. Fantastic
// Fiction does not expose a stable per-book identifier in its URL scheme
// (the canonical path is `/<letter>/<author-slug>/<book-slug>.htm`, which
// is neither stable nor reliably reconstructible from any field we
// surface). Rather than fabricate a slug-based ID, we treat this source as
// search-only. Callers that resolve a candidate via Search should treat
// the embedded Raw HTML as the source of truth.
func (f *FantasticFiction) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	return nil, nil
}

// Search queries Fantastic Fiction's title-search endpoint and parses the
// returned HTML. Returns (nil, nil) for an empty query, a 404 response, or
// a page with no parseable book blocks. Per booklore-ng's behaviour we cap
// at the first ffMaxResults book blocks.
func (f *FantasticFiction) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	// `searchfor=book` is the title-search variant. The author-search
	// variant emits author-page links instead of book records; we want
	// books.
	searchURL := fmt.Sprintf("%s/search/?searchfor=book&keywords=%s", f.baseURL, encodeQuery(q))
	body, err := f.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	books := parseFantasticFictionSearchPage(body)
	if len(books) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(books))
	for i := range books {
		books[i].Source = fantasticFictionID
		books[i].Region = region
		books[i].Raw = json.RawMessage(body)
		out = append(out, books[i])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseFantasticFictionSearchPage extracts Candidates from the title-search
// results page. Blocks without a title are skipped. ExternalID is left empty
// (see FantasticFiction type doc).
func parseFantasticFictionSearchPage(html []byte) []metadata.Candidate {
	s := string(html)

	blocks := ffBookBlockRE.FindAllString(s, -1)
	if len(blocks) == 0 {
		return nil
	}
	if len(blocks) > ffMaxResults {
		blocks = blocks[:ffMaxResults]
	}

	out := make([]metadata.Candidate, 0, len(blocks))
	for _, block := range blocks {
		title := ffStripText(ffFirstSubmatch(ffTitleRE, block))
		if title == "" {
			continue
		}

		c := metadata.Candidate{Title: title}

		if author := ffStripText(ffFirstSubmatch(ffAuthorRE, block)); author != "" {
			c.Authors = []string{author}
		}

		if m := ffYearRE.FindStringSubmatch(block); len(m) >= 2 {
			c.PublishedAt = m[1]
		}

		if series := ffStripText(ffFirstSubmatch(ffSeriesRE, block)); series != "" {
			c.Series = series
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Local helpers (ff-prefixed)
// ---------------------------------------------------------------------------

// ffFirstSubmatch returns submatch[1] or "" if the regex did not match.
func ffFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ffStripText flattens HTML tags from a fragment, decodes the named and
// numeric HTML entities Fantastic Fiction emits, and collapses whitespace.
// Mirrors booklore-ng's decoder scope and Anna's Archive's helper shape.
func ffStripText(s string) string {
	if s == "" {
		return ""
	}
	s = ffTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = ffNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := ffNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	s = ffWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
