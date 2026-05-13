package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ContinuumApp/continuum-plugin-ebooksdb/internal/metadata"
)

const amazonID = "amazon"
const amazonBaseURL = "https://www.amazon.com"

// asinRE matches Amazon's 10-character ASIN format: uppercase letters or
// digits. ISBN-10 (10 digits) satisfies this; ISBN-13 (13 digits) does not.
var asinRE = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// amTitleRE extracts the text inside <span id="productTitle">...</span>.
var amTitleRE = regexp.MustCompile(`(?is)<span[^>]*\bid="productTitle"[^>]*>([^<]+)</span>`)

// amAuthorsBlockRE locates the byline block that contains author links.
// We then walk amAuthorLinkRE over the captured fragment.
var amAuthorsBlockRE = regexp.MustCompile(`(?is)<span[^>]*\bclass="[^"]*\bauthor\b[^"]*"[^>]*>([\s\S]*?)</span>`)

// amAuthorLinkRE pulls each <a class="a-link-normal">Name</a> within the byline.
var amAuthorLinkRE = regexp.MustCompile(`(?is)<a[^>]*\bclass="[^"]*\ba-link-normal\b[^"]*"[^>]*>([^<]+)</a>`)

// amDescNoscriptRE prefers the noscript-wrapped description (clean text).
var amDescNoscriptRE = regexp.MustCompile(`(?is)<div[^>]*\bid="bookDescription_feature_div"[^>]*>[\s\S]*?<noscript>([\s\S]*?)</noscript>`)

// amDescSpanRE falls back to the visible span within bookDescription_feature_div.
var amDescSpanRE = regexp.MustCompile(`(?is)<div[^>]*\bid="bookDescription_feature_div"[^>]*>[\s\S]*?<span[^>]*>([\s\S]*?)</span>`)

// amCoverREs is the ordered list of selectors we try for the cover image.
var amCoverREs = []*regexp.Regexp{
	regexp.MustCompile(`(?is)<img[^>]*\bid="imgBlkFront"[^>]*\bsrc="([^"]+)"`),
	regexp.MustCompile(`(?is)<img[^>]*\bid="ebooksImgBlkFront"[^>]*\bsrc="([^"]+)"`),
	regexp.MustCompile(`(?is)<img[^>]*\bid="main-image"[^>]*\bsrc="([^"]+)"`),
}

// amDetailLiRE captures each <li> inside the detail-bullet-list or
// detailBullets_feature_div container.
var amDetailLiRE = regexp.MustCompile(`(?is)<li\b[^>]*>([\s\S]*?)</li>`)

// amPublisherRE — captures publisher name (before the trailing date paren).
var amPublisherRE = regexp.MustCompile(`(?i)Publisher[:\s]+(.+?)\s*\(`)

// amPubDateRE — captures the date inside the trailing parens.
var amPubDateRE = regexp.MustCompile(`\(([^)]+)\)`)

// amLanguageRE — single-line language extraction.
var amLanguageRE = regexp.MustCompile(`(?i)Language[:\s]+(.+)`)

// amPagesRE — matches "NNN pages" anywhere in the detail bullet.
var amPagesRE = regexp.MustCompile(`(\d+)\s+pages`)

// amISBN10RE — captures the 10-digit ISBN-10.
var amISBN10RE = regexp.MustCompile(`(?i)ISBN-10[:\s]+(\d{10})`)

// amISBN13RE — captures the ISBN-13 (digits with optional hyphens).
var amISBN13RE = regexp.MustCompile(`(?i)ISBN-13[:\s]+([\d-]+)`)

// amTagStripRE removes any HTML tag — used to flatten captured fragments.
var amTagStripRE = regexp.MustCompile(`(?is)<[^>]+>`)

// amWSRE collapses runs of whitespace.
var amWSRE = regexp.MustCompile(`\s+`)

// Amazon is the Source impl for amazon.com (HTML scraping by ASIN).
//
// Amazon is hostile to scraping; this source is best-effort. Search is
// intentionally limited: queries that are not ASIN-shaped return (nil, nil)
// rather than attempting to parse the search-results page, which changes
// often. Callers needing text discovery should rely on other sources and
// hand off ASIN to Amazon afterwards.
type Amazon struct {
	http    *HTTPClient
	baseURL string
}

// NewAmazon constructs the source with the production base URL.
func NewAmazon(ua string) *Amazon {
	return NewAmazonAt(amazonBaseURL, ua)
}

// NewAmazonAt constructs the source against a custom base URL (tests).
func NewAmazonAt(baseURL, ua string) *Amazon {
	return &Amazon{
		http:    NewHTTPClient(baseURL, ua),
		baseURL: baseURL,
	}
}

func (a *Amazon) ID() string                       { return amazonID }
func (a *Amazon) Enabled(cfg map[string]bool) bool { return cfg[amazonID] }

// amazonHostFor returns the host root for the given region.
// Test override: if baseURL is not the production URL, return it verbatim
// (mirrors the storytel pattern in audiobooksdb).
func (a *Amazon) amazonHostFor(region string) string {
	if a.baseURL != amazonBaseURL {
		return a.baseURL
	}
	switch strings.ToLower(region) {
	case "us", "":
		return "https://www.amazon.com"
	case "uk":
		return "https://www.amazon.co.uk"
	case "de":
		return "https://www.amazon.de"
	case "fr":
		return "https://www.amazon.fr"
	case "jp":
		return "https://www.amazon.co.jp"
	case "ca":
		return "https://www.amazon.ca"
	case "au":
		return "https://www.amazon.com.au"
	case "es":
		return "https://www.amazon.es"
	case "it":
		return "https://www.amazon.it"
	case "nl":
		return "https://www.amazon.nl"
	case "mx":
		return "https://www.amazon.com.mx"
	case "br":
		return "https://www.amazon.com.br"
	case "in":
		return "https://www.amazon.in"
	default:
		return "https://www.amazon." + region
	}
}

