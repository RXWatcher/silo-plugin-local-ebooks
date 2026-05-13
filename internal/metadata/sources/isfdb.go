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

const isfdbID = "isfdb"
const isfdbBaseURL = "https://www.isfdb.org"

// isMaxResults caps the number of <tr> rows emitted per search. Matches
// booklore-ng's `.slice(0, 10)` ceiling.
const isMaxResults = 10

// isNumericIDRE validates the input to Get: ISFDB title IDs are bare
// integers (e.g. 1655 for "Dune"). Non-numeric input → (nil, nil).
var isNumericIDRE = regexp.MustCompile(`^\d+$`)

// isRowRE splits the search results page into <tr>…</tr> blocks. Each row
// is parsed independently with the field-specific regexes below.
var isRowRE = regexp.MustCompile(`(?is)<tr[^>]*>.*?</tr>`)

// isTitleLinkRE captures the numeric title ID (group 1) and the title text
// (group 2) from a /cgi-bin/title.cgi?<id> anchor. ISFDB's canonical
// per-title identifier is the integer after the `?`.
var isTitleLinkRE = regexp.MustCompile(`(?i)<a\s+href="[^"]*/title\.cgi\?(\d+)"[^>]*>([^<]+)</a>`)

// isAuthorLinkRE captures an author name from any /cgi-bin/ea.cgi?<id>
// anchor on either the search row or the detail page. Multiple authors
// per page are common, so callers use FindAllStringSubmatch.
var isAuthorLinkRE = regexp.MustCompile(`(?i)<a\s+href="[^"]*/ea\.cgi\?\d+"[^>]*>([^<]+)</a>`)

// isYearParenRE captures the first parenthesised 4-digit year in a search
// row, e.g. "(1965)". Stored verbatim in Candidate.PublishedAt.
var isYearParenRE = regexp.MustCompile(`\((\d{4})\)`)

// isDetailTitleRE captures the title from the detail page's content
// header. Preferred over the <title> fallback because the page <title>
// also contains the " - ISFDB" suffix.
var isDetailTitleRE = regexp.MustCompile(`(?is)<div class="contentheader">\s*<h1>([^<]+)</h1>`)

// isDetailFallbackTitleRE matches the page's <title> tag; callers strip
// the " - ISFDB" suffix before use.
var isDetailFallbackTitleRE = regexp.MustCompile(`(?i)<title>([^<]+) - ISFDB</title>`)

// isDetailDateRE captures the 4-digit year on the "Date:" label.
var isDetailDateRE = regexp.MustCompile(`(?i)Date:\s*</b>\s*(\d{4})`)

// isDetailYearRE captures the 4-digit year on the "Year:" label (fallback
// for title-rather-than-publication pages).
var isDetailYearRE = regexp.MustCompile(`(?i)Year:\s*</b>\s*(\d{4})`)

// isDetailPublisherRE captures the publisher text inside the "Publisher:"
// label's anchor.
var isDetailPublisherRE = regexp.MustCompile(`(?i)Publisher:\s*</b>\s*<a[^>]*>([^<]+)</a>`)

// isDetailPagesRE captures the integer page count on the "Pages:" label.
var isDetailPagesRE = regexp.MustCompile(`(?i)Pages:\s*</b>\s*(\d+)`)

// isDetailISBNRE captures the raw ISBN string on the "ISBN:" label;
// callers strip hyphens before storing.
var isDetailISBNRE = regexp.MustCompile(`(?i)ISBN:\s*</b>\s*([\d-X]+)`)

// isDetailSeriesRE captures the series name inside the "Series:" label's
// anchor.
var isDetailSeriesRE = regexp.MustCompile(`(?i)Series:\s*</b>\s*<a[^>]*>([^<]+)</a>`)

// isDetailSeriesNumRE captures the integer series position on the
// "Series Number:" label.
var isDetailSeriesNumRE = regexp.MustCompile(`(?i)Series Number:\s*</b>\s*(\d+)`)

// isDetailCoverRE prefers the <img class="…scan…"> selector, which is the
// canonical cover image on ISFDB title pages.
var isDetailCoverRE = regexp.MustCompile(`(?i)<img[^>]+src="([^"]+)"[^>]*class="[^"]*scan[^"]*"`)

