package ebookbackend_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/grpc/ebookbackend"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/server"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/tokens"
)

// --- transform unit tests --------------------------------------------------

func TestToBook_HappyPath(t *testing.T) {
	in := store.Ebook{
		ID: "abc", Title: "Atlas Shrugged",
		Authors: []string{"Ayn Rand"},
		Series:  "X", SeriesIndex: "1",
		Year: "1957", Language: "en",
		Format: "EPUB", HasCover: true,
	}
	got := ebookbackend.ToBook(in)
	want := ebookbackend.Book{
		ID: "abc", Title: "Atlas Shrugged",
		Authors: []string{"Ayn Rand"},
		Series:  "X", SeriesIndex: "1",
		Year: "1957", Language: "en",
		HasCover: true, Formats: []string{"epub"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ToBook: got %+v want %+v", got, want)
	}
}

func TestToBook_EmptyFormatYieldsEmptyFormats(t *testing.T) {
	// Tests intent: clients can iterate Formats unconditionally without nil
	// checks. An empty Format field must produce a non-nil empty slice.
	in := store.Ebook{ID: "x", Title: "Y"}
	got := ebookbackend.ToBook(in)
	if got.Formats == nil {
		t.Errorf("Formats must be non-nil even for empty format")
	}
	if len(got.Formats) != 0 {
		t.Errorf("Formats = %v; want empty", got.Formats)
	}
	if got.HasCover {
		t.Errorf("HasCover = true; want false on empty input")
	}
}

func TestToBookDetail_PopulatesFile(t *testing.T) {
	in := store.EbookDetail{
		Ebook: store.Ebook{
			ID: "k", Title: "T", Format: "pdf", HasCover: false,
		},
		Description: "D",
		ISBN:        "978-X",
		FileSize:    1024,
	}
	got := ebookbackend.ToBookDetail(in)
	if len(got.Files) != 1 {
		t.Fatalf("Files = %+v; want 1 file", got.Files)
	}
	if got.Files[0].MimeType != "application/pdf" {
		t.Errorf("MimeType = %q", got.Files[0].MimeType)
	}
	if got.Files[0].SizeBytes != 1024 {
		t.Errorf("SizeBytes = %d", got.Files[0].SizeBytes)
	}
	if got.Description != "D" || got.ISBN != "978-X" {
		t.Errorf("metadata: %+v", got)
	}
}

func TestToBookDetail_NoFormatYieldsEmptyFiles(t *testing.T) {
	// Tests intent: Files must always be a non-nil empty slice (never nil) so
	// JSON marshals as [] and the contract is stable for clients.
	in := store.EbookDetail{Ebook: store.Ebook{ID: "x", Title: "Y"}}
	got := ebookbackend.ToBookDetail(in)
	if got.Files == nil || len(got.Files) != 0 {
		t.Errorf("Files = %v; want empty non-nil", got.Files)
	}
}

func TestFormatToMime(t *testing.T) {
	cases := map[string]string{
		"epub": "application/epub+zip",
		"PDF":  "application/pdf", // case-insensitive
		"mobi": "application/x-mobipocket-ebook",
		"azw3": "application/vnd.amazon.ebook",
		"fb2":  "application/x-fictionbook+xml",
		"":     "application/octet-stream",
		"xyz":  "application/octet-stream",
	}
	for in, want := range cases {
		if got := ebookbackend.FormatToMime(in); got != want {
			t.Errorf("FormatToMime(%q)=%q want %q", in, got, want)
		}
	}
}

// --- HTTP handler tests with a fake store ----------------------------------

type fakeStore struct {
	list    store.Paged[store.Ebook]
	listErr error

	detail    store.EbookDetail
	detailErr error

	cover            []byte
	coverContentType string
	coverErr         error

	path, format string
	pathErr      error

	authors store.Paged[store.Author]
	series  store.Paged[store.Series]
	genres  store.Paged[store.Genre]

	libraries []store.LibraryPath
}

func (f *fakeStore) ListEbooks(_ context.Context, _ store.ListParams) (store.Paged[store.Ebook], error) {
	return f.list, f.listErr
}
func (f *fakeStore) ListLibraryPaths(_ context.Context) ([]store.LibraryPath, error) {
	return f.libraries, nil
}
func (f *fakeStore) GetEbookByID(_ context.Context, _ string) (store.EbookDetail, error) {
	return f.detail, f.detailErr
}
func (f *fakeStore) GetCover(_ context.Context, _ string) ([]byte, string, error) {
	return f.cover, f.coverContentType, f.coverErr
}
func (f *fakeStore) GetEbookPath(_ context.Context, _ string) (string, string, error) {
	return f.path, f.format, f.pathErr
}
func (f *fakeStore) ListAuthors(_ context.Context, _ store.ListParams) (store.Paged[store.Author], error) {
	return f.authors, nil
}
func (f *fakeStore) ListSeries(_ context.Context, _ store.ListParams) (store.Paged[store.Series], error) {
	return f.series, nil
}
func (f *fakeStore) ListGenres(_ context.Context, _ store.ListParams) (store.Paged[store.Genre], error) {
	return f.genres, nil
}

const testSecret = "test-secret-with-enough-entropy-32"

// signTestToken mints an HS256 token shaped like what the portal produces.
// Returns the URL-encoded query value.
func signTestToken(t *testing.T, bookID string, fileIdx int) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"aud":      tokens.Audience,
		"sub":      "1",
		"book_id":  bookID,
		"file_idx": fileIdx,
		"exp":      time.Now().Add(5 * time.Minute).Unix(),
		"iat":      time.Now().Unix(),
	})
	s, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return url.QueryEscape(s)
}

