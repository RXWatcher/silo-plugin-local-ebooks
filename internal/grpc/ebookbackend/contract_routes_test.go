package ebookbackend_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

// The portal proxies the ebook_backend.v1 contract under /api/v1/*. These
// routes used to 404 entirely (handlers were only mounted at bare /catalog),
// so the plugin was non-functional in production while bare-path tests passed.

func TestAPIV1Routes_AreReachable(t *testing.T) {
	tmp := t.TempDir()
	fp := filepath.Join(tmp, "b.epub")
	if err := os.WriteFile(fp, []byte("EPUBDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{
		list:   store.Paged[store.Ebook]{Items: []store.Ebook{{}}, Total: 1, Page: 1, Limit: 50},
		detail: store.EbookDetail{},
		path:   fp, format: "epub",
		cover: []byte{0xff, 0xd8, 0xff}, coverContentType: "image/jpeg",
	}
	ts := newTestServer(t, fs)
	defer ts.Close()

	for _, path := range []string{
		"/api/v1/capabilities",
		"/api/v1/catalog",
		"/api/v1/catalog/libraries",
		"/api/v1/catalog/search?q=foo",
		"/api/v1/catalog/the-id",
		"/api/v1/browse/authors",
		"/api/v1/browse/series",
		"/api/v1/browse/genres",
		"/api/v1/file/the-id",
		"/api/v1/cover/the-id/large",
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusNotFound {
			t.Errorf("GET %s -> 404 (route not registered for the proxied contract)", path)
		}
	}
}

func TestAPIV1Catalog_EmitsNextCursorWhenMorePages(t *testing.T) {
	fs := &fakeStore{list: store.Paged[store.Ebook]{
		Items: []store.Ebook{{}}, Total: 100, Page: 1, Limit: 10,
	}}
	ts := newTestServer(t, fs)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/v1/catalog?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Items      []any  `json:"items"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.NextCursor == "" {
		t.Fatal("expected next_cursor when page*limit < total (portal stops paginating without it)")
	}
}

func TestAPIV1Catalog_NoNextCursorOnLastPage(t *testing.T) {
	fs := &fakeStore{list: store.Paged[store.Ebook]{
		Items: []store.Ebook{{}}, Total: 5, Page: 1, Limit: 50,
	}}
	ts := newTestServer(t, fs)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		NextCursor string `json:"next_cursor"`
	}
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body.NextCursor != "" {
		t.Fatalf("did not expect next_cursor on the last page, got %q", body.NextCursor)
	}
}

func TestAPIV1Catalog_InvalidLibraryIDIs400(t *testing.T) {
	fs := &fakeStore{list: store.Paged[store.Ebook]{Items: []store.Ebook{{}}, Total: 1, Page: 1, Limit: 50}}
	ts := newTestServer(t, fs)
	defer ts.Close()

	// Present-but-invalid library_id must fail closed, not silently drop the
	// filter and leak the whole multi-library catalog.
	for _, bad := range []string{"abc", "-1", "0"} {
		res, err := http.Get(ts.URL + "/api/v1/catalog?library_id=" + bad)
		if err != nil {
			t.Fatalf("GET library_id=%q: %v", bad, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("library_id=%q -> status %d, want 400", bad, res.StatusCode)
		}
	}
}
