package ebookparse

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseFB2_HappyPath(t *testing.T) {
	fb2 := `<?xml version="1.0" encoding="UTF-8"?>
<FictionBook xmlns="http://www.gribuser.ru/xml/fictionbook/2.0">
<description><title-info>
  <genre>sf_space</genre>
  <author><first-name>Andy</first-name><last-name>Weir</last-name></author>
  <book-title>Project Hail Mary</book-title>
  <annotation>A lone astronaut.</annotation>
  <lang>en</lang>
  <sequence name="Hail Mary" number="1"/>
</title-info></description>
</FictionBook>`
	path := filepath.Join(t.TempDir(), "sample.fb2")
	os.WriteFile(path, []byte(fb2), 0o644)
	p, err := ParseFB2(path)
	if err != nil { t.Fatal(err) }
	if p.Format != "fb2" { t.Errorf("format %q", p.Format) }
	if p.Title != "Project Hail Mary" { t.Errorf("title %q", p.Title) }
	if len(p.Authors) != 1 || p.Authors[0] != "Andy Weir" {
		t.Errorf("authors %v", p.Authors)
	}
	if p.Language != "en" { t.Errorf("language %q", p.Language) }
	if p.Description != "A lone astronaut." { t.Errorf("desc %q", p.Description) }
	if p.Series != "Hail Mary" || p.SeriesPos != "1" {
		t.Errorf("series %q %q", p.Series, p.SeriesPos)
	}
	if len(p.Genres) != 1 || p.Genres[0] != "sf_space" {
		t.Errorf("genres %v", p.Genres)
	}
}

func TestParseFB2_MissingFile(t *testing.T) {
	_, err := ParseFB2("/nonexistent.fb2")
	if err == nil { t.Error("expected error") }
}
