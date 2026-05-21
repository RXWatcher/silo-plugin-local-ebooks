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

const annasArchiveID = "annasarchive"
const annasArchiveBaseURL = "https://annas-archive.org"

// aaMD5RE matches Anna's Archive's primary identifier: a 32-char lowercase
// hex string. Get requires this shape; non-matching input returns (nil, nil).
var aaMD5RE = regexp.MustCompile(`^[a-f0-9]{32}$`)

// aaEbookExtensions is the whitelist of file formats this source treats as
// books. Anna's Archive also indexes audiobook formats (.m4b, .mp3) which we
// deliberately filter out — this plugin's purpose is ebook metadata.
// Matches booklore-ng's FILE_EXTENSIONS list.
var aaEbookExtensions = map[string]bool{
	"epub": true, "pdf": true, "mobi": true, "azw3": true,
	"cbr": true, "cbz": true, "fb2": true, "djvu": true, "txt": true,
}

// aaTrRowRE splits the table-style search page into <tr>…</tr> blocks.
// We parse each row independently with the field-specific regexes below.
var aaTrRowRE = regexp.MustCompile(`(?is)<tr[^>]*>.*?</tr>`)

// aaMD5HrefRE extracts the MD5 from any /md5/<hex> link inside a row.
// MD5 is the primary key on Anna's Archive; rows without one are unusable.
var aaMD5HrefRE = regexp.MustCompile(`href="/md5/([a-f0-9]{32})"`)

// aaSearchImgRE extracts the first <img src="…"> URL in a row (cover).
var aaSearchImgRE = regexp.MustCompile(`(?is)<img[^>]*src="([^"]+)"`)

// aaSearchTitleRE extracts the title from the MD5 anchor's inner span.
var aaSearchTitleRE = regexp.MustCompile(`(?is)<a[^>]*href="/md5/[^"]*"[^>]*>[\s\S]*?<span[^>]*>([^<]+)</span>`)

// aaSearchTitleAltRE is a fallback for the "js-vim-focus" alt structure
// Anna's Archive sometimes emits for the title text.
var aaSearchTitleAltRE = regexp.MustCompile(`(?is)class="[^"]*js-vim-focus[^"]*"[^>]*>([^<]+)<`)

// aaSearchAuthorRE captures the author from a search?q=author:… link.
var aaSearchAuthorRE = regexp.MustCompile(`(?is)search\?q=author:[^"]*"[^>]*><span[^>]*>([^<]+)</span>`)

// aaSearchPublisherRE captures the publisher from a publisher:… link.
var aaSearchPublisherRE = regexp.MustCompile(`(?is)publisher:[^"]*"[^>]*><span[^>]*>([^<]+)</span>`)

// aaYearRE matches the first 4-digit year (1900s/2000s) anywhere in a row.
var aaYearRE = regexp.MustCompile(`\b(19|20)\d{2}\b`)

// aaISBN13RE matches a 13-digit ISBN-13.
var aaISBN13RE = regexp.MustCompile(`\b(97[89]\d{10})\b`)

// aaISBN10RE matches a 10-character ISBN-10 (9 digits + digit or X).
var aaISBN10RE = regexp.MustCompile(`\b(\d{9}[\dXx])\b`)

// aaFileFormatRE matches a known book/audio extension; lowercase the match
// before consulting aaEbookExtensions to decide whether to keep the row.
var aaFileFormatRE = regexp.MustCompile(`(?i)\b(epub|pdf|mobi|azw3|cbr|cbz|fb2|djvu|txt|mp3|m4b)\b`)

// aaLangCodeRE matches the [xx] 2-letter ISO language code in brackets.
var aaLangCodeRE = regexp.MustCompile(`(?i)\[([a-z]{2})\]`)

// aaDetailH1RE pulls the title from the detail page's H1.
var aaDetailH1RE = regexp.MustCompile(`(?is)<h1[^>]*>([^<]+)</h1>`)

// aaDetailTitleTagRE is the fallback when the page has no H1.
var aaDetailTitleTagRE = regexp.MustCompile(`(?is)<title>([^<]+)</title>`)

// aaDetailCoverRE prefers the <img alt="cover"> selector, which is stable
// across Anna's Archive's layouts.
var aaDetailCoverRE = regexp.MustCompile(`(?is)<img[^>]*src="([^"]+)"[^>]*alt="cover"`)

