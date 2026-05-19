CREATE TABLE IF NOT EXISTS app_config (
  id         INT PRIMARY KEY DEFAULT 1,
  data       JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT app_config_singleton CHECK (id = 1)
);

INSERT INTO app_config (id, data)
VALUES (
  1,
  '{
    "metadata_sources_enabled": [
      "openlibrary",
      "googlebooks",
      "isbndb",
      "hardcover",
      "goodreads",
      "amazon",
      "annasarchive",
      "gutenberg",
      "bookbrainz",
      "fantasticfiction",
      "isfdb",
      "librarything",
      "internetarchive",
      "worldcat",
      "douban"
    ],
    "metadata_default_region": "us",
    "metadata_cache_ttl_days": 30,
    "metadata_rate_limit_rps": 5,
    "scan_inline_enrich": false,
    "metadata_scan_source": "openlibrary",
    "googlebooks_api_key": "",
    "isbndb_api_key": "",
    "hardcover_api_key": ""
  }'::jsonb
)
ON CONFLICT (id) DO NOTHING;
