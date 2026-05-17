package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAmazonFake(t *testing.T) (*httptest.Server, *Amazon) {
	t.Helper()
	book := loadFixture(t, "amazon_book.html")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/dp/B08G9PRS1K"):
			w.Header().Set("Content-Type", "text/html")
			w.Write(book)
		case strings.HasPrefix(r.URL.Path, "/dp/"):
			// any other ASIN → 404
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	a := NewAmazonAt(srv.URL, "test-agent")
	a.http.Client = srv.Client()
	return srv, a
}

// TestAmazon_GetByASIN verifies happy-path scraping of a product page.
// The fixture contains the structural elements the parser depends on; the
// test asserts every field the parser populates so that any regression in
// a single selector is visible.
func TestAmazon_GetByASIN(t *testing.T) {
	srv, a := newAmazonFake(t)
	defer srv.Close()

	c, err := a.Get(context.Background(), "B08G9PRS1K", "us")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil candidate")
	}
	if c.Title != "Project Hail Mary" {
		t.Errorf("title: got %q, want %q", c.Title, "Project Hail Mary")
	}
	if c.Source != amazonID {
		t.Errorf("source: got %q, want %q", c.Source, amazonID)
	}
	if c.ASIN != "B08G9PRS1K" {
		t.Errorf("asin: got %q, want %q", c.ASIN, "B08G9PRS1K")
	}
	if c.ExternalID != "B08G9PRS1K" {
		t.Errorf("external_id: got %q, want %q", c.ExternalID, "B08G9PRS1K")
	}
	// Fixture has two author spans (Andy Weir, Mary Robinette Kowal) so we
	// verify that the parser walks every byline block in document order,
	// not just the first. The "(Author)" role labels live in sibling spans
	// outside the <a> tag and are naturally excluded from the link-text
	// capture, so the parenthesized-name filter is not exercised here.
	wantAuthors := []string{"Andy Weir", "Mary Robinette Kowal"}
	if len(c.Authors) != len(wantAuthors) {
		t.Errorf("authors: got %v, want %v", c.Authors, wantAuthors)
	} else {
		for i, want := range wantAuthors {
			if c.Authors[i] != want {
				t.Errorf("authors[%d]: got %q, want %q", i, c.Authors[i], want)
			}
		}
	}
	if c.ISBN != "9780593135204" {
		t.Errorf("isbn: got %q, want %q", c.ISBN, "9780593135204")
	}
	if c.Publisher != "Ballantine Books" {
		t.Errorf("publisher: got %q, want %q", c.Publisher, "Ballantine Books")
	}
	if c.PublishedAt != "May 4, 2021" {
		t.Errorf("published_at: got %q, want %q", c.PublishedAt, "May 4, 2021")
	}
	if c.Language != "English" {
		t.Errorf("language: got %q, want %q", c.Language, "English")
	}
	if c.PageCount != 476 {
		t.Errorf("page_count: got %d, want 476", c.PageCount)
	}
	if c.CoverURL == "" {
		t.Error("expected non-empty cover URL")
	}
	if c.Description == "" {
		t.Error("expected non-empty description")
	}
	if c.Region != "us" {
		t.Errorf("region: got %q, want %q", c.Region, "us")
	}
}

// TestAmazon_GetMissing verifies that a 404 surfaces as ErrNotFound rather
// than a nil candidate or an opaque error.
func TestAmazon_GetMissing(t *testing.T) {
	srv, a := newAmazonFake(t)
	defer srv.Close()

	c, err := a.Get(context.Background(), "B000000000", "us")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got err=%v c=%v", err, c)
	}
	if c != nil {
		t.Error("expected nil candidate on not-found")
	}
}

// TestAmazon_NonASINGetReturnsNil verifies that an ISBN-13 (13 digits, not
// ASIN-shaped) returns (nil, nil) without hitting the network. The test
// uses a server that 404s any path so a network call would fail the
// assertion of nil error.
func TestAmazon_NonASINGetReturnsNil(t *testing.T) {
	srv, a := newAmazonFake(t)
	defer srv.Close()

	c, err := a.Get(context.Background(), "9780593135204", "us")
	if err != nil {
		t.Errorf("expected nil error for non-ASIN id, got %v", err)
	}
	if c != nil {
		t.Errorf("expected nil candidate for non-ASIN id, got %+v", c)
	}
}

// TestAmazon_HostForRegion locks the region→host contract. The fake-server
// tests in this file construct Amazon with a non-production baseURL, which
// trips the early-return at the top of amazonHostFor and bypasses the
// region switch entirely. A typo like "amazonn.de" would slip through.
// This test pins each branch directly against the production baseURL.
func TestAmazon_HostForRegion(t *testing.T) {
	a := &Amazon{baseURL: amazonBaseURL}
	cases := []struct {
		region string
		want   string
	}{
		{"us", "https://www.amazon.com"},
		{"", "https://www.amazon.com"},
		{"uk", "https://www.amazon.co.uk"},
		{"de", "https://www.amazon.de"},
		{"jp", "https://www.amazon.co.jp"},
		{"ca", "https://www.amazon.ca"},
		// Unknown region must NOT be interpolated into the host (SSRF) —
		// it falls back to the US host.
		{"xx", "https://www.amazon.com"},
	}
	for _, tc := range cases {
		if got := a.amazonHostFor(tc.region); got != tc.want {
			t.Errorf("amazonHostFor(%q): got %q, want %q", tc.region, got, tc.want)
		}
	}
}

// TestAmazon_SearchNonASINReturnsNil documents the deliberate decision to
// skip text search entirely. ASIN-shaped queries delegate to Get; anything
// else returns (nil, nil) without a network call.
func TestAmazon_SearchNonASINReturnsNil(t *testing.T) {
	srv, a := newAmazonFake(t)
	defer srv.Close()

	cs, err := a.Search(context.Background(), "project hail mary", "us")
	if err != nil {
		t.Errorf("expected nil error for text search, got %v", err)
	}
	if cs != nil {
		t.Errorf("expected nil slice for text search, got %+v", cs)
	}
}
