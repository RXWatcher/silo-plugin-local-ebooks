package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newWorldCatFake serves the detail fixture on /isbn/0441172660, the
// search fixture on /search, and 404s for any unrecognised ISBN. Mirrors
// booklore-ng's two-endpoint shape: ISBN lookup is a direct GET on the
// /isbn/<n> path; keyword search hits /search?q=<query>.
func newWorldCatFake(t *testing.T) (*httptest.Server, *WorldCat) {
	t.Helper()
	record := loadFixture(t, "worldcat_record.html")
	search := loadFixture(t, "worldcat_search.html")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/isbn/0441172660":
			w.Header().Set("Content-Type", "text/html")
			w.Write(record)
		case strings.HasPrefix(r.URL.Path, "/isbn/"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/search":
			w.Header().Set("Content-Type", "text/html")
			w.Write(search)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	wc := NewWorldCatAt(srv.URL, "test-agent")
	wc.http.Client = srv.Client()
	return srv, wc
}

// TestWorldCat_GetByISBN is the happy-path assertion. The detail fixture
// is hand-crafted to exercise every parser path that survives our
// Candidate-scope decisions: title via class="title" h1, multi-author
// with first-wins dedupe, inline Publisher: label, Year: label, inline
// Language: label, #summary description (with entity decoding), and a
// class="cover" image. Per-task spec: ExternalID == ISBN == cleaned input.
func TestWorldCat_GetByISBN(t *testing.T) {
	srv, wc := newWorldCatFake(t)
	defer srv.Close()

	// Hyphens in the input are intentional; the implementation strips
	// them before the regex test and the URL build.
	c, err := wc.Get(context.Background(), "0-441-17266-0", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil candidate")
	}
	if c.Title != "Dune" {
		t.Errorf("title: got %q, want %q", c.Title, "Dune")
	}
	// Per-task spec: ExternalID and ISBN both hold the cleaned ISBN.
	if c.ExternalID != "0441172660" {
		t.Errorf("external_id: got %q, want %q", c.ExternalID, "0441172660")
	}
	if c.ISBN != "0441172660" {
		t.Errorf("isbn: got %q, want %q", c.ISBN, "0441172660")
	}
	if c.Source != worldCatID {
		t.Errorf("source: got %q, want %q", c.Source, worldCatID)
	}
	if c.Region != "us" {
		t.Errorf("region: got %q, want %q", c.Region, "us")
	}
	// Dedupe: Frank Herbert appears twice in the fixture's byline and
	// must collapse to a single entry; Kevin J. Anderson follows.
	if len(c.Authors) != 2 || c.Authors[0] != "Frank Herbert" || c.Authors[1] != "Kevin J. Anderson" {
		t.Errorf("authors: got %v, want [Frank Herbert, Kevin J. Anderson]", c.Authors)
	}
	if c.Publisher != "Ace Books" {
		t.Errorf("publisher: got %q, want %q", c.Publisher, "Ace Books")
	}
	if c.PublishedAt != "1965" {
		t.Errorf("published_at: got %q, want %q", c.PublishedAt, "1965")
	}
	if c.Language != "English" {
		t.Errorf("language: got %q, want %q", c.Language, "English")
	}
	// Description: the fixture's `&amp;` must decode to `&` and
	// surrounding whitespace must collapse.
	if c.Description == "" || !strings.Contains(c.Description, "Arrakis") || !strings.Contains(c.Description, "& widely") {
		t.Errorf("description: got %q (entity decode or extraction failed)", c.Description)
	}
	if c.CoverURL == "" || !strings.HasSuffix(c.CoverURL, "/images/covers/dune.jpg") {
		t.Errorf("cover URL: got %q", c.CoverURL)
	}

	// Scope guards: WorldCat candidates intentionally do NOT populate
	// these fields. A regression that starts emitting them needs to
	// either be intentional (and update the test) or rejected.
	if len(c.Genres) != 0 {
		t.Errorf("genres should be empty (noisy library subject headings dropped), got %v", c.Genres)
	}
	if c.PageCount != 0 {
		t.Errorf("page_count should be zero (false-positive prone), got %d", c.PageCount)
	}
	if c.Series != "" || c.SeriesPos != "" {
		t.Errorf("series fields should be empty, got series=%q pos=%q", c.Series, c.SeriesPos)
	}
}

// TestWorldCat_GetMissing verifies that a 404 on /isbn/<n> surfaces as
// ErrNotFound and returns a nil candidate.
func TestWorldCat_GetMissing(t *testing.T) {
	srv, wc := newWorldCatFake(t)
	defer srv.Close()

	c, err := wc.Get(context.Background(), "9780000000000", "us")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got err=%v c=%v", err, c)
	}
	if c != nil {
		t.Error("expected nil candidate on not-found")
	}
}

