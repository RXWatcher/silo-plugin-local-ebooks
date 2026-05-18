// Package runtime implements the plugin's Runtime gRPC server. Config holds
// the parsed plugin global config; main.go uses the onConfigure callback to
// re-init pool/store/server when config arrives.
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimedefault"
)

// Config is the parsed plugin global config.
type Config struct {
	DatabaseURL          string
	LibraryPaths         []string
	Libraries            []LibraryConfig
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

// LibraryConfig is one configured catalog root. The legacy library_paths
// setting may still be a plain string array; object entries allow the portal
// to present distinct Books/Comics/etc. libraries without changing the scanner
// contract.
type LibraryConfig struct {
	Path      string
	Name      string
	MediaType string
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
			cfg.Libraries = libraryConfigsFrom(val)
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
	if len(cfg.Libraries) == 0 {
		for _, path := range cfg.LibraryPaths {
			cfg.Libraries = append(cfg.Libraries, LibraryConfig{Path: path})
		}
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

// Snapshot returns the most recently applied Config. Slice fields are
// deep-copied so a caller can't mutate the locked config's backing arrays
// while a concurrent Configure rewrites s.cfg.
func (s *Server) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.LibraryPaths = append([]string(nil), s.cfg.LibraryPaths...)
	c.MetadataSourcesEnabled = append([]string(nil), s.cfg.MetadataSourcesEnabled...)
	c.Libraries = append([]LibraryConfig(nil), s.cfg.Libraries...)
	return c
}

func mask(s string) string {
	if s == "" {
		return ""
	}
	return "***redacted***"
}

// LogValue implements slog.LogValuer so slog.Any("cfg", c) never serializes
// the DSN (DB password), the stream-signing secret, or the API keys.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("database_url", mask(c.DatabaseURL)),
		slog.Any("library_paths", c.LibraryPaths),
		slog.String("standalone_http_listen", c.StandaloneHTTPListen),
		slog.String("stream_signing_secret", mask(c.StreamSigningSecret)),
		slog.Any("metadata_sources_enabled", c.MetadataSourcesEnabled),
		slog.String("metadata_default_region", c.MetadataDefaultRegion),
		slog.String("google_books_api_key", mask(c.GoogleBooksAPIKey)),
		slog.String("isbndb_api_key", mask(c.ISBNdbAPIKey)),
		slog.String("hardcover_api_key", mask(c.HardcoverAPIKey)),
	)
}

// String implements fmt.Stringer with the same redaction.
func (c Config) String() string { return c.LogValue().String() }

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

func libraryConfigsFrom(v any) []LibraryConfig {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]LibraryConfig, 0, len(arr))
	for _, item := range arr {
		switch x := item.(type) {
		case string:
			if x != "" {
				out = append(out, LibraryConfig{Path: x})
			}
		case map[string]any:
			path := stringFrom(x["path"])
			if path == "" {
				continue
			}
			out = append(out, LibraryConfig{
				Path:      path,
				Name:      stringFrom(x["name"]),
				MediaType: stringFrom(x["media_type"]),
			})
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
