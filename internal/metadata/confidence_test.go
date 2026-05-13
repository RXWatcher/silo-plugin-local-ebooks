package metadata

import "testing"

func TestConfidence_ExactTitle(t *testing.T) {
	c := Candidate{Title: "Project Hail Mary"}
	score := CalculateConfidence("Project Hail Mary", c, nil)
	if score < 30 || score > 32 {
		t.Errorf("expected ~30-32, got %d", score)
	}
}

func TestConfidence_ISBNBeatsTitleAlone(t *testing.T) {
	orig := &Candidate{ISBN: "9780593135204"}
	isbnMatch := Candidate{Title: "Wrong Title", ISBN: "9780593135204"}
	titleMatch := Candidate{Title: "Project Hail Mary", ISBN: "9999999999999"}
	a := CalculateConfidence("Project Hail Mary", isbnMatch, orig)
	b := CalculateConfidence("Project Hail Mary", titleMatch, orig)
	if a <= b {
		t.Errorf("ISBN-match (%d) should beat title-only (%d)", a, b)
	}
}

func TestConfidence_ISBNCrossFormat(t *testing.T) {
	// ISBN-10 "0593135202" and ISBN-13 "9780593135204" share core "059313520".
	orig := &Candidate{ISBN: "0593135202"}
	c := Candidate{Title: "X", ISBN: "9780593135204"}
	score := CalculateConfidence("X", c, orig)
	if score < 30 {
		t.Errorf("expected cross-format ISBN match (>=30), got %d", score)
	}
}

func TestConfidence_AuthorFractional(t *testing.T) {
	orig := &Candidate{Authors: []string{"Andy Weir", "Ghost"}}
	c := Candidate{Title: "X", Authors: []string{"Andy Weir"}}
	score := CalculateConfidence("X", c, orig)
	// title 30 (exact), author 20*(1/2)=10, completeness 2/9 → 2. Total 42.
	if score != 42 {
		t.Errorf("expected 42, got %d", score)
	}
}

func TestConfidence_MissingOriginalSkipsSignals(t *testing.T) {
	c := Candidate{Title: "X", Authors: []string{"A"}, ASIN: "B00", ISBN: "9780"}
	score := CalculateConfidence("X", c, nil)
	// title 30 + completeness 3/9 → 3. Total 33.
	if score != 33 {
		t.Errorf("expected 33, got %d", score)
	}
}

func TestConfidence_CapsAt100(t *testing.T) {
	orig := &Candidate{ISBN: "9780", Authors: []string{"A"}, ASIN: "B0"}
	c := Candidate{
		Title: "Full", ISBN: "9780", ASIN: "B0",
		Authors: []string{"A"}, Description: "d", CoverURL: "c",
		Publisher: "p", Language: "en", PageCount: 100,
		Genres: []string{"g"},
	}
	score := CalculateConfidence("Full", c, orig)
	if score > 100 {
		t.Errorf("score must cap at 100, got %d", score)
	}
}