// isDetailCoverFallbackRE matches any /images/ asset as a last resort.
var isDetailCoverFallbackRE = regexp.MustCompile(`(?i)<img[^>]+src="(https?://[^"]+/images/[^"]+)"`)

// isTagStripRE removes HTML tags from a captured fragment.
var isTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// isWSRE collapses runs of whitespace into single spaces.
var isWSRE = regexp.MustCompile(`\s+`)

// isNumEntityRE matches numeric decimal entities (e.g. &#39;, &#8217;).
var isNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// ISFDB is the Source impl for isfdb.org (HTML scraping).
//
// ISFDB (Internet Speculative Fiction Database) is a community-maintained
// catalog of science fiction, fantasy, and horror titles. It has no public
// JSON API; the CGI endpoints serve HTML and are scraped with regex
// (mirroring booklore-ng's implementation). Per-title IDs are stable
// integers reachable at /cgi-bin/title.cgi?<id>.
//
// Plan deviation: the implementation plan called out an XML/SOAP API
// (getpub_by_ISBN.cgi). Booklore-ng's reference does NOT use it; it
// scrapes the HTML CGI pages directly. We mirror booklore-ng.
//
// Scope decisions that deviate from booklore-ng:
//
//   - The hardcoded `genres: ['Science Fiction', 'Fantasy']` literal that
//     booklore-ng adds to every candidate is dropped. That's site-flavor
//     noise, not real per-book metadata.
//
//   - The hardcoded `tags: [<bookType>]` field is dropped: our Candidate
//     type has no Tags field and the NOVEL/COLLECTION/ANTHOLOGY taxonomy
//     doesn't map cleanly onto any existing field.
type ISFDB struct {
	http    *HTTPClient
	baseURL string
}

// NewISFDB constructs the source with the production base URL.
func NewISFDB(ua string) *ISFDB {
	return NewISFDBAt(isfdbBaseURL, ua)
}

