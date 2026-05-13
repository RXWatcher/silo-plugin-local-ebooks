package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newDoubanFake serves the detail fixture on /subject/2567698/, the
// search fixture on /subject_search, and 404s for any unrecognised
// subject ID. Mirrors newISFDBFake's shape so the per-source test
// scaffolding stays uniform across the metadata-sources package.
func newDoubanFake(t *testing.T) (*httptest.Server, *Douban) {
	t.Helper()
	subject := loadFixture(t, "douban_subject.html")
	search := loadFixture(t, "douban_search.html")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/subject/2567698"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(subject)
		case strings.HasPrefix(r.URL.Path, "/subject/"):
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/subject_search":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(search)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	d := NewDoubanAt(srv.URL, "test-agent")
	d.http.Client = srv.Client()
	return srv, d
}

// TestDouban_GetBySubjectID is the happy-path detail-page assertion.
// We assert every parser-populated field so any selector regression
// is immediately visible. The fixture is 三体 (The Three-Body Problem,
// subject ID 2567698) — a real Douban catalog entry with a
// canonical Chinese metadata layout (author anchor, label-driven info
// block, all-hidden description span).
func TestDouban_GetBySubjectID(t *testing.T) {
	srv, d := newDoubanFake(t)
	defer srv.Close()

	c, err := d.Get(context.Background(), "2567698", "cn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil candidate")
	}
	if c.Title != "三体" {
		t.Errorf("title: got %q, want %q", c.Title, "三体")
	}
	if c.ExternalID != "2567698" {
		t.Errorf("external_id: got %q, want %q", c.ExternalID, "2567698")
	}
	if c.Source != doubanID {
		t.Errorf("source: got %q, want %q", c.Source, doubanID)
	}
	if c.Region != "cn" {
		t.Errorf("region: got %q, want %q", c.Region, "cn")
	}
	// Language is forced to "zh" — Douban is a Chinese-language catalog.
	if c.Language != "zh" {
		t.Errorf("language: got %q, want %q", c.Language, "zh")
	}
	if len(c.Authors) != 1 || c.Authors[0] != "刘慈欣" {
		t.Errorf("authors: got %v, want [刘慈欣]", c.Authors)
	}
	if c.Publisher != "重庆出版社" {
		t.Errorf("publisher: got %q, want %q", c.Publisher, "重庆出版社")
	}
	// Year-only output: 2008-1 → "2008".
	if c.PublishedAt != "2008" {
		t.Errorf("published_at: got %q, want %q", c.PublishedAt, "2008")
	}
	// ISBN hyphens stripped to match the rest of the codebase's storage.
	if c.ISBN != "9787536692930" {
		t.Errorf("isbn: got %q, want %q", c.ISBN, "9787536692930")
	}
	if c.Series != "中国科幻基石丛书" {
		t.Errorf("series: got %q, want %q", c.Series, "中国科幻基石丛书")
	}
	// Description prefers the all-hidden variant (unabridged).
	if !strings.Contains(c.Description, "文化大革命") {
		t.Errorf("description should contain the all-hidden intro; got %q", c.Description)
	}
	if c.CoverURL == "" || !strings.HasSuffix(c.CoverURL, "/lpic/s2768378.jpg") {
		t.Errorf("cover URL: got %q", c.CoverURL)
	}
	// Confirm fields the spec explicitly excludes are NOT populated.
	if c.PageCount != 0 {
		t.Errorf("page_count should be unset (excluded by spec), got %d", c.PageCount)
	}
	if c.ASIN != "" {
		t.Errorf("asin should be unset (excluded by spec), got %q", c.ASIN)
	}
	if len(c.Genres) != 0 {
		t.Errorf("genres should be unset (excluded by spec), got %v", c.Genres)
	}
}

// TestDouban_GetMissing verifies that a 404 surfaces as ErrNotFound.
// Any unrecognised numeric ID exercises this path (the fake's default
// branch returns 404 for /subject/<other-id>).
func TestDouban_GetMissing(t *testing.T) {
	srv, d := newDoubanFake(t)
	defer srv.Close()

	c, err := d.Get(context.Background(), "999999", "cn")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got err=%v c=%v", err, c)
	}
	if c != nil {
		t.Error("expected nil candidate on not-found")
	}
}

// TestDouban_GetNonNumericReturnsNil verifies non-numeric IDs short-
// circuit to (nil, nil) without a network call. The fake's default 404
// would surface as a non-nil error if Get tried to hit the network for
// a non-numeric input — its absence proves the regex gate fires first.
func TestDouban_GetNonNumericReturnsNil(t *testing.T) {
	srv, d := newDoubanFake(t)
	defer srv.Close()

	c, err := d.Get(context.Background(), "not-numeric", "cn")
	if err != nil {
		t.Errorf("expected nil error for non-numeric id, got %v", err)
	}
	if c != nil {
		t.Errorf("expected nil candidate for non-numeric id, got %+v", c)
	}
}

// TestDouban_SearchByText is the search happy path. The fixture's
// window.__DATA__ blob contains three items; we assert the count and
// the per-field translation on the first item (the canonical 三体
// entry), plus a multi-result sanity check.
//
// This test also documents that Douban's search results page DOES
// embed the structured items inline (window.__DATA__ = {...};) — i.e.
// it is NOT fully JS-rendered. Booklore-ng's reference relies on the
// same embedded JSON; this Go impl mirrors that behaviour.
func TestDouban_SearchByText(t *testing.T) {
	srv, d := newDoubanFake(t)
	defer srv.Close()

	cs, err := d.Search(context.Background(), "三体", "cn")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("expected 3 results from fixture, got %d", len(cs))
	}

	first := cs[0]
	if first.Title != "三体" {
		t.Errorf("first.Title: got %q, want %q", first.Title, "三体")
	}
	// ExternalID extracted from the result's URL.
	if first.ExternalID != "2567698" {
		t.Errorf("first.ExternalID: got %q, want %q", first.ExternalID, "2567698")
	}
	if len(first.Authors) != 1 || first.Authors[0] != "刘慈欣" {
		t.Errorf("first.Authors: got %v, want [刘慈欣]", first.Authors)
	}
	if first.Publisher != "重庆出版社" {
		t.Errorf("first.Publisher: got %q, want %q", first.Publisher, "重庆出版社")
	}
	if first.PublishedAt != "2008" {
		t.Errorf("first.PublishedAt: got %q, want %q", first.PublishedAt, "2008")
	}
	if first.CoverURL == "" {
		t.Errorf("first.CoverURL should be populated")
	}
	if first.Source != doubanID {
		t.Errorf("first.Source: got %q, want %q", first.Source, doubanID)
	}
	if first.Region != "cn" {
		t.Errorf("first.Region: got %q, want %q", first.Region, "cn")
	}
	if first.Language != "zh" {
		t.Errorf("first.Language: got %q, want %q", first.Language, "zh")
	}

	// Sanity-check the second item to guard the per-item loop.
	if cs[1].ExternalID != "3066477" {
		t.Errorf("cs[1].ExternalID: got %q, want %q", cs[1].ExternalID, "3066477")
	}
	if cs[1].Title != "三体II：黑暗森林" {
		t.Errorf("cs[1].Title: got %q, want %q", cs[1].Title, "三体II：黑暗森林")
	}
}
