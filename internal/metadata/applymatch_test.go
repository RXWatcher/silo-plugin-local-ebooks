package metadata

import "testing"

func TestApplyMatch_OverwritesNonEmpty(t *testing.T) {
	row := EbookRow{Title: "old", Author: "OldA"}
	c := Candidate{Title: "new", Authors: []string{"NewA"}}
	got := ApplyMatch(row, c)
	if got.Title != "new" {
		t.Errorf("title %q", got.Title)
	}
	if got.Author != "NewA" {
		t.Errorf("author %q", got.Author)
	}
}

func TestApplyMatch_PreservesNonEmptyOnEmpty(t *testing.T) {
	row := EbookRow{Title: "old", Author: "OldA"}
	c := Candidate{Title: "new"}
	got := ApplyMatch(row, c)
	if got.Title != "new" {
		t.Errorf("title %q", got.Title)
	}
	if got.Author != "OldA" {
		t.Errorf("author should be preserved, got %q", got.Author)
	}
}

func TestApplyMatch_AllFieldsPopulated(t *testing.T) {
	row := EbookRow{ID: "abc-123", Format: "epub"}
	c := Candidate{
		Title:       "T",
		Authors:     []string{"A1", "A2"},
		Publisher:   "Pub",
		PublishedAt: "2021-05-04",
		Language:    "en",
		Genres:      []string{"G1", "G2"},
		ISBN:        "9780123456789",
		ASIN:        "B08G9PRS1K",
		Description: "D",
		PageCount:   300,
		Series:      "S",
		SeriesPos:   "1",
	}
	got := ApplyMatch(row, c)
	if got.ID != "abc-123" {
		t.Errorf("ID must be preserved, got %q", got.ID)
	}
	if got.Format != "epub" {
		t.Errorf("Format must be preserved, got %q", got.Format)
	}
	if got.Title != "T" {
		t.Errorf("title %q", got.Title)
	}
	if got.Author != "A1, A2" {
		t.Errorf("author %q", got.Author)
	}
	if got.Publisher != "Pub" {
		t.Errorf("publisher %q", got.Publisher)
	}
	if got.Year != "2021" {
		t.Errorf("year %q", got.Year)
	}
	if got.Language != "en" {
		t.Errorf("language %q", got.Language)
	}
	if got.Genre != "G1, G2" {
		t.Errorf("genre %q", got.Genre)
	}
	if got.ISBN != "9780123456789" {
		t.Errorf("isbn %q", got.ISBN)
	}
	if got.ASIN != "B08G9PRS1K" {
		t.Errorf("asin %q", got.ASIN)
	}
	if got.Description != "D" {
		t.Errorf("description %q", got.Description)
	}
	if got.PageCount != 300 {
		t.Errorf("page_count %d", got.PageCount)
	}
	if got.Series != "S" {
		t.Errorf("series %q", got.Series)
	}
	if got.SeriesPos != "1" {
		t.Errorf("series_pos %q", got.SeriesPos)
	}
}

func TestApplyMatch_YearExtract(t *testing.T) {
	row := EbookRow{}
	got := ApplyMatch(row, Candidate{PublishedAt: "2021-05-04"})
	if got.Year != "2021" {
		t.Errorf("year %q", got.Year)
	}
	got = ApplyMatch(row, Candidate{PublishedAt: "2021"})
	if got.Year != "2021" {
		t.Errorf("year-only %q", got.Year)
	}
	got = ApplyMatch(row, Candidate{PublishedAt: ""})
	if got.Year != "" {
		t.Errorf("empty year %q", got.Year)
	}
}

func TestApplyMatch_FormatNotOverwritten(t *testing.T) {
	row := EbookRow{ID: "id", Format: "epub", Title: "old"}
	// Candidate has no Format field — Format must remain "epub"
	c := Candidate{Title: "new"}
	got := ApplyMatch(row, c)
	if got.Format != "epub" {
		t.Errorf("Format must be preserved at scan, got %q", got.Format)
	}
	if got.Title != "new" {
		t.Errorf("title %q", got.Title)
	}
}
