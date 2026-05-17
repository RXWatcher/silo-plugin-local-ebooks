package ebookparse

import (
	"strings"
	"testing"
)

func TestSanitize_BoundsFieldsAndCover(t *testing.T) {
	p := Parsed{
		Title:       strings.Repeat("t", 10<<20),
		Description: strings.Repeat("d", 10<<20),
		Authors:     make([]string, 5000),
		Genres:      make([]string, 5000),
		Cover:       &Cover{ContentType: "text/html", Bytes: []byte("<script>alert(1)</script>")},
	}
	for i := range p.Authors {
		p.Authors[i] = strings.Repeat("a", 1<<20)
	}
	p.sanitize()

	if len(p.Title) > maxFieldBytes {
		t.Fatalf("title not clamped: %d", len(p.Title))
	}
	if len(p.Description) > maxDescriptionBytes {
		t.Fatalf("description not clamped: %d", len(p.Description))
	}
	if len(p.Authors) > maxListItems || len(p.Authors[0]) > maxFieldBytes {
		t.Fatalf("authors not clamped: n=%d len0=%d", len(p.Authors), len(p.Authors[0]))
	}
	if len(p.Genres) > maxListItems {
		t.Fatalf("genres not clamped: %d", len(p.Genres))
	}
	if p.Cover != nil {
		t.Fatalf("text/html cover must be dropped (XSS), got %+v", p.Cover)
	}
}

func TestSanitize_CoverPolicy(t *testing.T) {
	// Oversized image cover dropped.
	big := Parsed{Cover: &Cover{ContentType: "image/png", Bytes: make([]byte, maxStoredCoverBytes+1)}}
	big.sanitize()
	if big.Cover != nil {
		t.Fatal("oversized cover must be dropped")
	}
	// Valid image cover kept, content-type normalized.
	ok := Parsed{Cover: &Cover{ContentType: "IMAGE/JPEG; charset=binary", Bytes: []byte{0xff, 0xd8, 0xff}}}
	ok.sanitize()
	if ok.Cover == nil || ok.Cover.ContentType != "image/jpeg" {
		t.Fatalf("valid cover wrongly altered: %+v", ok.Cover)
	}
}
