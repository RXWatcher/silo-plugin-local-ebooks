package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/metadata"
)

const worldCatID = "worldcat"
const worldCatBaseURL = "https://www.worldcat.org"

// wcMaxResults caps the number of result blocks emitted per search. Matches
// booklore-ng's `.slice(0, 10)` ceiling.
const wcMaxResults = 10

// wcISBNRE matches an ISBN-10 (with terminal check digit X/x) or a bare
// ISBN-13. Hyphens are stripped before the test. Mirrors the per-task
// spec; intentionally more permissive than the canonical isbnRE because
// WorldCat redirects /isbn/<n> for any digit-shaped input.
var wcISBNRE = regexp.MustCompile(`^(?:\d{9}[\dXx]|\d{13})$`)

// wcResultBlockRE splits the search results page into result/record
// blocks. Mirrors booklore-ng's primary itemRegex pass; both `result`
// and `record` class hooks are accepted. The terminating `</div>\s*</div>`
// is the booklore shape — fragile on deeply nested blocks but holds on
// the live search page which uses a flat row layout.
var wcResultBlockRE = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*(?:result|record)[^"]*"[^>]*>.*?</div>\s*</div>`)

// wcResultTitleRE captures the title from a result block. Prefers an
// anchor with a "title" class; the parser falls back to wcResultHeadingRE.
var wcResultTitleRE = regexp.MustCompile(`(?is)<a[^>]*class="[^"]*title[^"]*"[^>]*>([^<]+)</a>`)

// wcResultHeadingRE captures the title from an h2/h3/h4 heading inside
// the result block. Used when wcResultTitleRE does not match.
var wcResultHeadingRE = regexp.MustCompile(`(?is)<h[234][^>]*>([^<]+)</h[234]>`)

// wcResultAuthorByRE captures "by <a …>Author Name</a>" — the most common
// shape in the search list.
var wcResultAuthorByRE = regexp.MustCompile(`(?is)by\s+<[^>]*>([^<]+)</a>`)

// wcResultAuthorSpanRE captures an "author"-classed <span> as a fallback.
var wcResultAuthorSpanRE = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*author[^"]*"[^>]*>([^<]+)</span>`)

// wcResultYearRE captures the first 4-digit year in a result block.
// Stored verbatim in Candidate.PublishedAt.
var wcResultYearRE = regexp.MustCompile(`(\d{4})`)

// wcResultLanguageRE captures "Language: <Name>" up to the next tag.
var wcResultLanguageRE = regexp.MustCompile(`(?i)Language:\s*([^<]+)`)

// ---------- detail page selectors ----------

// wcDetailTitleRE prefers an h1 with a "title" class (the modern WorldCat
// layout). Falls back to og:title for older/redirect renderings.
var wcDetailTitleRE = regexp.MustCompile(`(?is)<h1[^>]*class="[^"]*title[^"]*"[^>]*>([^<]+)</h1>`)

// wcDetailOGTitleRE captures the og:title meta as the title fallback.
var wcDetailOGTitleRE = regexp.MustCompile(`(?i)<meta\s+property="og:title"\s+content="([^"]+)"`)

// wcDetailAuthorRE captures author names from `/author/<slug>` anchors.
// Multiple per page; callers dedupe.
var wcDetailAuthorRE = regexp.MustCompile(`(?is)<a[^>]*href="[^"]*/author/[^"]*"[^>]*>([^<]+)</a>`)

// wcDetailPublisherLabelRE matches the inline "Publisher: <text>" label.
var wcDetailPublisherLabelRE = regexp.MustCompile(`(?i)Publisher:\s*([^<]+)`)

// wcDetailPublisherSpanRE matches a `publisher`-classed <span>.
var wcDetailPublisherSpanRE = regexp.MustCompile(`(?is)<span[^>]*class="[^"]*publisher[^"]*"[^>]*>([^<]+)</span>`)

// wcDetailYearLabelRE captures the year on a "Year:" or "Date:" label.
var wcDetailYearLabelRE = regexp.MustCompile(`(?i)(?:Year|Date):\s*(\d{4})`)

// wcDetailCopyrightYearRE captures the year on a © prefix as a fallback
// (mirrors booklore-ng's secondary year extractor).
var wcDetailCopyrightYearRE = regexp.MustCompile(`©\s*(\d{4})`)

// wcDetailLanguageRE captures "Language: <Name>".
var wcDetailLanguageRE = regexp.MustCompile(`(?i)Language:\s*([^<]+)`)

// wcDetailSummaryRE captures the contents of #summary or
// .description. Booklore-ng uses two alternative selectors; we mirror
// both via separate regexes (RE2 has no real disjunction with capture
// groups across the alternatives, so two passes are cleaner).
var wcDetailSummaryIDRE = regexp.MustCompile(`(?is)<div[^>]*id="[^"]*summary[^"]*"[^>]*>(.*?)</div>`)

