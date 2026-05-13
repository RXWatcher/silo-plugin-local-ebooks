package ebookparse

import (
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ParsePDF extracts metadata from a PDF file's info dictionary.
// PDF carries only a small set of fields; ISBN/series/language are typically absent.
func ParsePDF(filePath string) (Parsed, error) {
	f, r, err := pdf.Open(filePath)
	if err != nil {
		return Parsed{}, fmt.Errorf("pdf: open: %w", err)
	}
	defer f.Close()

	out := Parsed{
		Format:    "pdf",
		PageCount: r.NumPage(),
	}

	info := r.Trailer().Key("Info")
	if info.IsNull() {
		return out, nil
	}

	if t := strings.TrimSpace(info.Key("Title").Text()); t != "" {
		out.Title = t
	}
	if a := strings.TrimSpace(info.Key("Author").Text()); a != "" {
		out.Authors = append(out.Authors, a)
	}
	if s := strings.TrimSpace(info.Key("Subject").Text()); s != "" {
		out.Description = s
	}
	if k := strings.TrimSpace(info.Key("Keywords").Text()); k != "" {
		for _, g := range strings.Split(k, ",") {
			if g = strings.TrimSpace(g); g != "" {
				out.Genres = append(out.Genres, g)
			}
		}
	}
	if d := strings.TrimSpace(info.Key("CreationDate").Text()); d != "" {
		// PDF dates: "D:20210504000000+00'00'" — extract year
		if len(d) >= 6 && strings.HasPrefix(d, "D:") {
			if t, err := tryParseDate(d[2:6]); err == nil {
				out.PublishedAt = t
			}
		}
	}

	return out, nil
}
