package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newLibraryThingFake serves the work fixture on /isbn/<known>, 404s
// /isbn/<unknown>, and serves the search fixture on /search.php for
// any query. The default handler 404s everything else so that
// non-network paths (non-ISBN input) can be distinguished from
// successful network calls in TestLibraryThing_GetNonISBNReturnsNil.
func newLibraryThingFake(t *testing.T) (*httptest.Server, *LibraryThing) {
	t.Helper()
	work := loadFixture(t, "librarything_work.html")
	search := loadFixture(t, "librarything_search.html")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/isbn/9780441172665":
			w.Header().Set("Content-Type", "text/html")
			w.Write(work)
		case strings.HasPrefix(r.URL.Path, "/isbn/"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/search.php":
			w.Header().Set("Content-Type", "text/html")
			w.Write(search)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	l := NewLibraryThingAt(srv.URL, "test-agent")
	l.http.Client = srv.Client()
	return srv, l
}

// TestLibraryThing_GetByISBN is the happy-path assertion. Every field
// the work-page parser populates is asserted so any selector regression
// is immediately visible. The fixture intentionally repeats Frank
// Herbert across the header and the work-meta block to exercise the
// first-wins dedupe path; the description block has 3+ consecutive
// <br>s to exercise the multi-newline collapse path; the cover img
// uses a multi-class attribute to exercise the `cover` substring match.
func TestLibraryThing_GetByISBN(t *testing.T) {
	srv, l := newLibraryThingFake(t)
	defer srv.Close()

	c, err := l.Get(context.Background(), "978-0-441-17266-5", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil candidate")
	}
	if c.Title != "Dune" {
		t.Errorf("title: got %q, want %q", c.Title, "Dune")
	}
	// Hyphens stripped on both ISBN and ExternalID; ExternalID equals
	// ISBN (no separate work-ID identifier exposed on this path).
	if c.ISBN != "9780441172665" {
		t.Errorf("isbn: got %q, want %q", c.ISBN, "9780441172665")
	}
	if c.ExternalID != "9780441172665" {
		t.Errorf("external_id: got %q, want %q", c.ExternalID, "9780441172665")
	}
	if c.Source != libraryThingID {
		t.Errorf("source: got %q, want %q", c.Source, libraryThingID)
	}
	if c.Region != "us" {
		t.Errorf("region: got %q, want %q", c.Region, "us")
	}
	if len(c.Authors) < 1 || c.Authors[0] != "Frank Herbert" {
		t.Errorf("authors: got %v, want first=Frank Herbert", c.Authors)
	}
	// Dedupe: Frank Herbert appears 3+ times on the work fixture but
	// must collapse to a single entry.
	for j := 1; j < len(c.Authors); j++ {
		if c.Authors[j] == c.Authors[0] {
			t.Errorf("authors not deduped: got %v", c.Authors)
		}
	}
	if c.Series != "Dune" {
		t.Errorf("series: got %q, want %q", c.Series, "Dune")
	}
	if c.CoverURL == "" || !strings.HasSuffix(c.CoverURL, "/picsizes/dune.jpg") {
		t.Errorf("cover URL: got %q", c.CoverURL)
	}
	// Description must contain prose from both <p> blocks AND must NOT
	// contain raw <br> markup (br→\n cleaning) or raw <a> tags
	// (tag-strip).
	if c.Description == "" {
		t.Fatal("expected description to be populated")
	}
	if strings.Contains(c.Description, "<br") || strings.Contains(c.Description, "<a ") {
		t.Errorf("description contains raw HTML: %q", c.Description)
	}
	if !strings.Contains(c.Description, "Paul Atreides") || !strings.Contains(c.Description, "Hugo Award") {
		t.Errorf("description missing expected prose: %q", c.Description)
	}
	// Multi-newline collapse: the fixture has 3+ <br>s in a row. After
	// cleaning, no run of 3+ '\n' should remain.
	if strings.Contains(c.Description, "\n\n\n") {
		t.Errorf("description has 3+ consecutive newlines (collapse failed): %q", c.Description)
	}
	// Fields the LibraryThing parser intentionally does NOT populate.
	// Lock the contract so a future "let's also parse pages" doesn't
	// silently leak.
	if c.Publisher != "" {
		t.Errorf("publisher should be empty (not populated by LT): got %q", c.Publisher)
	}
	if c.PublishedAt != "" {
		t.Errorf("published_at should be empty on detail (not populated): got %q", c.PublishedAt)
	}
	if c.PageCount != 0 {
		t.Errorf("page_count should be zero: got %d", c.PageCount)
	}
	if c.SeriesPos != "" {
		t.Errorf("series_pos should be empty: got %q", c.SeriesPos)
	}
}

// TestLibraryThing_GetMissing verifies that a 404 surfaces as
// ErrNotFound.
func TestLibraryThing_GetMissing(t *testing.T) {
	srv, l := newLibraryThingFake(t)
	defer srv.Close()

	c, err := l.Get(context.Background(), "9780000000000", "us")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got err=%v c=%v", err, c)
	}
	if c != nil {
		t.Error("expected nil candidate on not-found")
	}
}