// wcDetailDescriptionClassRE captures the contents of a description-classed div.
var wcDetailDescriptionClassRE = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*description[^"]*"[^>]*>(.*?)</div>`)

// wcDetailCoverImgRE captures a cover-classed <img>'s src attribute.
var wcDetailCoverImgRE = regexp.MustCompile(`(?is)<img[^>]*class="[^"]*cover[^"]*"[^>]*src="([^"]+)"`)

// wcDetailOGImageRE captures the og:image meta as a cover fallback.
var wcDetailOGImageRE = regexp.MustCompile(`(?i)<meta\s+property="og:image"\s+content="([^"]+)"`)

// ---------- generic helpers ----------

// wcTagStripRE removes HTML tags from a captured fragment.
var wcTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// wcWSRE collapses runs of whitespace into single spaces.
var wcWSRE = regexp.MustCompile(`\s+`)

// wcNumEntityRE matches numeric decimal entities (e.g. &#39;, &#8217;).
var wcNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// WorldCat is the Source impl for worldcat.org (HTML scraping).
//
// WorldCat is OCLC's global union catalog. The official Search API
// requires a paid OCLC subscription key; booklore-ng's free-tier path
// (which we mirror) scrapes the public web pages instead:
//
//   - GET <base>/search?q=<query>   for keyword search
//   - GET <base>/isbn/<isbn>        for ISBN lookup (302 redirects to a
//     canonical record page; net/http follows redirects by default)
//
// Fragility notes:
//
// WorldCat's HTML is poorly documented and the live site has been
// reskinned several times. The regex set here is translated as-is from
// booklore-ng's worldcat.ts (December 2025 vintage). Both pages have
// multiple candidate selectors for most fields; each parser tries the
// primary selector then falls back. If WorldCat reskins, expect the
// per-field selectors to break independently — the parser is
// intentionally defensive (every field is best-effort, never required
// except Title for the detail page).
//
// Plan deviation: the task plan said to use "WorldCat Search API (free
// tier or OCLC)". In reality OCLC's API requires a paid key, and
// booklore-ng's reference scrapes HTML. We scrape HTML and document
// this as the explicit deviation. Fixtures are .html, not .json.
//
// Scope decisions that deviate from booklore-ng:
//
//   - Genres/categories are not populated. Booklore-ng emits the
//     subject/format anchor text as `categories`; in this codebase that
//     would map to Candidate.Genres, which is reserved for clean genre
//     tags. WorldCat subject anchors are noisy library headings
//     ("Fiction, English"; "Library schools — United States"), so they
//     are dropped.
//
//   - PageCount is not populated. Booklore-ng's "(\d+) pages" extractor
//     reliably false-positives on stray spans on the detail page
//     ("517 reviews", "234 holdings"); skip rather than misreport.
//
//   - ISSN search and the ISBN-13/10 separator fields from booklore-ng
//     are not surfaced. Candidate has a single ISBN field; we store
//     the hyphen-stripped input verbatim.
type WorldCat struct {
	http    *HTTPClient
	baseURL string
}

// NewWorldCat constructs the source with the production base URL.
func NewWorldCat(ua string) *WorldCat {
	return NewWorldCatAt(worldCatBaseURL, ua)
}

