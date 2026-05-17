# Local Ebooks Setup, Debugging, And Flows

Plugin ID: `continuum.local-ebooks`
Version documented: `0.1.0`

## Purpose

local filesystem ebook/document backend for continuum.ebooks.

## Runtime Dependencies

- Continuum plugin host
- Postgres schema for this plugin
- Mounted ebook/document folders visible to the plugin runtime
- continuum.ebooks for the user-facing portal

## Setup Checklist

1. Create schema and configure database_url.
2. Mount ebook folders into the runtime.
3. Configure library_paths as strings or objects with path/name/media_type.
4. Add optional metadata API keys for Google Books, ISBNdb, or Hardcover.
5. Run library_scan or wait for the scheduled task.
6. Select this backend in continuum.ebooks.

## Configuration Reference

- `database_url`
- `library_paths`
- `standalone_http_listen`
- `stream_signing_secret`
- `metadata_sources_enabled`
- `metadata_default_region`
- `metadata_cache_ttl_days`
- `metadata_rate_limit_rps`
- `scan_inline_enrich`
- `metadata_scan_source`
- `googlebooks_api_key`
- `isbndb_api_key`
- `hardcover_api_key`

Use the plugin manifest/admin form as the source of truth for field validation and defaults. Keep database credentials scoped to the plugin schema unless a plugin explicitly needs read access to Continuum core tables.

## Exposed Routes

- `* /api/v1/* [authenticated]`
- `* /admin/* [admin]`

## Capabilities

- `ebook_backend.v1 (local_ebooks) - Scans local ebook, document, and future comic libraries and exposes them to the Ebooks portal.`
- `metadata_provider.v1 (local_ebooks_meta) - Searches OpenLibrary, GoogleBooks, ISBNdb, Hardcover, Goodreads, Amazon, Anna's Archive, Project Gutenberg, BookBrainz, FantasticFiction, ISFDB, LibraryThing, Internet Archive, WorldCat, and Douban.`
- `scheduled_task.v1 (library_scan) - Periodic library rescan`
- `scheduled_task.v1 (metadata_enrichment_worker) - Metadata enrichment worker`

## Operational Flows

### Scan/catalog

1. The scanner walks configured paths and identifies EPUB/PDF/document/comic files.
2. It extracts local metadata and stores normalized catalog rows.
3. Ebooks portal calls this backend for catalog, search, details, covers, and files.

### Metadata enrichment

1. Configured metadata sources are queried with cache and rate limits.
2. Better title/author/cover/identifier data is merged into local records.

### File delivery

1. Ebooks portal requests a file.
2. The backend returns file access or streams from local storage; the portal may cache before serving to readers.

## How This Plugin Communicates

- Implements ebook_backend.v1 and metadata_provider.v1 for continuum.ebooks.
- Does not own OPDS/Kobo/Kindle routes; the Ebooks portal does.
- Shares scan/enrichment results through backend API calls.

## Debugging Runbook

- If books do not appear, verify mounts and media_type/path JSON.
- If metadata is poor, enable sources gradually and check API keys/rate limits.
- If downloads fail, check file permissions and path remappings in the Ebooks portal.
- Use docs/operations.md for scanner and metadata operations.

## Log And Health Checks

- Start with Continuum Admin -> Plugins and confirm the installation is enabled.
- Check the plugin process logs around startup for manifest loading, migration, and route registration.
- Check scheduled task logs when a workflow depends on polling or reconciliation.
- Confirm the plugin routes are reachable through Continuum using the access level shown above.
- For database-backed plugins, verify the configured role can connect, create/migrate tables in its schema, and read/write expected rows.

## Common Failure Patterns

- Wrong installation ID selected in a portal or router setting after reinstalling a plugin.
- Plugin database URL points at the public schema instead of the dedicated plugin schema.
- Reverse proxy forwards the SPA route but not `/api/*`, `/api/v1/*`, `/assets/*`, or provider-specific public routes.
- Network checks are run from the operator laptop instead of from the Continuum/plugin runtime network.
- Secrets are regenerated during restart, invalidating signed URLs, encrypted fields, or login state.

## Verification After Changes

1. Restart or reload the plugin installation.
2. Open the plugin route or admin page in Continuum.
3. Exercise the smallest workflow that crosses a plugin boundary.
4. Confirm both the source plugin and destination plugin record the same request/session/login identifier.
5. Leave the scheduled reconciler enough time to run, then confirm terminal state or a useful error.
