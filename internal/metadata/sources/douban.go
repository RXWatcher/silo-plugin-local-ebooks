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

const doubanID = "douban"
const doubanBaseURL = "https://book.douban.com"

// dbMaxResults caps the number of search-result entries emitted.
// Mirrors booklore-ng's implicit ceiling on the JSON `items` array.
const dbMaxResults = 10

// dbNumericIDRE validates Get input: Douban subject IDs are bare
// integers (e.g. 2567698 for 三体 / The Three-Body Problem). Non-numeric
// input → (nil, nil) without a network call.
var dbNumericIDRE = regexp.MustCompile(`^\d+$`)

// dbDataBlockRE captures the JSON blob that Douban's search-results page
// emits as `window.__DATA__ = { ... };`. Mirrors booklore-ng's regex; the
// trailing `;` and lazy `[\s\S]*?` together bound the match to the first
// complete assignment. RE2-safe (no lookaround/backrefs).
var dbDataBlockRE = regexp.MustCompile(`(?s)window\.__DATA__\s*=\s*(\{.*?\});`)

// dbSubjectIDInURLRE captures the numeric subject ID from a Douban URL
// of the form ".../subject/<id>/...". Used to extract ExternalID from
// each search result's `url` field.
var dbSubjectIDInURLRE = regexp.MustCompile(`/subject/(\d+)/`)

// dbTitleRE captures the canonical title from a detail page. Douban
// emits the book title inside the `v:itemreviewed` microdata span,
// nested in the main <h1>. The fallback (dbTitleFallbackRE) handles
// older/varied page layouts.
var dbTitleRE = regexp.MustCompile(`(?is)<span\s+property="v:itemreviewed"[^>]*>([^<]+)</span>`)

// dbTitleFallbackRE matches the page's <title> tag. Douban appends
// " (豆瓣)" (literally "(Douban)") to every book page title; callers
// strip that suffix before use.
var dbTitleFallbackRE = regexp.MustCompile(`(?is)<title>\s*([^<]+?)\s*</title>`)

// dbInfoBlockRE isolates the metadata block. Douban wraps all book
// attributes (author, publisher, date, ISBN, etc.) in <div id="info">.
// Isolating the block first keeps the per-label regexes from matching
// unrelated text elsewhere on the page (e.g. recommendations sidebar).
var dbInfoBlockRE = regexp.MustCompile(`(?is)<div\s+id="info"[^>]*>(.*?)</div>`)

// Per-label regexes operate on each tag-stripped line of the info
// block (one line per Douban label, split on <br /> by dbInfoLinesFrom).
// Douban uses Chinese labels (作者 Author, 出版社 Publisher, 出版年
// Publication-year, ISBN, 页数 Pages, 副标题 Subtitle, 丛书 Series).
// We allow whitespace between the label and its colon because Douban
// wraps the label in <span class="pl"> and the colon sits outside the
// span — after tag-stripping there's a space between them.
var dbInfoAuthorRE = regexp.MustCompile(`^\s*作者\s*[:：]\s*(.+?)\s*$`)
var dbInfoPublisherRE = regexp.MustCompile(`^\s*出版社\s*[:：]\s*(.+?)\s*$`)
var dbInfoYearRE = regexp.MustCompile(`^\s*出版年\s*[:：]\s*(.+?)\s*$`)
var dbInfoISBNRE = regexp.MustCompile(`^\s*ISBN\s*[:：]\s*([\dXx-]+)\s*$`)
var dbInfoSeriesRE = regexp.MustCompile(`^\s*丛书\s*[:：]\s*(.+?)\s*$`)

// dbAuthorAnchorRE captures author names when Douban renders the
// `作者:` line as a sequence of <a href="/author/..."> anchors rather
// than plain text. Preferred over the plain-text path because it gives
// per-author splits without needing to guess the separator.
var dbAuthorAnchorRE = regexp.MustCompile(`(?is)作者[:：]\s*((?:<a[^>]*>[^<]+</a>\s*/?\s*)+)`)

// dbAnchorTextRE captures the text inside an <a>…</a> tag. Used after
// dbAuthorAnchorRE isolates the author-list fragment.
var dbAnchorTextRE = regexp.MustCompile(`(?is)<a[^>]*>([^<]+)</a>`)

// dbDescRE captures the description from the related_info block. Douban
// renders the "intro" in a <span class="all hidden"> when truncated and
// in <div class="intro"> when fully shown; we prefer the all-hidden
// variant because it carries the unabridged text.
var dbDescRE = regexp.MustCompile(`(?is)<span\s+class="all\s+hidden"[^>]*>(.*?)</span>`)

