# Local Ebooks for Continuum

`continuum.local-ebooks` is the filesystem-backed ebook backend for Continuum. It scans configured library directories on disk, ingests EPUB / PDF / MOBI / AZW / AZW3 / FB2 files, exposes catalog, cover, and file endpoints to the Ebooks portal, and ships an in-process metadata aggregator that fans out across fifteen external book-data sources.

The user-facing reader, OPDS/Kobo/Kindle integrations, and request workflow live in [`continuum-plugin-ebooks`](https://github.com/RXWatcher/continuum-plugin-ebooks). This plugin owns local discovery, ingestion, metadata, covers, and signed file access.

## Category

Lives under **Books / Ebooks** in the plugin catalog.

## Capabilities

| Type | ID | Purpose |
| --- | --- | --- |
| `ebook_backend.v1` | `local_ebooks` | Surfaces the local library as a `library_source` to the Ebooks portal (`supports_catalog`, no requests, no auto-monitoring). |
| `metadata_provider.v1` | `local_ebooks_meta` | Aggregator that searches fifteen bundled book metadata sources; default book priority `1`. |
| `scheduled_task.v1` | `library_scan` | Walks every enabled library path and upserts discovered titles. Cron `0 */6 * * *`. |
| `scheduled_task.v1` | `metadata_enrichment_worker` | Drains the enrichment queue, filling missing fields from the bundled providers. Cron `* * * * *`. |
| `http_routes.v1` | `admin` | Operator UI for libraries, scans, metadata settings, and diagnostics. |

HTTP surface declared in the manifest:

- `GET /assets/*`, `GET /api/v1/file/*`, `GET /api/v1/cover/*`, `GET /catalog/*` — public (signed where needed).
- `* /api/v1/*` — authenticated, consumed by the portal.
- `GET /admin` (navigable, labelled "Local Ebooks") and `* /admin/*` — admin-only.

## Dependencies

- Consumed by [`continuum-plugin-ebooks`](https://github.com/RXWatcher/continuum-plugin-ebooks) (the portal) as an ebook backend and as a metadata provider. The portal must share the same `stream_signing_secret` so its `media_signing_secret` validates signed cover and file URLs issued here.
- Standalone otherwise — the admin UI, scanner, and metadata aggregator all run without the portal installed, and an optional direct catalog listener is available via `standalone_http_listen`.
- Host: [`github.com/ContinuumApp/continuum`](https://github.com/ContinuumApp/continuum). SDK: [`github.com/ContinuumApp/continuum-plugin-sdk`](https://github.com/ContinuumApp/continuum-plugin-sdk).
- Sibling ebook plugins: [`continuum-plugin-ebooks`](https://github.com/RXWatcher/continuum-plugin-ebooks) (portal), [`continuum-plugin-bookwarehouse-ebook`](https://github.com/RXWatcher/continuum-plugin-bookwarehouse-ebook), [`continuum-plugin-ebook-requests`](https://github.com/RXWatcher/continuum-plugin-ebook-requests).

## External services

- **Filesystem mounts** — one or more host directories mounted into the plugin runtime and listed in `library_paths`. Paths are validated container-side; the admin UI's filesystem browser lists what is visible to the plugin.
- **Postgres** — a dedicated schema (`local_ebooks` by convention) reached via `database_url`. Migrations run on startup.
- **Metadata provider APIs** — HTTPS calls to the fifteen sources listed below. Three accept API keys (`googlebooks_api_key`, `isbndb_api_key`, `hardcover_api_key`); the rest are anonymous and rate-limited per source.

## Supported file formats

The scanner ingests files with these extensions; everything else is ignored:

- `.epub`
- `.pdf`
- `.mobi`, `.azw`, `.azw3`
- `.fb2`

Library entries can also be tagged with a `media_type` of `book`, `comics`, `manga`, or `documents`. The CBR/CBZ comic ingest path is reserved in the manifest description as a future addition and is not yet exercised by the parser.

## Library scanning

- `scanner.Walk` enumerates each enabled library root, hashes file content (BLAKE2b) and metadata, parses recognized formats via `internal/ebookparse`, and upserts ebook + file + cover rows.
- Rows whose files have disappeared are soft-deleted; partial walk errors are recorded on the `scan_event` audit row instead of being masked as success, and per-file ingest failures are surfaced as a `"<n> file(s) failed to ingest"` note on the same row.
- A global scan honors the `enabled` flag on each library path; the per-library admin "Scan" button is an explicit override and runs even for disabled libraries (disabled only suppresses portal exposure).
- After a scan, if `scan_inline_enrich` is set, the enrichment worker is drained synchronously and the metadata cache evicts expired entries.

## Metadata providers

The aggregator (`internal/metadata/sources`) ships with fifteen providers, all registered in `cmd/continuum-plugin-local-ebooks/main.go`:

- **OpenLibrary** — anonymous JSON API; primary fallback source.
- **GoogleBooks** — uses `googlebooks_api_key` if set, otherwise anonymous quota.
- **ISBNdb** — requires `isbndb_api_key`.
- **Hardcover** — GraphQL; requires `hardcover_api_key`.
- **Goodreads** — public web data.
- **Amazon** — public listings via product pages.
- **Anna's Archive** — open library mirror lookups.
- **Project Gutenberg** — public-domain catalog.
- **BookBrainz** — MetaBrainz book project.
- **FantasticFiction** — author bibliographies.
- **ISFDB** — Internet Speculative Fiction Database.
- **LibraryThing** — community catalog data.
- **Internet Archive** — Open Library + IA book metadata.
- **WorldCat** — OCLC catalog lookups.
- **Douban** — Chinese-language book catalog.

Results are merged in the aggregator, scored via `confidence.go`, cached in Postgres with a TTL, and rate-limited per source. The `metadata_sources_enabled` config narrows which providers are queried; `metadata_scan_source` selects which one the enrichment worker prefers during library scans.

## Admin UI

`internal/server/admin_home.go` ships a single-file HTML/CSS/JS admin at `GET /admin` (mounted as the host's navigable "Local Ebooks" item). The page is a four-tab shell:

- **Libraries** — add / remove / rename paths, toggle enabled, pick `media_type`, browse container paths, and trigger per-library scans.
- **Scans** — global scan trigger plus a feed of recent `scan_event` rows.
- **Metadata** — pick enabled sources, scan source, default region, cache TTL, per-source RPS, inline-enrichment toggle, and paste API keys for Google Books / ISBNdb / Hardcover (secrets are never echoed back into the form).
- **Diagnostics** — DB health, catalog totals, queue depth, last and active scan.

Above the tabs is a status strip (DB / Libraries / Sources / Last scan / Active scan). The same template skeleton is shared with the sibling `bw-audio`, `bw-ebook`, and `local-audiobooks` plugins so operators see consistent chrome.

## Configuration

| Key | Required | Description |
| --- | --- | --- |
| `database_url` | yes | Postgres DSN for the `local_ebooks` schema. Declared in `global_config_schema` as a secret password field. |
| `stream_signing_secret` | no | HMAC secret shared with the portal's `media_signing_secret`; required for signed cover and file URLs to validate. |
| `library_paths` | yes | JSON array of either strings or objects with `path`, `name`, `media_type` (`book` / `comics` / `manga` / `documents`). |
| `standalone_http_listen` | no | Optional listener for direct catalog access when the portal is absent. |
| `metadata_sources_enabled` | no | Array of source IDs to query. Defaults to all bundled providers. |
| `metadata_default_region` | no | ISO country code; defaults to `us`. |
| `metadata_cache_ttl_days` | no | Positive cache TTL; defaults to `30`. |
| `metadata_rate_limit_rps` | no | Per-source request rate limit. |
| `scan_inline_enrich` | no | Drain the enrichment worker synchronously after each scan. |
| `metadata_scan_source` | no | Source used by the enrichment worker during scans. |
| `googlebooks_api_key` | no | Optional Google Books API key. |
| `isbndb_api_key` | no | ISBNdb API key (required to query ISBNdb). |
| `hardcover_api_key` | no | Hardcover GraphQL API key (required to query Hardcover). |

Example `library_paths`:

```json
[
  {"path": "/srv/ebooks",   "name": "Books",  "media_type": "book"},
  {"path": "/srv/comics",   "name": "Comics", "media_type": "comics"},
  {"path": "/srv/handbooks","name": "Docs",   "media_type": "documents"}
]
```

Postgres bootstrap:

```sql
CREATE ROLE plugin_local_ebooks WITH LOGIN PASSWORD '<chosen>';
CREATE SCHEMA local_ebooks AUTHORIZATION plugin_local_ebooks;
GRANT CONNECT ON DATABASE continuum TO plugin_local_ebooks;
```

## Detailed docs

- [Setup, debugging, and communication flows](docs/setup-debug-flows.md)
- [Operations](docs/operations.md)

## Build and release

```bash
make build   # go build -o continuum-plugin-local-ebooks ./cmd/continuum-plugin-local-ebooks
make test    # go test ./...
make fmt
```

CI builds linux-amd64 binaries on push to main via the reusable workflow in [RXWatcher/continuum-plugin-repository](https://github.com/RXWatcher/continuum-plugin-repository) and publishes them to the catalog at [`./binaries/`](https://github.com/RXWatcher/continuum-plugin-repository/tree/main/binaries).