// aaDetailAuthorRE finds the "Author:" label followed by linked text.
var aaDetailAuthorRE = regexp.MustCompile(`(?is)Author[s]?:\s*<[^>]*>([^<]+)<`)

// aaDetailPublisherRE finds the "Publisher:" label followed by linked text.
var aaDetailPublisherRE = regexp.MustCompile(`(?is)Publisher:\s*<[^>]*>([^<]+)<`)

// aaDetailYearRE finds an explicit "Year:" label followed by a 4-digit year.
var aaDetailYearRE = regexp.MustCompile(`(?i)Year:\s*(\d{4})`)

// aaDetailISBN13RE finds the labelled "ISBN-13:" line.
var aaDetailISBN13RE = regexp.MustCompile(`(?i)ISBN-13:\s*(\d{13})`)

// aaDetailISBN10RE finds the labelled "ISBN-10:" line.
var aaDetailISBN10RE = regexp.MustCompile(`(?i)ISBN-10:\s*(\d{9}[\dXx])`)

// aaDetailLanguageRE captures the "Language:" label's value.
var aaDetailLanguageRE = regexp.MustCompile(`(?is)Language:\s*<[^>]*>([^<]+)<`)

// aaDetailPagesRE captures the "Pages:" or "Page:" count.
var aaDetailPagesRE = regexp.MustCompile(`(?i)Pages?:\s*(\d+)`)

// aaDetailDescRE captures the description div content.
var aaDetailDescRE = regexp.MustCompile(`(?is)<div[^>]*class="[^"]*description[^"]*"[^>]*>([\s\S]*?)</div>`)

// aaDetailExtensionRE captures the "Extension:" label's value (used by the
// format filter on the detail page). Case-insensitive.
var aaDetailExtensionRE = regexp.MustCompile(`(?i)Extension:\s*([a-z0-9]+)`)

// aaTagStripRE removes HTML tags from a captured fragment.
var aaTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// aaWSRE collapses runs of whitespace into single spaces.
var aaWSRE = regexp.MustCompile(`\s+`)

// aaNumEntityRE matches numeric decimal entities (e.g. &#39;, &#8217;).
var aaNumEntityRE = regexp.MustCompile(`&#(\d+);`)

// AnnasArchive is the Source impl for annas-archive.org (HTML scraping).
//
// Anna's Archive does not expose a JSON API; both search and detail
// endpoints serve HTML and are scraped with regex (mirroring booklore-ng's
// implementation). The MD5 hash of the file is the primary identifier.
//
// Scope: this implementation parses Anna's Archive's `?display=table`
// search layout and the per-MD5 detail page. The "block-style" search
// fallback that booklore-ng implements (for the non-table layout) is
// intentionally omitted; the table layout covers the common case and the
// extra parser path would double the surface area without proportional
// coverage gain. If table parsing returns zero rows, Search returns nil.
type AnnasArchive struct {
	http    *HTTPClient
	baseURL string
}

// NewAnnasArchive constructs the source with the production base URL.
func NewAnnasArchive(ua string) *AnnasArchive {
	return NewAnnasArchiveAt(annasArchiveBaseURL, ua)
}