// dbDescFallbackRE captures the first <div class="intro"> inside the
// related_info block as a fallback when the all-hidden variant is
// absent (short descriptions are inlined directly).
var dbDescFallbackRE = regexp.MustCompile(`(?is)<div\s+class="intro"[^>]*>(.*?)</div>`)

// dbCoverRE captures the cover image URL. Douban's main cover lives in
// <a class="nbg"><img src="..."/></a>. The `nbg` class is stable across
// the layouts we've sampled.
var dbCoverRE = regexp.MustCompile(`(?is)<a[^>]+class="nbg"[^>]*>\s*<img[^>]+src="([^"]+)"`)

// dbCoverFallbackRE accepts the <div id="mainpic"> layout used on
// older Douban book pages.
var dbCoverFallbackRE = regexp.MustCompile(`(?is)<div\s+id="mainpic"[^>]*>.*?<img[^>]+src="([^"]+)"`)

// dbYearExtractRE pulls a 4-digit year out of any Douban date string
// (handles "2008-1", "2008-01-01", "2008年1月", or "2008"). Stored
// verbatim as the candidate's PublishedAt.
var dbYearExtractRE = regexp.MustCompile(`(\d{4})`)

// dbTagStripRE removes HTML tags from a captured fragment.
var dbTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// dbWSRE collapses runs of whitespace into single spaces.
var dbWSRE = regexp.MustCompile(`\s+`)

// dbNumEntityRE matches numeric decimal HTML entities (e.g. &#39;).
var dbNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// Douban is the Source impl for book.douban.com (HTML scraping).
//
// Douban (豆瓣读书) is China's largest community-curated book catalog.
// It has no public API; the site serves HTML pages indexed by stable
// numeric "subject IDs" (e.g. /subject/2567698/ for 三体). This source
// is region-targeted: most records are Chinese-language, so candidates
// have Language set to "zh".
//
// Reference: booklore-ng's src/lib/metadata/providers/douban.ts.
// Booklore's reference only implements *search* against the JSON blob
// embedded in the search-results page (window.__DATA__ = {...};). It
// does NOT scrape the detail page. The Get path here is therefore
// new code, modelled on the detail-page structure (Douban's layout is
// stable but not guaranteed; see the per-regex comments for fragility).
//
// Search URL caveat: in production, Douban's search lives on
// `search.douban.com/book/subject_search` (a different host from the
// detail-page base). For test hermeticity and constructor simplicity,
// this impl points the search call at `<baseURL>/subject_search`. The
// production base URL `book.douban.com/subject_search` redirects to the
// search host transparently, so the call still works end-to-end. A
// future enhancement would split the search host into a separate
// constructor parameter; documenting rather than implementing now.
//
// Scope decisions:
//
//   - Set Language to "zh" on every candidate. Douban's catalog is
//     overwhelmingly Chinese-language; the few translated-into-Chinese
//     pages still describe the Chinese edition. If we encounter a
//     bilingual edition we'd want to revisit, but the field reflects
//     the *edition* not the *original* work.
//
//   - PageCount, Genres, ASIN are NOT populated per the plan. Douban
//     does emit a 页数 ("pages") label, but the value is often noisy
//     (e.g. "517页" with a Chinese suffix) and the spec scopes us out.
//
//   - Description is the unabridged intro (the all-hidden variant)
//     when available, falling back to the visible intro <div>.
type Douban struct {
	http    *HTTPClient
	baseURL string
}

// NewDouban constructs the source with the production base URL.
func NewDouban(ua string) *Douban {
	return NewDoubanAt(doubanBaseURL, ua)
}

