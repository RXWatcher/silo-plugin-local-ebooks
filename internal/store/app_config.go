package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
)

type AppConfig struct {
	MetadataSourcesEnabled []string `json:"metadata_sources_enabled"`
	MetadataDefaultRegion  string   `json:"metadata_default_region"`
	MetadataCacheTTLDays   int      `json:"metadata_cache_ttl_days"`
	MetadataRateLimitRPS   int      `json:"metadata_rate_limit_rps"`
	ScanInlineEnrich       bool     `json:"scan_inline_enrich"`
	MetadataScanSource     string   `json:"metadata_scan_source"`
	GoogleBooksAPIKey      string   `json:"googlebooks_api_key"`
	ISBNdbAPIKey           string   `json:"isbndb_api_key"`
	HardcoverAPIKey        string   `json:"hardcover_api_key"`
}

func DefaultAppConfig() AppConfig {
	return AppConfig{
		MetadataSourcesEnabled: []string{
			"openlibrary", "googlebooks", "isbndb", "hardcover", "goodreads",
			"amazon", "annasarchive", "gutenberg", "bookbrainz", "fantasticfiction",
			"isfdb", "librarything", "internetarchive", "worldcat", "douban",
		},
		MetadataDefaultRegion: "us",
		MetadataCacheTTLDays:  30,
		MetadataRateLimitRPS:  5,
		MetadataScanSource:    "openlibrary",
	}
}

func (c AppConfig) WithDefaults() AppConfig {
	def := DefaultAppConfig()
	if len(c.MetadataSourcesEnabled) == 0 {
		c.MetadataSourcesEnabled = append([]string(nil), def.MetadataSourcesEnabled...)
	}
	if c.MetadataDefaultRegion == "" {
		c.MetadataDefaultRegion = def.MetadataDefaultRegion
	}
	if c.MetadataCacheTTLDays <= 0 {
		c.MetadataCacheTTLDays = def.MetadataCacheTTLDays
	}
	if c.MetadataRateLimitRPS <= 0 {
		c.MetadataRateLimitRPS = def.MetadataRateLimitRPS
	}
	if c.MetadataScanSource == "" {
		c.MetadataScanSource = def.MetadataScanSource
	}
	return c
}

func (c AppConfig) Validate() error {
	c = c.WithDefaults()
	for _, id := range c.MetadataSourcesEnabled {
		if !validMetadataSource(id) {
			return fmt.Errorf("metadata_sources_enabled contains unknown source %q", id)
		}
	}
	if !validMetadataSource(c.MetadataScanSource) {
		return fmt.Errorf("metadata_scan_source is not a valid source: %s", c.MetadataScanSource)
	}
	return nil
}

func validMetadataSource(id string) bool {
	switch id {
	case "openlibrary", "googlebooks", "isbndb", "hardcover", "goodreads",
		"amazon", "annasarchive", "gutenberg", "bookbrainz", "fantasticfiction",
		"isfdb", "librarything", "internetarchive", "worldcat", "douban":
		return true
	default:
		return false
	}
}

func (s *Store) GetAppConfig(ctx context.Context) (AppConfig, error) {
	cfg := DefaultAppConfig()
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM app_config WHERE id = 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg.WithDefaults(), nil
}

func (s *Store) PutAppConfig(ctx context.Context, cfg AppConfig) error {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO app_config (id, data, updated_at) VALUES (1, $1, now())
ON CONFLICT (id) DO UPDATE SET data = excluded.data, updated_at = now()`, raw)
	return err
}

func (s *Store) ImportLegacyAppConfig(ctx context.Context, cfg AppConfig) (bool, error) {
	current, err := s.GetAppConfig(ctx)
	if err != nil {
		return false, err
	}
	if !reflect.DeepEqual(current, DefaultAppConfig()) {
		return false, nil
	}
	legacy := cfg.WithDefaults()
	if reflect.DeepEqual(legacy, current) {
		return false, nil
	}
	if err := s.PutAppConfig(ctx, legacy); err != nil {
		return false, err
	}
	return true, nil
}
