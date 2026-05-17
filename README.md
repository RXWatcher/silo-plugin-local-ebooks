# Local Ebooks for Continuum

`continuum.local-ebooks` scans local ebook and document folders and exposes
them to the Continuum Ebooks portal as an `ebook_backend.v1` source. It is the
right backend when EPUB, PDF, comic, or document files live on disk next to the
Continuum deployment.

The user-facing web app, OPDS/Kobo/Kindle integrations, request workflow, and
cache management come from `continuum.ebooks`; this plugin owns local library
scanning, metadata, cover data, and file access.

## Detailed Operations Docs

- [Setup, debugging, and communication flows](docs/setup-debug-flows.md)

## Features

- Scans one or more configured local library paths.
- Supports legacy string paths or object entries with `path`, `name`, and
  `media_type`.
- Exposes catalog, search, detail, cover, and file access to the Ebooks portal.
- Aggregates metadata from OpenLibrary, Google Books, ISBNdb, Hardcover,
  Goodreads, Amazon, Anna's Archive, Project Gutenberg, BookBrainz,
  FantasticFiction, ISFDB, LibraryThing, Internet Archive, WorldCat, and
  Douban.
- Supports metadata caching, per-source rate limits, and scheduled enrichment.
- Optional standalone direct catalog access listener.

## Configuration

| Key | Required | Description |
|---|---|---|
| `database_url` | yes | Postgres DSN for the `local_ebooks` schema. |
| `library_paths` | yes | JSON library paths, either strings or objects with `path`, `name`, and `media_type`. |
| `standalone_http_listen` | no | Optional listener for direct catalog access. |
| `stream_signing_secret` | no | Reserved HMAC secret for signed download URLs. |
| `metadata_sources_enabled` | no | JSON array of metadata source IDs to query. Defaults to all. |
| `metadata_default_region` | no | Default ISO country code. Defaults to `us`. |
| `metadata_cache_ttl_days` | no | Positive metadata cache TTL. Defaults to 30 days. |
| `metadata_rate_limit_rps` | no | Per-source request rate limit. |
| `scan_inline_enrich` | no | Run enrichment synchronously after each scan. |
| `metadata_scan_source` | no | Source used by the enrichment worker during scans. |
| `googlebooks_api_key` | no | Optional Google Books API key. |
| `isbndb_api_key` | no | Optional ISBNdb API key. |
| `hardcover_api_key` | no | Optional Hardcover API key. |

Example library config:

```json
[
  {"path": "/srv/ebooks", "name": "Books", "media_type": "book"},
  {"path": "/srv/comics", "name": "Comics", "media_type": "comics"}
]
```

## Database Setup

```sql
CREATE ROLE plugin_local_ebooks WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA local_ebooks AUTHORIZATION plugin_local_ebooks;
GRANT CONNECT ON DATABASE continuum TO plugin_local_ebooks;
```

## Portal Integration

1. Mount ebook files into the plugin runtime.
2. Configure `library_paths`.
3. Install and configure `continuum.ebooks`.
4. Select `continuum.local-ebooks` as a source or request provider in the
   Ebooks portal settings.

## Build And Test

```bash
make build
make test
```
