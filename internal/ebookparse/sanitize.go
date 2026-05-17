package ebookparse

import "strings"

// Parsers read UNTRUSTED ebook files. A hostile file can carry a 4-8 MiB
// "title", an attacker-chosen cover Content-Type (served verbatim by the
// cover endpoint → stored XSS), or thousands of genre entries. sanitize
// bounds every field before the result is persisted/served.
const (
	maxFieldBytes       = 4 << 10  // title/author/publisher/language/isbn/asin/series
	maxDescriptionBytes = 64 << 10 // description is allowed to be longer
	maxStoredCoverBytes = 8 << 20  // matches the sibling audiobook cap
	maxListItems        = 64       // authors / genres
)

// allowedCoverTypes is the only set of Content-Types the cover endpoint may
// echo back. Anything else (notably text/html) is rejected so a crafted
// cover cannot become stored XSS.
var allowedCoverTypes = map[string]bool{
	"image/jpeg": true,
	"image/jpg":  true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
	"image/avif": true,
}

func clampStr(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

func clampList(in []string) []string {
	if len(in) > maxListItems {
		in = in[:maxListItems]
	}
	for i := range in {
		in[i] = clampStr(in[i], maxFieldBytes)
	}
	return in
}

// normalizeCoverType lowercases and strips any "; charset=..." parameters.
func normalizeCoverType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// sanitize bounds every field of a freshly-parsed (untrusted) record.
func (p *Parsed) sanitize() {
	p.Title = clampStr(p.Title, maxFieldBytes)
	p.Description = clampStr(p.Description, maxDescriptionBytes)
	p.Publisher = clampStr(p.Publisher, maxFieldBytes)
	p.Language = clampStr(p.Language, maxFieldBytes)
	p.ISBN = clampStr(p.ISBN, maxFieldBytes)
	p.ASIN = clampStr(p.ASIN, maxFieldBytes)
	p.Series = clampStr(p.Series, maxFieldBytes)
	p.SeriesPos = clampStr(p.SeriesPos, maxFieldBytes)
	p.Authors = clampList(p.Authors)
	p.Genres = clampList(p.Genres)
	if p.Cover != nil {
		ct := normalizeCoverType(p.Cover.ContentType)
		if len(p.Cover.Bytes) == 0 || len(p.Cover.Bytes) > maxStoredCoverBytes || !allowedCoverTypes[ct] {
			p.Cover = nil // oversized / empty / non-image → don't store or serve
		} else {
			p.Cover.ContentType = ct
		}
	}
}
