package ebookparse

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseFB2 extracts metadata from a FictionBook 2.0 XML file.
func ParseFB2(filePath string) (Parsed, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Parsed{}, fmt.Errorf("fb2: open: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return Parsed{}, fmt.Errorf("fb2: read: %w", err)
	}
	var book fb2Book
	if err := xml.Unmarshal(data, &book); err != nil {
		return Parsed{}, fmt.Errorf("fb2: parse: %w", err)
	}

	out := Parsed{
		Format:      "fb2",
		Title:       strings.TrimSpace(book.Description.TitleInfo.BookTitle),
		Description: strings.TrimSpace(book.Description.TitleInfo.Annotation),
		Language:    strings.TrimSpace(book.Description.TitleInfo.Lang),
	}
	for _, a := range book.Description.TitleInfo.Authors {
		name := strings.TrimSpace(strings.TrimSpace(a.FirstName) + " " + strings.TrimSpace(a.LastName))
		if name != "" {
			out.Authors = append(out.Authors, name)
		}
	}
	for _, g := range book.Description.TitleInfo.Genres {
		if g = strings.TrimSpace(g); g != "" {
			out.Genres = append(out.Genres, g)
		}
	}
	if book.Description.TitleInfo.Sequence.Name != "" {
		out.Series = book.Description.TitleInfo.Sequence.Name
		out.SeriesPos = book.Description.TitleInfo.Sequence.Number
	}
	return out, nil
}

type fb2Book struct {
	XMLName     xml.Name       `xml:"FictionBook"`
	Description fb2Description `xml:"description"`
}

type fb2Description struct {
	TitleInfo fb2TitleInfo `xml:"title-info"`
}

type fb2TitleInfo struct {
	BookTitle  string      `xml:"book-title"`
	Annotation string      `xml:"annotation"`
	Lang       string      `xml:"lang"`
	Authors    []fb2Author `xml:"author"`
	Genres     []string    `xml:"genre"`
	Sequence   fb2Sequence `xml:"sequence"`
}

type fb2Author struct {
	FirstName string `xml:"first-name"`
	LastName  string `xml:"last-name"`
}

type fb2Sequence struct {
	Name   string `xml:"name,attr"`
	Number string `xml:"number,attr"`
}