// NewDoubanAt constructs the source against a custom base URL (tests).
func NewDoubanAt(baseURL, ua string) *Douban {
	return &Douban{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (d *Douban) ID() string                       { return doubanID }
func (d *Douban) Enabled(cfg map[string]bool) bool { return cfg[doubanID] }

// Get fetches a single book by its numeric Douban subject ID. Input
// that is not a bare integer returns (nil, nil) without a network call.
// 404 surfaces as ErrNotFound. A parsed page with no extractable title
// also surfaces as ErrNotFound — that case means the response was 200
// but not a valid subject page (e.g. a "subject removed" landing page).
func (d *Douban) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if !dbNumericIDRE.MatchString(id) {
		return nil, nil
	}

	subjectURL := fmt.Sprintf("%s/subject/%s/", d.baseURL, id)
	body, err := d.http.GetJSON(ctx, subjectURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseDoubanSubjectPage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	c.ExternalID = id
	c.Source = doubanID
	c.Region = region
	c.Language = "zh"
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries Douban's subject-search endpoint and parses the JSON
// blob embedded in the results page (window.__DATA__ = {...};).
// Returns (nil, nil) for an empty query, a 404 response, or a page that
// does not embed the expected JSON. Capped at dbMaxResults entries.
//
// Booklore-ng's reference shows that Douban's search page DOES embed
// the structured data inline (despite being JS-rendered for the user);
// we extract that JSON rather than scraping the rendered DOM. If
// Douban ever migrates to a fully client-rendered search page the
// regex match will fail and this method will return (nil, nil), which
// matches the "no results" path that downstream consumers already
// handle.
func (d *Douban) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	searchURL := fmt.Sprintf("%s/subject_search?search_text=%s", d.baseURL, encodeQuery(q))
	body, err := d.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	items := parseDoubanSearchPage(body)
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(items))
	for i := range items {
		items[i].Source = doubanID
		items[i].Region = region
		items[i].Language = "zh"
		items[i].Raw = json.RawMessage(body)
		out = append(out, items[i])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// dbSearchItem is the on-the-wire shape of a single entry in the
// `items` array inside window.__DATA__. Field names mirror Douban's
// emitted JSON; only the fields we surface are decoded.
type dbSearchItem struct {
	Title    string `json:"title"`
	URL      string `json:"url"`
	CoverURL string `json:"cover_url"`
	Abstract string `json:"abstract"`
}

// dbSearchData wraps the items array under the top-level JSON key.
type dbSearchData struct {
	Items []dbSearchItem `json:"items"`
}

// parseDoubanSearchPage extracts the embedded JSON from the search
// results HTML and translates each item to a Candidate. Returns nil
// when the JSON blob is missing, malformed, or empty.
func parseDoubanSearchPage(html []byte) []metadata.Candidate {
	m := dbDataBlockRE.FindSubmatch(html)
	if len(m) < 2 {
		return nil
	}
	var data dbSearchData
	if err := json.Unmarshal(m[1], &data); err != nil {
		return nil
	}
	if len(data.Items) == 0 {
		return nil
	}
	items := data.Items
	if len(items) > dbMaxResults {
		items = items[:dbMaxResults]
	}

	out := make([]metadata.Candidate, 0, len(items))
	for _, it := range items {
		title := dbStripText(it.Title)
		if title == "" {
			continue
		}
		c := metadata.Candidate{
			Title:    title,
			CoverURL: it.CoverURL,
		}
		// External ID: extract the numeric subject ID from the URL.
		if sm := dbSubjectIDInURLRE.FindStringSubmatch(it.URL); len(sm) >= 2 {
			c.ExternalID = sm[1]
		}
		// Abstract format: "Author1 / Author2 / Publisher / 2008-01 / 价格".
		// We pull authors (everything before the third-to-last slash) and
		// the year from the second-to-last segment when there are >=4
		// segments. Two-segment abstracts (just "Author / Publisher")
		// degrade gracefully to author-only.
		if it.Abstract != "" {
			parts := strings.Split(it.Abstract, " / ")
			cleaned := make([]string, 0, len(parts))
			for _, p := range parts {
				if t := strings.TrimSpace(p); t != "" {
					cleaned = append(cleaned, t)
				}
			}
			switch {
			case len(cleaned) >= 4:
				c.Authors = cleaned[:len(cleaned)-3]
				c.Publisher = cleaned[len(cleaned)-3]
				if y := dbYearExtractRE.FindStringSubmatch(cleaned[len(cleaned)-2]); len(y) >= 2 {
					c.PublishedAt = y[1]
				}
			case len(cleaned) >= 2:
				c.Authors = cleaned[:1]
				if len(cleaned) >= 3 {
					c.Publisher = cleaned[1]
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Detail page parser
// ---------------------------------------------------------------------------

// parseDoubanSubjectPage extracts a Candidate from a /subject/<id>/
// page. Returns nil when no usable title can be extracted (caller
// surfaces ErrNotFound). The parser is defensive: missing fields are
// silently skipped rather than failing the whole parse.
func parseDoubanSubjectPage(html []byte) *metadata.Candidate {
	s := string(html)

	// Title — prefer the microdata span, fall back to the <title> tag
	// (stripping the " (豆瓣)" suffix Douban appends to every page).
	title := dbStripText(dbFirstSubmatch(dbTitleRE, s))
	if title == "" {
		raw := dbStripText(dbFirstSubmatch(dbTitleFallbackRE, s))
		// Douban appends " (豆瓣)" (Chinese parens) on every book page.
		raw = strings.TrimSuffix(raw, " (豆瓣)")
		title = strings.TrimSpace(raw)
	}
	if title == "" {
		return nil
	}

	c := &metadata.Candidate{Title: title}

	// Info block: isolate the <div id="info"> contents and parse the
	// labelled lines from the tag-stripped flat text.
	infoRaw := dbFirstSubmatch(dbInfoBlockRE, s)

	// Authors — prefer the anchor-list shape (gives clean per-author
	// splits); fall back to the plain-text label.
	if am := dbAuthorAnchorRE.FindStringSubmatch(infoRaw); len(am) >= 2 {
		anchors := dbAnchorTextRE.FindAllStringSubmatch(am[1], -1)
		authors := make([]string, 0, len(anchors))
		seen := make(map[string]bool, len(anchors))
		for _, a := range anchors {
			name := dbStripText(a[1])
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

	// Split the info block on <br /> boundaries — Douban renders one
	// label per <br />-terminated line. We can't use dbStripText on
	// the whole block because that collapses whitespace globally and
	// erases the per-label boundary the line-anchored regexes rely on.
	infoLines := dbInfoLinesFrom(infoRaw)

	if c.Authors == nil {
		for _, line := range infoLines {
			if m := dbInfoAuthorRE.FindStringSubmatch(line); len(m) >= 2 {
				// Split on " / " (Douban's separator) or " " for short lists.
				parts := strings.Split(m[1], "/")
				authors := make([]string, 0, len(parts))
				seen := make(map[string]bool, len(parts))
				for _, p := range parts {
					name := strings.TrimSpace(p)
					if name == "" || seen[name] {
						continue
					}
					seen[name] = true
					authors = append(authors, name)
				}
				if len(authors) > 0 {
					c.Authors = authors
				}
				break
			}
		}
	}

	for _, line := range infoLines {
		if c.Publisher == "" {
			if m := dbInfoPublisherRE.FindStringSubmatch(line); len(m) >= 2 {
				c.Publisher = strings.TrimSpace(m[1])
			}
		}
		if c.PublishedAt == "" {
			if m := dbInfoYearRE.FindStringSubmatch(line); len(m) >= 2 {
				if y := dbYearExtractRE.FindStringSubmatch(m[1]); len(y) >= 2 {
					c.PublishedAt = y[1]
				}
			}
		}
		if c.ISBN == "" {
			if m := dbInfoISBNRE.FindStringSubmatch(line); len(m) >= 2 {
				c.ISBN = strings.ReplaceAll(m[1], "-", "")
			}
		}
		if c.Series == "" {
			if m := dbInfoSeriesRE.FindStringSubmatch(line); len(m) >= 2 {
				c.Series = strings.TrimSpace(m[1])
			}
		}
	}

	// Description — all-hidden variant preferred, fall back to intro.
	if desc := dbStripText(dbFirstSubmatch(dbDescRE, s)); desc != "" {
		c.Description = desc
	} else if desc := dbStripText(dbFirstSubmatch(dbDescFallbackRE, s)); desc != "" {
		c.Description = desc
	}

	// Cover URL.
	if cover := dbFirstSubmatch(dbCoverRE, s); cover != "" {
		c.CoverURL = cover
	} else if cover := dbFirstSubmatch(dbCoverFallbackRE, s); cover != "" {
		c.CoverURL = cover
	}

	return c
}

// dbInfoLinesFrom splits the raw <div id="info"> HTML into one line
// per Douban label. The page uses <br /> (or <br>) between labels;
// splitting on those tags and then stripping inner HTML gives clean
// `作者: …`-style lines the per-label regexes can match line-anchored.
func dbInfoLinesFrom(infoRaw string) []string {
	if infoRaw == "" {
		return nil
	}
	// Normalise <br/> | <br /> | <br> all into one delimiter.
	delim := "__DB_BR__"
	br := regexp.MustCompile(`(?i)<br\s*/?>`)
	flat := br.ReplaceAllString(infoRaw, delim)
	parts := strings.Split(flat, delim)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		line := dbStripText(p)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Local helpers (db-prefixed)
// ---------------------------------------------------------------------------

// dbFirstSubmatch returns submatch[1] or "" if the regex did not match.
func dbFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// dbStripText flattens HTML tags from a fragment, decodes the named
// and numeric HTML entities Douban emits, and collapses whitespace.
// Safe for UTF-8 Chinese content: the tag-strip + entity-decode
// operations don't slice on character boundaries (tag/entity regex
// only matches ASCII-only metacharacters).
func dbStripText(s string) string {
	if s == "" {
		return ""
	}
	s = dbTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = dbNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := dbNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	s = dbWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