// TestWorldCat_GetNonISBNReturnsNil verifies non-ISBN input short-circuits
// to (nil, nil) without a network call. The fake's default handler 404s
// every unknown path, so a network call here would surface as a non-nil
// error rather than the silent (nil, nil) we require.
func TestWorldCat_GetNonISBNReturnsNil(t *testing.T) {
	srv, wc := newWorldCatFake(t)
	defer srv.Close()

	c, err := wc.Get(context.Background(), "not-an-isbn", "us")
	if err != nil {
		t.Errorf("expected nil error for non-ISBN id, got %v", err)
	}
	if c != nil {
		t.Errorf("expected nil candidate for non-ISBN id, got %+v", c)
	}
}

// TestWorldCat_SearchByText is the search happy path. The fixture
// contains two well-formed result blocks (one via class="title" anchor,
// one via <h3> heading + classed author span) plus a malformed block
// with neither a title anchor nor a heading. The malformed block must
// be skipped. Both well-formed rows are asserted in detail to guard
// every search-path selector.
func TestWorldCat_SearchByText(t *testing.T) {
	srv, wc := newWorldCatFake(t)
	defer srv.Close()

	cs, err := wc.Search(context.Background(), "dune", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 2 {
		t.Fatalf("expected 2 results (2 well-formed rows, 1 malformed skipped), got %d", len(cs))
	}

	// Row 0: title via class="title" anchor, author via "by …" anchor.
	first := cs[0]
	if first.Title != "Dune" {
		t.Errorf("first.Title: got %q, want %q", first.Title, "Dune")
	}
	if len(first.Authors) != 1 || first.Authors[0] != "Frank Herbert" {
		t.Errorf("first.Authors: got %v, want [Frank Herbert]", first.Authors)
	}
	if first.PublishedAt != "1965" {
		t.Errorf("first.PublishedAt: got %q, want %q", first.PublishedAt, "1965")
	}
	if first.Language != "English" {
		t.Errorf("first.Language: got %q, want %q", first.Language, "English")
	}
	if first.Source != worldCatID {
		t.Errorf("first.Source: got %q, want %q", first.Source, worldCatID)
	}
	if first.Region != "us" {
		t.Errorf("first.Region: got %q, want %q", first.Region, "us")
	}
	// ExternalID is intentionally empty on search rows: per-row HTML
	// doesn't expose a stable ISBN/record id we can pin to.
	if first.ExternalID != "" {
		t.Errorf("first.ExternalID should be empty on search rows, got %q", first.ExternalID)
	}

	// Row 1: title via <h3> heading fallback, author via classed span
	// fallback. Guards both alternative selectors in one row.
	second := cs[1]
	if second.Title != "Dune Messiah" {
		t.Errorf("second.Title: got %q, want %q", second.Title, "Dune Messiah")
	}
	if len(second.Authors) != 1 || second.Authors[0] != "Frank Herbert" {
		t.Errorf("second.Authors: got %v, want [Frank Herbert]", second.Authors)
	}
	if second.PublishedAt != "1969" {
		t.Errorf("second.PublishedAt: got %q, want %q", second.PublishedAt, "1969")
	}
}
