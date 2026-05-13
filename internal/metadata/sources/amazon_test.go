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
	if len(c.Authors) == 0 || c.Authors[0] != "Andy Weir" {
		t.Errorf("authors: got %v, want [Andy Weir]", c.Authors)
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
