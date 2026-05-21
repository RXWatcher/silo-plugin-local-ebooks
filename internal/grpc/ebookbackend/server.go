package ebookbackend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/tokens"
)

// writeTokenError surfaces the appropriate status code for verification
// failures: 503 when the plugin has no signing secret configured, 401 for
// any other rejection (missing/invalid/expired token, book or file mismatch).
func writeTokenError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if errors.Is(err, tokens.ErrSecretUnconfigured) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "media signing secret not configured"})
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
}

// Store is the minimal store-layer interface the server depends on. Defined
// here (not in package store) so unit tests can substitute a fake without
// pulling in pgx.
type Store interface {
	ListEbooks(ctx context.Context, p store.ListParams) (store.Paged[store.Ebook], error)
	ListLibraryPaths(ctx context.Context) ([]store.LibraryPath, error)
	GetEbookByID(ctx context.Context, id string) (store.EbookDetail, error)
	GetCover(ctx context.Context, id string) ([]byte, string, error)
	GetEbookPath(ctx context.Context, id string) (string, string, error)
	ListAuthors(ctx context.Context, p store.ListParams) (store.Paged[store.Author], error)
	ListSeries(ctx context.Context, p store.ListParams) (store.Paged[store.Series], error)
	ListGenres(ctx context.Context, p store.ListParams) (store.Paged[store.Genre], error)
}

// Server hosts the ebook_backend.v1 contract handlers. Use NewServer to
// construct one; mount the returned http.Handler under whatever prefix the
// host expects.
type Server struct {
	store  Store
	logger *slog.Logger
	secret string // shared HMAC for signed media token verification
}

// NewServer constructs a Server. If logger is nil, slog.Default is used.
// secret is the HMAC key shared with the ebooks portal — Cover() and File()
// each require a valid signed ?token= matching the book id and file_idx.
func NewServer(s Store, logger *slog.Logger, secret string) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{store: s, logger: logger, secret: secret}
}