// NewWorldCatAt constructs the source against a custom base URL (tests).
func NewWorldCatAt(baseURL, ua string) *WorldCat {
	return &WorldCat{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (w *WorldCat) ID() string                       { return worldCatID }
func (w *WorldCat) Enabled(cfg map[string]bool) bool { return cfg[worldCatID] }

// Get fetches a single record by ISBN. Hyphens are stripped before
// validation. Input that is not an ISBN-10 or ISBN-13 short-circuits to
// (nil, nil) without a network call. 404 surfaces as ErrNotFound. A 200
// response with no extractable title also surfaces as ErrNotFound — that
// case means the ISBN landed on a redirect or empty-result page.
func (w *WorldCat) Get(ctx context.Context, isbn, region string) (*metadata.Candidate, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(isbn), "-", "")
	if !wcISBNRE.MatchString(cleaned) {
		return nil, nil
	}

	itemURL := fmt.Sprintf("%s/isbn/%s", w.baseURL, cleaned)
	body, err := w.http.GetJSON(ctx, itemURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseWorldCatItemPage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	// Per-task spec: ExternalID = ISBN, ISBN = ExternalID (i.e. both
	// fields hold the cleaned ISBN; the detail page rarely has a more
	// canonical record identifier we could surface here).
	c.ExternalID = cleaned
	c.ISBN = cleaned
	c.Source = worldCatID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries WorldCat's public keyword-search page and parses the
// returned HTML. Returns (nil, nil) for an empty query, a 404 response,
// or a page with no parseable result blocks. Capped at wcMaxResults
// rows. ExternalID is intentionally empty on search rows — the per-row
// HTML does not reliably include an ISBN.
func (w *WorldCat) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	searchURL := fmt.Sprintf("%s/search?q=%s", w.baseURL, encodeQuery(q))
	body, err := w.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows := parseWorldCatSearchResults(body)
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(rows))
	for j := range rows {
		rows[j].Source = worldCatID
		rows[j].Region = region
		rows[j].Raw = json.RawMessage(body)
		out = append(out, rows[j])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseWorldCatSearchResults extracts Candidates from the search results
// page. Blocks without a title are skipped. ExternalID is left empty
// (per-row HTML does not expose a stable, scrapeable ISBN/record id).
func parseWorldCatSearchResults(html []byte) []metadata.Candidate {
	s := string(html)

	blocks := wcResultBlockRE.FindAllString(s, -1)
	if len(blocks) == 0 {
		return nil
	}
	if len(blocks) > wcMaxResults {
		blocks = blocks[:wcMaxResults]
	}

	out := make([]metadata.Candidate, 0, len(blocks))
	for _, block := range blocks {
		title := wcStripText(wcFirstSubmatch(wcResultTitleRE, block))
		if title == "" {
			title = wcStripText(wcFirstSubmatch(wcResultHeadingRE, block))
		}
		if title == "" {
			continue
		}

		c := metadata.Candidate{Title: title}

		// Author: prefer the "by …" anchor; fall back to a span.
		if author := wcStripText(wcFirstSubmatch(wcResultAuthorByRE, block)); author != "" {
			c.Authors = []string{author}
		} else if author := wcStripText(wcFirstSubmatch(wcResultAuthorSpanRE, block)); author != "" {
			c.Authors = []string{author}
		}

		if ym := wcResultYearRE.FindStringSubmatch(block); len(ym) >= 2 {
			c.PublishedAt = ym[1]
		}

		if lang := wcStripText(wcFirstSubmatch(wcResultLanguageRE, block)); lang != "" {
			c.Language = lang
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Detail page parser
// ---------------------------------------------------------------------------

// parseWorldCatItemPage extracts a Candidate from a /isbn/<isbn> page.
// Returns nil when no usable title can be extracted (caller surfaces
// ErrNotFound). Per the type-level scope notes: Genres, PageCount, ASIN,
// Series, and SeriesPos are intentionally left empty.
func parseWorldCatItemPage(html []byte) *metadata.Candidate {
	s := string(html)

	title := wcStripText(wcFirstSubmatch(wcDetailTitleRE, s))
	if title == "" {
		title = wcStripText(wcFirstSubmatch(wcDetailOGTitleRE, s))
	}
	if title == "" {
		return nil
	}

	c := &metadata.Candidate{Title: title}

	// Authors — multiple anchors; dedupe first-wins. Cap at 5 to mirror
	// booklore-ng's defensive ceiling on noisy pages.
	if am := wcDetailAuthorRE.FindAllStringSubmatch(s, -1); len(am) > 0 {
		seen := make(map[string]bool, len(am))
		authors := make([]string, 0, len(am))
		for _, a := range am {
			name := wcStripText(a[1])
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			authors = append(authors, name)
			if len(authors) >= 5 {
				break
			}
		}
		if len(authors) > 0 {
			c.Authors = authors
		}
	}

	// Publisher — inline label preferred, classed span fallback.
	if pub := wcStripText(wcFirstSubmatch(wcDetailPublisherLabelRE, s)); pub != "" {
		c.Publisher = pub
	} else if pub := wcStripText(wcFirstSubmatch(wcDetailPublisherSpanRE, s)); pub != "" {
		c.Publisher = pub
	}

	// Year — Year:/Date: label preferred, © fallback.
	if m := wcDetailYearLabelRE.FindStringSubmatch(s); len(m) >= 2 {
		c.PublishedAt = m[1]
	} else if m := wcDetailCopyrightYearRE.FindStringSubmatch(s); len(m) >= 2 {
		c.PublishedAt = m[1]
	}

	// Language.
	if lang := wcStripText(wcFirstSubmatch(wcDetailLanguageRE, s)); lang != "" {
		c.Language = lang
	}

	// Description — #summary preferred, .description fallback. The
	// captured fragment may contain inline HTML; wcStripText flattens it.
	if desc := wcStripText(wcFirstSubmatch(wcDetailSummaryIDRE, s)); desc != "" {
		c.Description = desc
	} else if desc := wcStripText(wcFirstSubmatch(wcDetailDescriptionClassRE, s)); desc != "" {
		c.Description = desc
	}

	// Cover — class="cover" preferred, og:image fallback.
	if cover := wcFirstSubmatch(wcDetailCoverImgRE, s); cover != "" {
		c.CoverURL = cover
	} else if cover := wcFirstSubmatch(wcDetailOGImageRE, s); cover != "" {
		c.CoverURL = cover
	}

	return c
}

// ---------------------------------------------------------------------------
// Local helpers (wc-prefixed)
// ---------------------------------------------------------------------------

// wcFirstSubmatch returns submatch[1] or "" if the regex did not match.
func wcFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// wcStripText flattens HTML tags from a fragment, decodes the named and
// numeric HTML entities WorldCat emits, and collapses whitespace. Mirrors
// the isStripText/ffStripText shape.
func wcStripText(s string) string {
	if s == "" {
		return ""
	}
	s = wcTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = wcNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := wcNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	s = wcWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
