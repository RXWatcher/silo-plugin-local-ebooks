package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newISFDBFake serves the detail fixture on /cgi-bin/title.cgi?1655, the
// search fixture on /cgi-bin/se.cgi, and 404s for any unrecognised ID.
func newISFDBFake(t *testing.T) (*httptest.Server, *ISFDB) {
	t.Helper()
	title := loadFixture(t, "isfdb_title.html")
	search := loadFixture(t, "isfdb_search.html")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/title.cgi":
			if r.URL.RawQuery == "1655" {
				w.Header().Set("Content-Type", "text/html")
				w.Write(title)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "/cgi-bin/se.cgi":
			w.Header().Set("Content-Type", "text/html")
			w.Write(search)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	i := NewISFDBAt(srv.URL, "test-agent")
	i.http.Client = srv.Client()
	return srv, i
}

// TestISFDB_GetByTitleID is the happy-path assertion: every field the
// detail parser populates is asserted so any selector regression is
// immediately visible. The detail fixture intentionally repeats Frank
// Herbert across the header and the metadata block to exercise the
// first-wins dedupe path.
func TestISFDB_GetByTitleID(t *testing.T) {
	srv, i := newISFDBFake(t)
	defer srv.Close()

	c, err := i.Get(context.Background(), "1655", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil candidate")
	}
	if c.Title != "Dune" {
		t.Errorf("title: got %q, want %q", c.Title, "Dune")
	}
	if c.ExternalID != "1655" {
		t.Errorf("external_id: got %q, want %q", c.ExternalID, "1655")
	}
	if c.Source != isfdbID {
		t.Errorf("source: got %q, want %q", c.Source, isfdbID)
	}
	if c.Region != "us" {
		t.Errorf("region: got %q, want %q", c.Region, "us")
	}
	if len(c.Authors) < 1 || c.Authors[0] != "Frank Herbert" {
		t.Errorf("authors: got %v, want first=Frank Herbert", c.Authors)
	}
	// Dedupe: Frank Herbert appears 3+ times on the detail fixture but
	// must collapse to a single entry.
	for j := 1; j < len(c.Authors); j++ {
		if c.Authors[j] == c.Authors[0] {
			t.Errorf("authors not deduped: got %v", c.Authors)
		}
	}
	if c.Publisher != "Ace Books" {
		t.Errorf("publisher: got %q, want %q", c.Publisher, "Ace Books")
	}
	if c.PublishedAt != "1965" {
		t.Errorf("published_at: got %q, want %q", c.PublishedAt, "1965")
	}
	if c.PageCount != 517 {
		t.Errorf("page_count: got %d, want 517", c.PageCount)
	}
	// ISBN hyphens stripped.
	if c.ISBN != "0441172660" {
		t.Errorf("isbn: got %q, want %q", c.ISBN, "0441172660")
	}
	if c.Series != "Dune" {
		t.Errorf("series: got %q, want %q", c.Series, "Dune")
	}
	if c.SeriesPos != "1" {
		t.Errorf("series_pos: got %q, want %q", c.SeriesPos, "1")
	}
	if c.CoverURL == "" || !strings.HasSuffix(c.CoverURL, "/images/dune-cover.jpg") {
		t.Errorf("cover URL: got %q", c.CoverURL)
	}
}

// TestISFDB_GetMissing verifies that a 404 surfaces as ErrNotFound.
func TestISFDB_GetMissing(t *testing.T) {
	srv, i := newISFDBFake(t)
	defer srv.Close()

	c, err := i.Get(context.Background(), "999999", "us")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got err=%v c=%v", err, c)
	}
	if c != nil {
		t.Error("expected nil candidate on not-found")
	}
}

// TestISFDB_GetNonNumericReturnsNil verifies non-numeric IDs short-circuit
// to (nil, nil) without a network call. The fake's default 404 would
// surface as a non-nil error if Get tried to hit the network.
func TestISFDB_GetNonNumericReturnsNil(t *testing.T) {
	srv, i := newISFDBFake(t)
	defer srv.Close()

	c, err := i.Get(context.Background(), "not-numeric", "us")
	if err != nil {
		t.Errorf("expected nil error for non-numeric id, got %v", err)
	}
	if c != nil {
		t.Errorf("expected nil candidate for non-numeric id, got %+v", c)
	}
}

// TestISFDB_SearchByText is the search happy path. The fixture contains
// three well-formed rows plus a malformed (no-title-link) row that must
// be skipped. We assert the count and every parser-populated field on
// the first row.
func TestISFDB_SearchByText(t *testing.T) {
	srv, i := newISFDBFake(t)
	defer srv.Close()

	cs, err := i.Search(context.Background(), "dune", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("expected 3 results (3 well-formed rows, 1 malformed skipped), got %d", len(cs))
	}

	first := cs[0]
	if first.Title != "Dune" {
		t.Errorf("first.Title: got %q, want %q", first.Title, "Dune")
	}
	if first.ExternalID != "1655" {
		t.Errorf("first.ExternalID: got %q, want %q", first.ExternalID, "1655")
	}
	if len(first.Authors) != 1 || first.Authors[0] != "Frank Herbert" {
		t.Errorf("first.Authors: got %v, want [Frank Herbert]", first.Authors)
	}
	if first.PublishedAt != "1965" {
		t.Errorf("first.PublishedAt: got %q, want %q", first.PublishedAt, "1965")
	}
	if first.Source != isfdbID {
		t.Errorf("first.Source: got %q, want %q", first.Source, isfdbID)
	}
	if first.Region != "us" {
		t.Errorf("first.Region: got %q, want %q", first.Region, "us")
	}

	// Multi-author row: row 3 has two author anchors and both should be
	// captured. This guards the FindAllStringSubmatch path.
	var multi *struct {
		Title   string
		Authors []string
	}
	for j := range cs {
		if cs[j].Title == "Children of Dune" {
			multi = &struct {
				Title   string
				Authors []string
			}{cs[j].Title, cs[j].Authors}
			break
		}
	}
	if multi == nil {
		t.Fatal("expected the Children of Dune row to be present")
	}
	if len(multi.Authors) != 2 || multi.Authors[0] != "Frank Herbert" || multi.Authors[1] != "Kevin J. Anderson" {
		t.Errorf("multi.Authors: got %v, want [Frank Herbert, Kevin J. Anderson]", multi.Authors)
	}

	// Genres-noise drop: booklore-ng emits ['Science Fiction', 'Fantasy']
	// on every row; we intentionally drop that. Confirm it's empty.
	for j := range cs {
		if len(cs[j].Genres) != 0 {
			t.Errorf("genres should be empty (site-flavor noise dropped), got %v on row %d", cs[j].Genres, j)
		}
	}
}