// TestLibraryThing_GetNonISBNReturnsNil verifies non-ISBN inputs
// short-circuit to (nil, nil) without a network call. The fake's
// default 404 would surface as ErrNotFound (not nil) if Get tried to
// hit the network for these inputs.
func TestLibraryThing_GetNonISBNReturnsNil(t *testing.T) {
	srv, l := newLibraryThingFake(t)
	defer srv.Close()

	// Various non-ISBN shapes: too-short, too-long, alphanumeric ASIN,
	// title text. All must return (nil, nil).
	for _, id := range []string{"", "12345", "B00FLIJJSA", "not-an-isbn", "97804411726651"} {
		c, err := l.Get(context.Background(), id, "us")
		if err != nil {
			t.Errorf("id=%q: expected nil error, got %v", id, err)
		}
		if c != nil {
			t.Errorf("id=%q: expected nil candidate, got %+v", id, c)
		}
	}
}

// TestLibraryThing_SearchByText is the search happy path. The fixture
// contains three well-formed <tr class="searchresult"> rows plus one
// malformed (no /work/ title link) row that must be skipped. The table
// also has a header row without the searchresult class — the row regex
// must NOT pick it up.
func TestLibraryThing_SearchByText(t *testing.T) {
	srv, l := newLibraryThingFake(t)
	defer srv.Close()

	cs, err := l.Search(context.Background(), "dune", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("expected 3 results (3 well-formed rows, 1 malformed skipped, header excluded by class filter), got %d", len(cs))
	}

	first := cs[0]
	if first.Title != "Dune" {
		t.Errorf("first.Title: got %q, want %q", first.Title, "Dune")
	}
	// ExternalID is intentionally empty on search rows: the /work/<id>
	// link's id is a LibraryThing work ID, not an ISBN, and Get only
	// accepts ISBNs. See parseLibraryThingSearchResults doc.
	if first.ExternalID != "" {
		t.Errorf("first.ExternalID: got %q, want empty", first.ExternalID)
	}
	if len(first.Authors) != 1 || first.Authors[0] != "Frank Herbert" {
		t.Errorf("first.Authors: got %v, want [Frank Herbert]", first.Authors)
	}
	if first.PublishedAt != "1965" {
		t.Errorf("first.PublishedAt: got %q, want %q", first.PublishedAt, "1965")
	}
	if first.Source != libraryThingID {
		t.Errorf("first.Source: got %q, want %q", first.Source, libraryThingID)
	}
	if first.Region != "us" {
		t.Errorf("first.Region: got %q, want %q", first.Region, "us")
	}

	// All three rows must be present in order.
	wantTitles := []string{"Dune", "Dune Messiah", "Children of Dune"}
	for j, want := range wantTitles {
		if cs[j].Title != want {
			t.Errorf("row %d title: got %q, want %q", j, cs[j].Title, want)
		}
	}
}