// NewAnnasArchiveAt constructs the source against a custom base URL (tests).
func NewAnnasArchiveAt(baseURL, ua string) *AnnasArchive {
	return &AnnasArchive{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (a *AnnasArchive) ID() string                       { return annasArchiveID }
func (a *AnnasArchive) Enabled(cfg map[string]bool) bool { return cfg[annasArchiveID] }

// Get fetches a single book by MD5. Input that is not a 32-char lowercase
// hex string returns (nil, nil). 404 surfaces as ErrNotFound. A parsed page
// whose Extension is a non-ebook format (e.g. m4b audiobook) also returns
// ErrNotFound — the user explicitly looked up this MD5 and the same
// "not a book per our definition" signal is appropriate as a 404.
func (a *AnnasArchive) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(strings.ToLower(id))
	if !aaMD5RE.MatchString(id) {
		return nil, nil
	}

	bookURL := fmt.Sprintf("%s/md5/%s", a.baseURL, id)
	body, err := a.http.GetJSON(ctx, bookURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c, ext := parseAnnasArchiveDetailPage(body, a.baseURL)
	if c == nil {
		return nil, ErrNotFound
	}
	// Format filter: if the detail page lists an Extension and it's not
	// a recognized ebook format, treat as not-found (see type doc).
	if ext != "" && !aaEbookExtensions[ext] {
		return nil, ErrNotFound
	}

	c.ExternalID = id
	c.Source = annasArchiveID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search queries Anna's Archive for books matching the given text. Returns
// nil on empty query or zero parsed rows. Rows with a detected non-ebook
// extension (e.g. mp3, m4b) are dropped; rows with no detected extension
// are kept (absence is not evidence).
func (a *AnnasArchive) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}

	searchURL := fmt.Sprintf("%s/search?q=%s&display=table", a.baseURL, encodeQuery(q))
	body, err := a.http.GetJSON(ctx, searchURL)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows := parseAnnasArchiveSearchPage(body, a.baseURL)
	if len(rows) == 0 {
		return nil, nil
	}
	out := make([]metadata.Candidate, 0, len(rows))
	for i := range rows {
		rows[i].Source = annasArchiveID
		rows[i].Region = region
		rows[i].Raw = json.RawMessage(body)
		out = append(out, rows[i])
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Search page parser
// ---------------------------------------------------------------------------

// parseAnnasArchiveSearchPage extracts Candidates from the table-layout
// search results. Rows without an MD5 link, with a title shorter than 2
// chars, or with a non-ebook file extension are dropped.
func parseAnnasArchiveSearchPage(html []byte, base string) []metadata.Candidate {
	s := string(html)

	rows := aaTrRowRE.FindAllString(s, -1)
	if len(rows) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(rows))
	out := make([]metadata.Candidate, 0, len(rows))
	for _, row := range rows {
		md5m := aaMD5HrefRE.FindStringSubmatch(row)
		if len(md5m) < 2 {
			continue
		}
		md5 := strings.ToLower(md5m[1])
		if seen[md5] {
			continue
		}

		// Title — primary, then alt selector.
		title := aaStripText(aaFirstSubmatch(aaSearchTitleRE, row))
		if title == "" {
			title = aaStripText(aaFirstSubmatch(aaSearchTitleAltRE, row))
		}
		if len(title) < 2 {
			continue
		}

		// Format filter: drop rows whose detected extension is not an
		// ebook format. Absence of an extension is NOT a filter reason.
		var fileFormat string
		if m := aaFileFormatRE.FindStringSubmatch(row); len(m) >= 2 {
			fileFormat = strings.ToLower(m[1])
			if !aaEbookExtensions[fileFormat] {
				continue
			}
		}

		seen[md5] = true

		c := metadata.Candidate{
			ExternalID: md5,
			Title:      title,
		}

		// Author — skip "unknown" sentinel.
		if author := aaStripText(aaFirstSubmatch(aaSearchAuthorRE, row)); author != "" && !strings.EqualFold(author, "unknown") {
			c.Authors = []string{author}
		}

		// Publisher.
		if pub := aaStripText(aaFirstSubmatch(aaSearchPublisherRE, row)); pub != "" {
			c.Publisher = pub
		}

		// Year — Anna's gives year only; store as 4-char string.
		if m := aaYearRE.FindString(row); m != "" {
			c.PublishedAt = m
		}

		// ISBN-13 preferred; ISBN-10 fallback.
		if m := aaISBN13RE.FindStringSubmatch(row); len(m) >= 2 {
			c.ISBN = m[1]
		} else if m := aaISBN10RE.FindStringSubmatch(row); len(m) >= 2 {
			c.ISBN = m[1]
		}

		// Language — 2-letter code, lowercase.
		if m := aaLangCodeRE.FindStringSubmatch(row); len(m) >= 2 {
			c.Language = strings.ToLower(m[1])
		}

		// Cover — resolve protocol-relative or root-relative URLs.
		if cover := aaFirstSubmatch(aaSearchImgRE, row); cover != "" {
			c.CoverURL = aaResolveURL(cover, base)
		}

		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Detail page parser
// ---------------------------------------------------------------------------

// parseAnnasArchiveDetailPage extracts a Candidate from a /md5/<hex> page.
// Returns (nil, "") when the page has no usable title. The second return is
// the lowercased Extension value (if any) so the caller can apply the
// ebook-format filter.
func parseAnnasArchiveDetailPage(html []byte, base string) (*metadata.Candidate, string) {
	s := string(html)

	title := aaStripText(aaFirstSubmatch(aaDetailH1RE, s))
	if title == "" {
		raw := aaStripText(aaFirstSubmatch(aaDetailTitleTagRE, s))
		// Trim site suffix after — or | separators.
		if idx := strings.IndexAny(raw, "-|"); idx > 0 {
			raw = strings.TrimSpace(raw[:idx])
		}
		title = raw
	}
	if title == "" {
		return nil, ""
	}

	c := &metadata.Candidate{Title: title}

	// Cover.
	if cover := aaFirstSubmatch(aaDetailCoverRE, s); cover != "" {
		c.CoverURL = aaResolveURL(cover, base)
	}

	// Author.
	if author := aaStripText(aaFirstSubmatch(aaDetailAuthorRE, s)); author != "" && !strings.EqualFold(author, "unknown") {
		c.Authors = []string{author}
	}

	// Publisher.
	if pub := aaStripText(aaFirstSubmatch(aaDetailPublisherRE, s)); pub != "" {
		c.Publisher = pub
	}

	// Year — labelled, fall back to first 4-digit year anywhere.
	if m := aaDetailYearRE.FindStringSubmatch(s); len(m) >= 2 {
		c.PublishedAt = m[1]
	} else if m := aaYearRE.FindString(s); m != "" {
		c.PublishedAt = m
	}

	// ISBN-13 preferred; ISBN-10 fallback. Each has a labelled and bare form.
	if m := aaDetailISBN13RE.FindStringSubmatch(s); len(m) >= 2 {
		c.ISBN = m[1]
	} else if m := aaISBN13RE.FindStringSubmatch(s); len(m) >= 2 {
		c.ISBN = m[1]
	} else if m := aaDetailISBN10RE.FindStringSubmatch(s); len(m) >= 2 {
		c.ISBN = m[1]
	} else if m := aaISBN10RE.FindStringSubmatch(s); len(m) >= 2 {
		c.ISBN = m[1]
	}

	// Language — labelled value first, bracketed code fallback.
	if lang := aaStripText(aaFirstSubmatch(aaDetailLanguageRE, s)); lang != "" {
		lang = strings.ToLower(lang)
		if len(lang) > 2 {
			lang = lang[:2]
		}
		c.Language = lang
	} else if m := aaLangCodeRE.FindStringSubmatch(s); len(m) >= 2 {
		c.Language = strings.ToLower(m[1])
	}

	// PageCount.
	if m := aaDetailPagesRE.FindStringSubmatch(s); len(m) >= 2 {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			c.PageCount = n
		}
	}

	// Description — strip nested tags.
	if desc := aaStripText(aaFirstSubmatch(aaDetailDescRE, s)); desc != "" {
		c.Description = desc
	}

	// Extension — returned to caller for the ebook-format filter.
	var ext string
	if m := aaDetailExtensionRE.FindStringSubmatch(s); len(m) >= 2 {
		ext = strings.ToLower(m[1])
	}

	return c, ext
}

// ---------------------------------------------------------------------------
// Local helpers (aa-prefixed)
// ---------------------------------------------------------------------------

// aaFirstSubmatch returns submatch[1] or "" if the regex did not match.
func aaFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// aaResolveURL turns a protocol-relative ("//host/…") or root-relative
// ("/path") URL into an absolute URL against base. Already-absolute URLs
// are returned verbatim.
func aaResolveURL(u, base string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	if strings.HasPrefix(u, "//") {
		// Inherit base scheme (assume https if base is empty).
		if strings.HasPrefix(base, "http://") {
			return "http:" + u
		}
		return "https:" + u
	}
	if strings.HasPrefix(u, "/") {
		return strings.TrimRight(base, "/") + u
	}
	return u
}

// aaStripText flattens HTML tags from a fragment, decodes the handful of
// HTML entities Anna's Archive emits (including numeric decimal entities
// like &#39; and &#8217;), and collapses whitespace. Mirrors booklore-ng's
// decoder scope: no &#xNN; hex, no recursive decoding.
func aaStripText(s string) string {
	if s == "" {
		return ""
	}
	s = aaTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	// Numeric decimal entities (after the named ones, so &#39; is already
	// handled by the literal replacer for speed; any remaining &#NNN; falls
	// through to here).
	s = aaNumEntityRE.ReplaceAllStringFunc(s, func(m string) string {
		sm := aaNumEntityRE.FindStringSubmatch(m)
		if len(sm) < 2 {
			return m
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil || n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
	s = aaWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
