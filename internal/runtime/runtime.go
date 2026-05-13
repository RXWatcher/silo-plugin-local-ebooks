// Package runtime implements the plugin's Runtime gRPC server. Config holds
// the parsed plugin global config; main.go uses the onConfigure callback to
// re-init pool/store/server when config arrives.
package runtime

import (
	"context"
	"errors"
	"sync"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimedefault"
)

// Config is the parsed plugin global config.
type Config struct {
	DatabaseURL          string
	LibraryPaths         []string
	StandaloneHTTPListen string
	StreamSigningSecret  string

	MetadataSourcesEnabled []string
	MetadataDefaultRegion  string
	MetadataCacheTTLDays   int
	MetadataRateLimitRPS   int
	ScanInlineEnrich       bool
	MetadataScanSource     string

	GoogleBooksAPIKey string
	ISBNdbAPIKey      string
	HardcoverAPIKey   string
}

// Server implements the plugin's Runtime service.
type Server struct {
	runtimedefault.Server
	manifest *pluginv1.PluginManifest
	onCfg    func(Config) error

	mu  sync.RWMutex
	cfg Config
}

// New constructs a runtime server. manifest may be nil in tests.
func New(manifest *pluginv1.PluginManifest, onConfigure func(Config) error) *Server {
	return &Server{manifest: manifest, onCfg: onConfigure}
}

func (s *Server) GetManifest(_ context.Context, _ *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *Server) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	cfg := Config{}
	for _, e := range req.GetConfig() {
		v := e.GetValue()
		if v == nil {
			continue
		}
		m := v.AsMap()
		val := m["value"]
		switch e.GetKey() {
		case "database_url":
			cfg.DatabaseURL = stringFrom(val)
		case "library_paths":
			cfg.LibraryPaths = stringSliceFrom(val)
		case "standalone_http_listen":
			cfg.StandaloneHTTPListen = stringFrom(val)
		case "stream_signing_secret":
			cfg.StreamSigningSecret = stringFrom(val)
		case "metadata_sources_enabled":
			cfg.MetadataSourcesEnabled = stringSliceFrom(val)
		case "metadata_default_region":
			cfg.MetadataDefaultRegion = stringFrom(val)
		case "metadata_cache_ttl_days":
			cfg.MetadataCacheTTLDays = intFrom(val)
		case "metadata_rate_limit_rps":
			cfg.MetadataRateLimitRPS = intFrom(val)
		case "scan_inline_enrich":
			cfg.ScanInlineEnrich = boolFrom(val)
		case "metadata_scan_source":
			cfg.MetadataScanSource = stringFrom(val)
		case "googlebooks_api_key":
			cfg.GoogleBooksAPIKey = stringFrom(val)
		case "isbndb_api_key":
			cfg.ISBNdbAPIKey = stringFrom(val)
		case "hardcover_api_key":
			cfg.HardcoverAPIKey = stringFrom(val)
		}
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("database_url is required")
	}
	if len(cfg.LibraryPaths) == 0 {
		return nil, errors.New("library_paths is required (non-empty array)")
	}
	// Apply defaults for metadata fields.
	if cfg.MetadataDefaultRegion == "" {
		cfg.MetadataDefaultRegion = "us"
	}
	if cfg.MetadataCacheTTLDays == 0 {
		cfg.MetadataCacheTTLDays = 30
	}
	if cfg.MetadataRateLimitRPS == 0 {
		cfg.MetadataRateLimitRPS = 5
	}
	if cfg.MetadataScanSource == "" {
		cfg.MetadataScanSource = "openlibrary"
	}
	if len(cfg.MetadataSourcesEnabled) == 0 {
		cfg.MetadataSourcesEnabled = []string{
			"openlibrary", "googlebooks", "isbndb", "hardcover", "goodreads",
			"amazon", "annasarchive", "gutenberg", "bookbrainz", "fantasticfiction",
			"isfdb", "librarything", "internetarchive", "worldcat", "douban",
		}
	}
	// Validate scan source against identifier-and-text-search-capable sources.
	// Permissive: any registered source is allowed.
	validScanSources := map[string]bool{
		"openlibrary": true, "googlebooks": true, "isbndb": true, "hardcover": true,
		"goodreads": true, "amazon": true, "annasarchive": true, "gutenberg": true,
		"bookbrainz": true, "fantasticfiction": true, "isfdb": true, "librarything": true,
		"internetarchive": true, "worldcat": true, "douban": true,
	}
	if !validScanSources[cfg.MetadataScanSource] {
		return nil, errors.New("metadata_scan_source is not a valid scan-capable source: " + cfg.MetadataScanSource)
	}
	if s.onCfg != nil {
		if err := s.onCfg(cfg); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return &pluginv1.ConfigureResponse{}, nil
}

// Snapshot returns the most recently applied Config.
func (s *Server) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func stringFrom(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringSliceFrom(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

func boolFrom(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

// Compile-time check that Server satisfies the SDK interface.
var _ pluginv1.RuntimeServer = (*Server)(nil)