// Libraries handles GET /catalog/libraries.
func (s *Server) Libraries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListLibraryPaths(r.Context())
	if err != nil {
		s.serverError(w, "list libraries", err)
		return
	}
	items := make([]Library, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		items = append(items, ToLibrary(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// --- list ------------------------------------------------------------------

// List handles GET /catalog (and /api/v1/catalog). Accepts library,
// library_id, page|cursor, limit, search query params.
func (s *Server) List(w http.ResponseWriter, r *http.Request) {
	p, ok := listParams(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid library_id")
		return
	}
	s.listEbooks(w, r, p)
}

// Search handles GET /api/v1/catalog/search?q=. The portal contract uses the
// `q` param; we fall back to `search` for direct callers.
func (s *Server) Search(w http.ResponseWriter, r *http.Request) {
	p, ok := listParams(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid library_id")
		return
	}
	if q := r.URL.Query().Get("q"); q != "" {
		p.Search = q
	}
	s.listEbooks(w, r, p)
}

func (s *Server) listEbooks(w http.ResponseWriter, r *http.Request, p store.ListParams) {
	out, err := s.store.ListEbooks(r.Context(), p)
	if err != nil {
		s.serverError(w, "list ebooks", err)
		return
	}
	wire := Page[Book]{
		Items: make([]Book, len(out.Items)),
		Total: out.Total, Page: out.Page, Limit: out.Limit,
		NextCursor: nextCursor(out.Page, out.Limit, out.Total),
	}
	for i, e := range out.Items {
		b := ToBook(e)
		if b.HasCover {
			b.CoverURL = "/catalog/" + b.ID + "/cover"
		}
		wire.Items[i] = b
	}
	writeJSON(w, http.StatusOK, wire)
}

// Detail handles GET /catalog/{id}.
func (s *Server) Detail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	d, err := s.store.GetEbookByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.serverError(w, "get ebook", err)
		return
	}
	wire := ToBookDetail(d)
	if wire.HasCover {
		wire.CoverURL = "/catalog/" + wire.ID + "/cover"
	}
	if len(wire.Files) > 0 {
		wire.Files[0].URL = "/catalog/" + wire.ID + "/file"
	}
	writeJSON(w, http.StatusOK, wire)
}

// Cover handles GET /catalog/{id}/cover. The size query parameter is
// advisory; v1 always returns the stored bytes.
func (s *Server) Cover(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	// Route is declared public on the host plugin proxy; the signed media
	// token in ?token= is the auth gate. file_idx=-1 for cover.
	if _, err := tokens.Verify(s.secret, r.URL.Query().Get("token"), id, tokens.CoverFileIdx); err != nil {
		writeTokenError(w, err)
		return
	}
	bytes, contentType, err := s.store.GetCover(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no cover")
		return
	}
	if err != nil {
		s.serverError(w, "get cover", err)
		return
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(bytes)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)
}

// File handles GET /catalog/{id}/file. Streams the ebook from disk with a
// Content-Disposition: attachment header.
func (s *Server) File(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id required")
		return
	}
	// Route is declared public on the host plugin proxy; the signed media
	// token in ?token= is the auth gate. file_idx=0 — ebooks single-file.
	if _, err := tokens.Verify(s.secret, r.URL.Query().Get("token"), id, tokens.FileFileIdx); err != nil {
		writeTokenError(w, err)
		return
	}
	path, format, err := s.store.GetEbookPath(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		s.serverError(w, "get ebook path", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.logger.Warn("open ebook file", "id", id, "path", path, "err", err)
		writeError(w, http.StatusInternalServerError, "open file")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		s.serverError(w, "stat file", err)
		return
	}
	filename := sanitizeFilename(filepath.Base(path))
	if filename == "" {
		filename = id + "." + ExtForFormat(format)
	}
	w.Header().Set("Content-Type", FormatToMime(format))
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	// ServeContent honors Range/If-Range and emits 206/Accept-Ranges/
	// Content-Length — required because /capabilities advertises
	// supports_range_requests:true (resumable/seeking ereader downloads).
	http.ServeContent(w, r, filename, stat.ModTime(), f)
}

// Authors handles GET /catalog/authors.
func (s *Server) Authors(w http.ResponseWriter, r *http.Request) {
	p, ok := listParams(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid library_id")
		return
	}
	out, err := s.store.ListAuthors(r.Context(), p)
	if err != nil {
		s.serverError(w, "list authors", err)
		return
	}
	wire := Page[Author]{Items: make([]Author, len(out.Items)), Total: out.Total, Page: out.Page, Limit: out.Limit, NextCursor: nextCursor(out.Page, out.Limit, out.Total)}
	for i, a := range out.Items {
		wire.Items[i] = ToAuthor(a)
	}
	writeJSON(w, http.StatusOK, wire)
}

// SeriesList handles GET /catalog/series.
func (s *Server) SeriesList(w http.ResponseWriter, r *http.Request) {
	p, ok := listParams(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid library_id")
		return
	}
	out, err := s.store.ListSeries(r.Context(), p)
	if err != nil {
		s.serverError(w, "list series", err)
		return
	}
	wire := Page[Series]{Items: make([]Series, len(out.Items)), Total: out.Total, Page: out.Page, Limit: out.Limit, NextCursor: nextCursor(out.Page, out.Limit, out.Total)}
	for i, x := range out.Items {
		wire.Items[i] = ToSeries(x)
	}
	writeJSON(w, http.StatusOK, wire)
}

// Genres handles GET /catalog/genres.
func (s *Server) Genres(w http.ResponseWriter, r *http.Request) {
	p, ok := listParams(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid library_id")
		return
	}
	out, err := s.store.ListGenres(r.Context(), p)
	if err != nil {
		s.serverError(w, "list genres", err)
		return
	}
	wire := Page[Genre]{Items: make([]Genre, len(out.Items)), Total: out.Total, Page: out.Page, Limit: out.Limit, NextCursor: nextCursor(out.Page, out.Limit, out.Total)}
	for i, g := range out.Items {
		wire.Items[i] = ToGenre(g)
	}
	writeJSON(w, http.StatusOK, wire)
}

// --- helpers ---------------------------------------------------------------


func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// decodeCursor maps an opaque ?cursor= (base64 of a 1-based page number)
// back to a page. Empty/invalid -> 0 (normalized to page 1 downstream).
func decodeCursor(s string) int {
	if s == "" {
		return 0
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(b))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// nextCursor returns the cursor for the page after (page,limit) given total,
// or "" when there are no more rows (the portal stops paginating on absence).
func nextCursor(page, limit, total int) string {
	if limit <= 0 || page <= 0 || page*limit >= total {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(page + 1)))
}

// listParams builds ListParams from the request, accepting either ?page= or
// an opaque ?cursor=. ok=false means a present library_id was invalid — the
// caller returns 400 rather than silently dropping the filter and leaking
// the entire multi-library catalog.
func listParams(r *http.Request) (store.ListParams, bool) {
	q := r.URL.Query()
	p := store.ListParams{
		Library: q.Get("library"),
		Search:  q.Get("search"),
		Limit:   atoi(q.Get("limit")),
	}
	if c := q.Get("cursor"); c != "" {
		p.Page = decodeCursor(c)
	} else {
		p.Page = atoi(q.Get("page"))
	}
	if v := q.Get("library_id"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return store.ListParams{}, false
		}
		p.LibraryID = int64(n)
	}
	return p, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg}})
}

func (s *Server) serverError(w http.ResponseWriter, op string, err error) {
	s.logger.Error(op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// sanitizeFilename strips characters that are problematic in
// Content-Disposition header values. Replaces them with "_".
func sanitizeFilename(name string) string {
	// Allow letters, digits, dot, dash, underscore, space; replace others.
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
