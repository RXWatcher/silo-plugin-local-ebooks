// Package sources holds the per-upstream metadata adapters. Each source
// satisfies the Source interface; the aggregator orchestrates them.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
)

// redactURL drops the query string before a URL is put in an error message.
// Google Books / ISBNdb / Hardcover carry the API key in the query (?key=…),
// and these errors are logged by the enrichment worker.
func redactURL(raw string) string {
	if base, _, found := strings.Cut(raw, "?"); found {
		return base + "?<redacted>"
	}
	return raw
}

// SoftLimit on response body size to cap surprise payloads.
const SoftLimit = 1 << 20 // 1 MB

// DefaultTimeout is the per-HTTP-request timeout.
const DefaultTimeout = 10 * time.Second

// Source is the uniform interface every per-upstream adapter implements.
type Source interface {
	ID() string                                                                      // stable lower-case slug
	Enabled(cfg map[string]bool) bool                                                // checks ID against enabled set
	Search(ctx context.Context, query, region string) ([]metadata.Candidate, error)  // returns 0+ candidates
	Get(ctx context.Context, externalID, region string) (*metadata.Candidate, error) // nil + nil if not found
}

// ErrNotFound is the canonical signal a Get found nothing (cached as not_found).
var ErrNotFound = errors.New("source: not found")

// HTTPClient is shared by sources; configurable for testing.
type HTTPClient struct {
	BaseURL string
	Client  *http.Client
	UA      string
}

// NewHTTPClient builds a client with the standard timeout + UA.
func NewHTTPClient(baseURL, ua string) *HTTPClient {
	return &HTTPClient{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: DefaultTimeout},
		UA:      ua,
	}
}

// GetJSON does GET, returns body bytes (capped) and HTTP status.
// 404 is reported as ErrNotFound; other non-2xx as fmt'd errors.
func (h *HTTPClient) GetJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if h.UA != "" {
		req.Header.Set("User-Agent", h.UA)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, SoftLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("source: GET %s status %d", redactURL(url), resp.StatusCode)
	}
	return body, nil
}

// GetJSONWithHeaders does GET with additional request headers, otherwise
// identical to GetJSON. Used by keyed sources that require non-standard auth
// headers (e.g. ISBNdb uses a bare Authorization header without Bearer prefix).
func (h *HTTPClient) GetJSONWithHeaders(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if h.UA != "" {
		req.Header.Set("User-Agent", h.UA)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, SoftLimit))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("source: GET %s status %d", redactURL(url), resp.StatusCode)
	}
	return body, nil
}

// UnmarshalInto parses JSON into v with a clear error.
func UnmarshalInto(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("source: json decode: %w", err)
	}
	return nil
}