func newTestServer(t *testing.T, fs *fakeStore) *httptest.Server {
	t.Helper()
	srv := ebookbackend.NewServer(fs, nil, testSecret)
	mux := http.NewServeMux()
	server.MountCatalog(mux, srv)
	return httptest.NewServer(mux)
}

func TestList_ReturnsItemsAndPagination(t *testing.T) {
	fs := &fakeStore{
		list: store.Paged[store.Ebook]{
			Items: []store.Ebook{
				{ID: "1", Title: "A", Format: "epub", HasCover: true, Authors: []string{"X"}},
				{ID: "2", Title: "B", Format: "pdf"},
			},
			Total: 2, Page: 1, Limit: 50,
		},
	}
	ts := newTestServer(t, fs)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/catalog?page=1&limit=10&search=foo")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got ebookbackend.Page[ebookbackend.Book]
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || len(got.Items) != 2 {
		t.Errorf("got %+v", got)
	}
	if got.Items[0].CoverURL != "/catalog/1/cover" {
		t.Errorf("CoverURL = %q (HasCover should produce it)", got.Items[0].CoverURL)
	}
	if got.Items[1].CoverURL != "" {
		t.Errorf("CoverURL = %q (no cover should be empty)", got.Items[1].CoverURL)
	}
}

func TestDetail_HappyPath(t *testing.T) {
	fs := &fakeStore{
		detail: store.EbookDetail{
			Ebook: store.Ebook{ID: "k", Title: "T", Format: "epub", HasCover: true},
			ISBN:  "978-X", FileSize: 2048,
		},
	}
	ts := newTestServer(t, fs)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/catalog/k")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got ebookbackend.BookDetail
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "k" || got.ISBN != "978-X" {
		t.Errorf("got %+v", got)
	}
	if len(got.Files) != 1 || got.Files[0].URL != "/catalog/k/file" {
		t.Errorf("files: %+v", got.Files)
	}
	if got.CoverURL != "/catalog/k/cover" {
		t.Errorf("CoverURL = %q", got.CoverURL)
	}
}

func TestDetail_NotFound(t *testing.T) {
	fs := &fakeStore{detailErr: store.ErrNotFound}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("status=%d", res.StatusCode)
	}
}

func TestCover_StreamsBytesWithContentType(t *testing.T) {
	fs := &fakeStore{cover: []byte{0xff, 0xd8, 0xff}, coverContentType: "image/jpeg"}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/1/cover?size=large&token=" + signTestToken(t, "1", tokens.CoverFileIdx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type=%q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	if len(body) != 3 {
		t.Errorf("len(body)=%d", len(body))
	}
}

func TestCover_NotFound(t *testing.T) {
	fs := &fakeStore{coverErr: store.ErrNotFound}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/1/cover?token=" + signTestToken(t, "1", tokens.CoverFileIdx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("status=%d", res.StatusCode)
	}
}

func TestFile_StreamsFileAndSetsHeaders(t *testing.T) {
	// Write a temp file the handler will stream.
	dir := t.TempDir()
	body := []byte("epub-bytes-here")
	p := filepath.Join(dir, "atlas shrugged.epub")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	fs := &fakeStore{path: p, format: "epub"}
	ts := newTestServer(t, fs)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/catalog/k/file?token=" + signTestToken(t, "k", tokens.FileFileIdx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/epub+zip" {
		t.Errorf("Content-Type=%q", ct)
	}
	cd := res.Header.Get("Content-Disposition")
	if !strings.Contains(cd, `filename="atlas shrugged.epub"`) {
		t.Errorf("Content-Disposition=%q", cd)
	}
	got, _ := io.ReadAll(res.Body)
	if string(got) != string(body) {
		t.Errorf("body mismatch: got %q", string(got))
	}
}

func TestFile_NotFound(t *testing.T) {
	fs := &fakeStore{pathErr: store.ErrNotFound}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/nope/file?token=" + signTestToken(t, "nope", tokens.FileFileIdx))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("status=%d", res.StatusCode)
	}
}

func TestAuthors_PaginatedResult(t *testing.T) {
	fs := &fakeStore{
		authors: store.Paged[store.Author]{
			Items: []store.Author{{Name: "Ayn Rand", Count: 3}},
			Total: 1, Page: 1, Limit: 50,
		},
	}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/authors")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got ebookbackend.Page[ebookbackend.Author]
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Name != "Ayn Rand" || got.Items[0].Count != 3 {
		t.Errorf("got %+v", got)
	}
}

func TestSeries_PaginatedResult(t *testing.T) {
	fs := &fakeStore{
		series: store.Paged[store.Series]{
			Items: []store.Series{{Name: "Foundation", Count: 7}},
			Total: 1, Page: 1, Limit: 50,
		},
	}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/series")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got ebookbackend.Page[ebookbackend.Series]
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Name != "Foundation" {
		t.Errorf("got %+v", got)
	}
}

func TestGenres_PaginatedResult(t *testing.T) {
	fs := &fakeStore{
		genres: store.Paged[store.Genre]{
			Items: []store.Genre{{Name: "Sci-Fi", Count: 12}},
			Total: 1, Page: 1, Limit: 50,
		},
	}
	ts := newTestServer(t, fs)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/catalog/genres")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	var got ebookbackend.Page[ebookbackend.Genre]
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Name != "Sci-Fi" {
		t.Errorf("got %+v", got)
	}
}