// NewISFDBAt constructs the source against a custom base URL (tests).
func NewISFDBAt(baseURL, ua string) *ISFDB {
	return &ISFDB{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (i *ISFDB) ID() string                       { return isfdbID }
func (i *ISFDB) Enabled(cfg map[string]bool) bool { return cfg[isfdbID] }

// Get fetches a single title by its numeric ISFDB title ID. Input that is
// not a bare integer returns (nil, nil) without a network call. 404
// surfaces as ErrNotFound. A parsed page with no extractable title also
// surfaces as ErrNotFound — that case means the response was 200 but not
// a valid title page (e.g. a redirect landing page).
func (i *ISFDB) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if !isNumericIDRE.MatchString(id) {
		return nil, nil
	}

	titleURL := fmt.Sprintf("%s/cgi-bin/title.cgi?%s", i.baseURL, id)
	body, err := i.http.GetJSON(ctx, titleURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseISFDBTitlePage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	c.ExternalID = id
	c.Source = isfdbID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries the ISFDB title-search CGI endpoint and parses the HTML
// table of results. Returns (nil, nil) for an empty query, a 404 response,
// or a page with no parseable rows. Capped at isMaxResults rows.
func (i *ISFDB) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	// `type=All+Titles` is the title-search variant. The CGI endpoint
	// also accepts authors and publications; we want title rows.
	searchURL := fmt.Sprintf("%s/cgi-bin/se.cgi?arg=%s&type=All+Titles", i.baseURL, encodeQuery(q))
	body, err := i.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows := parseISFDBSearchResults(body)
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(rows))
	for j := range rows {
		rows[j].Source = isfdbID
		rows[j].Region = region
		rows[j].Raw = json.RawMessage(body)
		out = append(out, rows[j])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseISFDBSearchResults extracts Candidates from the table of search
// results. Rows without a title link are skipped. ExternalID is the
// numeric title ID captured from the title.cgi?<id> anchor.
func parseISFDBSearchResults(html []byte) []metadata.Candidate {
	s := string(html)

	rows := isRowRE.FindAllString(s, -1)
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > isMaxResults {
		rows = rows[:isMaxResults]
	}

	out := make([]metadata.Candidate, 0, len(rows))
	for _, row := range rows {
		tm := isTitleLinkRE.FindStringSubmatch(row)
		if len(tm) < 3 {
			continue
		}
		title := isStripText(tm[2])
		if title == "" {
			continue
		}

		c := metadata.Candidate{
			ExternalID: tm[1],
			Title:      title,
		}

		// Authors: multiple anchors per row are possible.
		if am := isAuthorLinkRE.FindAllStringSubmatch(row, -1); len(am) > 0 {
			authors := make([]string, 0, len(am))
			for _, a := range am {
				if name := isStripText(a[1]); name != "" {
					authors = append(authors, name)
				}
			}
			if len(authors) > 0 {
				c.Authors = authors
			}
		}

		if ym := isYearParenRE.FindStringSubmatch(row); len(ym) >= 2 {
			c.PublishedAt = ym[1]
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Detail page parser
// ---------------------------------------------------------------------------

// parseISFDBTitlePage extracts a Candidate from a /cgi-bin/title.cgi page.
// Returns nil when no usable title can be extracted (caller surfaces
// ErrNotFound).
func parseISFDBTitlePage(html []byte) *metadata.Candidate {
	s := string(html)

	title := isStripText(isFirstSubmatch(isDetailTitleRE, s))
	if title == "" {
		// Fallback to <title> tag; strip the " - ISFDB" suffix that the
		// fallback regex captures via its second group structure.
		raw := isStripText(isFirstSubmatch(isDetailFallbackTitleRE, s))
		title = strings.TrimSuffix(raw, " - ISFDB")
		title = strings.TrimSpace(title)
	}
	if title == "" {
		return nil
	}

	c := &metadata.Candidate{Title: title}

	// Authors — dedupe first-wins. The detail page lists the same author
	// in both the header and the publication metadata block.
	if am := isAuthorLinkRE.FindAllStringSubmatch(s, -1); len(am) > 0 {
		seen := make(map[string]bool, len(am))
		authors := make([]string, 0, len(am))
		for _, a := range am {
			name := isStripText(a[1])
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

	// Year — "Date:" label preferred, "Year:" fallback.
	if m := isDetailDateRE.FindStringSubmatch(s); len(m) >= 2 {
		c.PublishedAt = m[1]
	} else if m := isDetailYearRE.FindStringSubmatch(s); len(m) >= 2 {
		c.PublishedAt = m[1]
	}

	// Publisher.
	if pub := isStripText(isFirstSubmatch(isDetailPublisherRE, s)); pub != "" {
		c.Publisher = pub
	}

	// PageCount.
	if m := isDetailPagesRE.FindStringSubmatch(s); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			c.PageCount = n
		}
	}

	// ISBN — strip hyphens to match the rest of the codebase's storage
	// convention (Anna's Archive, Goodreads, etc. all store hyphen-free).
	if m := isDetailISBNRE.FindStringSubmatch(s); len(m) >= 2 {
		c.ISBN = strings.ReplaceAll(m[1], "-", "")
	}

	// Series name.
	if series := isStripText(isFirstSubmatch(isDetailSeriesRE, s)); series != "" {
		c.Series = series
	}

	// Series position — stored as string (Candidate.SeriesPos is a string
	// because some sources use non-integer positions like "1.5").
	if m := isDetailSeriesNumRE.FindStringSubmatch(s); len(m) >= 2 {
		c.SeriesPos = m[1]
	}

	// Cover URL — class="…scan…" preferred, /images/ fallback.
	if cover := isFirstSubmatch(isDetailCoverRE, s); cover != "" {
		c.CoverURL = cover
	} else if cover := isFirstSubmatch(isDetailCoverFallbackRE, s); cover != "" {
		c.CoverURL = cover
	}

	return c
}

// ---------------------------------------------------------------------------
// Local helpers (is-prefixed)
// ---------------------------------------------------------------------------

// isFirstSubmatch returns submatch[1] or "" if the regex did not match.
func isFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// isStripText flattens HTML tags from a fragment, decodes the named and
// numeric HTML entities ISFDB emits, and collapses whitespace. Mirrors
// the aaStripText/ffStripText shape.
func isStripText(s string) string {
	if s == "" {
		return ""
	}
	s = isTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = isNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := isNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	s = isWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