// Get fetches a single book by ASIN. Non-ASIN input returns (nil, nil).
func (a *Amazon) Get(ctx context.Context, id, region string) (*metadata.Candidate, error) {
	id = strings.TrimSpace(id)
	if !asinRE.MatchString(id) {
		return nil, nil
	}

	host := a.amazonHostFor(region)
	bookURL := fmt.Sprintf("%s/dp/%s", host, id)
	body, err := a.http.GetJSON(ctx, bookURL)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	c := parseAmazonProductPage(body)
	if c == nil {
		return nil, ErrNotFound
	}
	c.ASIN = id
	if c.ExternalID == "" {
		c.ExternalID = id
	}
	c.Source = amazonID
	c.Region = region
	c.Raw = json.RawMessage(body)
	return c, nil
}

// Search returns Amazon results for the query. For non-ASIN queries this
// returns (nil, nil) by design: parsing Amazon's search-results HTML is
// brittle enough that we prefer to let other sources handle text discovery
// and use Amazon only for ASIN lookup. ASIN-shaped queries delegate to Get.
func (a *Amazon) Search(ctx context.Context, query, region string) ([]metadata.Candidate, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	if !asinRE.MatchString(q) {
		return nil, nil
	}
	c, err := a.Get(ctx, q, region)
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

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// parseAmazonProductPage extracts a Candidate from an Amazon product page.
// Returns nil when the page is unparseable (no recognizable title).
func parseAmazonProductPage(html []byte) *metadata.Candidate {
	s := string(html)

	title := amStripText(amFirstSubmatch(amTitleRE, s))
	if title == "" {
		return nil
	}

	c := &metadata.Candidate{Title: title}

	// Authors — walk the byline block and pick a-link-normal entries that
	// don't look like a role label such as "(Author)".
	if block := amFirstSubmatch(amAuthorsBlockRE, s); block != "" {
		for _, m := range amAuthorLinkRE.FindAllStringSubmatch(block, -1) {
			if len(m) < 2 {
				continue
			}
			name := amStripText(m[1])
			if name == "" || strings.Contains(name, "(") {
				continue
			}
			c.Authors = append(c.Authors, name)
		}
	}

	// Description — noscript first, then visible span.
	if d := amStripText(amFirstSubmatch(amDescNoscriptRE, s)); d != "" {
		c.Description = d
	} else if d := amStripText(amFirstSubmatch(amDescSpanRE, s)); d != "" {
		c.Description = d
	}

	// Cover — first selector that yields a value.
	for _, re := range amCoverREs {
		if u := strings.TrimSpace(amFirstSubmatch(re, s)); u != "" {
			c.CoverURL = u
			break
		}
	}

	// Detail bullets — iterate li elements and apply each field regex.
	for _, m := range amDetailLiRE.FindAllStringSubmatch(s, -1) {
		if len(m) < 2 {
			continue
		}
		text := amStripText(m[1])
		if text == "" {
			continue
		}

		if strings.Contains(text, "Publisher") {
			if c.Publisher == "" {
				if sm := amPublisherRE.FindStringSubmatch(text); len(sm) >= 2 {
					c.Publisher = strings.TrimSpace(sm[1])
				}
			}
			if c.PublishedAt == "" {
				if sm := amPubDateRE.FindStringSubmatch(text); len(sm) >= 2 {
					c.PublishedAt = strings.TrimSpace(sm[1])
				}
			}
		}

		if c.Language == "" && strings.Contains(text, "Language") {
			if sm := amLanguageRE.FindStringSubmatch(text); len(sm) >= 2 {
				c.Language = strings.TrimSpace(sm[1])
			}
		}

		if c.PageCount == 0 && (strings.Contains(text, "Paperback") || strings.Contains(text, "Hardcover") || strings.Contains(text, "pages")) {
			if sm := amPagesRE.FindStringSubmatch(text); len(sm) >= 2 {
				if n, err := amParseInt(sm[1]); err == nil && n > 0 {
					c.PageCount = n
				}
			}
		}

		if strings.Contains(text, "ISBN-13") {
			if sm := amISBN13RE.FindStringSubmatch(text); len(sm) >= 2 {
				isbn := strings.ReplaceAll(sm[1], "-", "")
				if isbn != "" {
					c.ISBN = isbn
				}
			}
		} else if c.ISBN == "" && strings.Contains(text, "ISBN-10") {
			if sm := amISBN10RE.FindStringSubmatch(text); len(sm) >= 2 {
				c.ISBN = sm[1]
			}
		}
	}

	return c
}

// ---------------------------------------------------------------------------
// Local helpers (am-prefixed)
// ---------------------------------------------------------------------------

// amFirstSubmatch returns submatch[1] or "" if the regex did not match.
func amFirstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// amStripText flattens HTML tags from a fragment, decodes the handful of
// HTML entities Amazon's product pages commonly emit, and collapses
// whitespace. It is deliberately not a full entity decoder — we only care
// about ones that affect downstream regex matching (notably &nbsp;, which
// Amazon uses as the separator between detail-bullet labels and values).
func amStripText(s string) string {
	if s == "" {
		return ""
	}
	s = amTagStripRE.ReplaceAllString(s, " ")
	s = strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&quot;", `"`,
		"&apos;", "'",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	).Replace(s)
	s = amWSRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// amParseInt is a tiny strconv.Atoi wrapper that keeps the parser readable.
func amParseInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}
