# Operations

Day-to-day operator runbook for `continuum-plugin-local-ebooks`. The README
covers what the plugin is and how to configure it; this file is the
"what do I press, what does it do, what breaks" reference. For ingest
failures by format see `troubleshooting.md`; for a UI tour see
`admin-ui.md`; for protocol detail see `setup-debug-flows.md`.

---

## 1. Postgres schema bootstrap

A dedicated role and schema, both keyed `local_ebooks`:

```sql
CREATE ROLE plugin_local_ebooks LOGIN PASSWORD '<set-something-strong>';
CREATE SCHEMA local_ebooks AUTHORIZATION plugin_local_ebooks;
GRANT CONNECT ON DATABASE continuum TO plugin_local_ebooks;
```

The DSN must set `search_path=local_ebooks`:

```
postgres://plugin_local_ebooks:<pwd>@db.internal:5432/continuum?search_path=local_ebooks&sslmode=disable
```

Migrations run on startup and are idempotent — re-running them against
an already-migrated schema is a no-op (tracked in
`local_ebooks.schema_migration`).

The role needs `CREATE TABLE` on its own schema for the first run. If
you restrict the role after install, restore it before any plugin upgrade
that ships a new migration.

---

## 2. Library configuration

Libraries are **owned by the plugin DB**. The `library_paths` config key
acts as a non-destructive seed: paths in the config that don't yet exist
in the DB are inserted on `Configure`, but paths *removed* from the
config are **not** auto-deleted. To remove a library, use the admin UI
(Libraries tab → Remove) — this deletes catalog rows but never touches
files on disk.

A library row has four fields you can edit:

| Field | Mutable | Notes |
| --- | --- | --- |
| `path` | no | Immutable after creation. To "rename" a path, add the new one and remove the old. |
| `name` | yes | Display name in admin and portal. |
| `media_type` | yes | `book` / `comics` / `manga` / `documents`. Classification only — does not change which extensions ingest. |
| `enabled` | yes | Hides from portal and excludes from global scans. Per-library scan button still runs. |

Library paths can be configured as plain strings or as objects:

```json
[
  {"path": "/srv/ebooks",    "name": "Books",  "media_type": "book"},
  {"path": "/srv/comics",    "name": "Comics", "media_type": "comics"},
  {"path": "/srv/handbooks", "name": "Docs",   "media_type": "documents"}
]
```

The admin filesystem browser (`GET /admin/filesystem/browse`) only lists
paths the plugin can actually see. If a path you expect is missing,
check the host mount first.

---

## 3. Library scanning

Three trigger paths:

| Trigger | Endpoint | Scope | Honors `enabled`? |
| --- | --- | --- | --- |
| Admin: Scan all libraries | `POST /admin/scan` | All libraries | Yes — disabled libraries skipped |
| Admin: per-library Scan button | `POST /admin/libraries/{id}/scan` | One library | **No** — override, runs even when disabled |
| Scheduled task | cron `0 */6 * * *` | All libraries | Yes |

The plugin does **not** auto-scan on first Configure. Trigger one
manually to populate the catalog after install.

Each scan:

- Soft-deletes files that disappeared since the last scan.
- Skips files whose `(size, mtime)` signature is unchanged. New ones
  parse, upsert, and enqueue for enrichment.
- Records `added / changed / deleted / failed` counts on a `scan_event`
  audit row. Per-file ingest failures show up as a
  `"<n> file(s) failed to ingest"` note on the row — drill into the
  scanner log for the per-file `slog` warnings.
- Conservatively *suppresses* soft-deletes if the filesystem walk itself
  hit unreadable subtrees, so missing visibility cannot delete
  still-present rows.

Progress and history: `GET /admin/scans` returns the recent scan_event
rows (newest first); the Scans tab in the admin UI displays the feed.

---

## 4. Metadata enrichment

The `metadata_enrichment_worker` scheduled task (cron `* * * * *`) drains
the `metadata_enrichment_job` queue. Each pass per ebook uses a single
source chosen by identifier strength:

1. **ISBN match** — if parser extracted an ISBN-10/ISBN-13,
   query `metadata_scan_isbn_source` (default `openlibrary`).
2. **ASIN match** — if no ISBN but an ASIN is present (common for
   MOBI/AZW/AZW3), query `metadata_scan_asin_source` (default
   `amazon`).
3. **Text fallback** — neither identifier present: query
   `metadata_scan_source` (default `openlibrary`) with title + primary
   author.

A single pass is single-source: failure or low confidence does *not*
cascade to a different source within the same pass. To try a different
source, change the relevant config key and re-enqueue (backfill).

### Enabled sources

`metadata_sources_enabled` is a JSON array of source IDs (defaults to
all). The list is validated on save against the registered source set.
Sources that require an API key are silently disabled when the key is
blank — they will never be queried even if listed as enabled.

### Triggering backfill

```
POST /admin/metadata/backfill
```

Enqueues every ebook. The worker picks them up at the next minute.
Use after:

- Adding an API key (so previously skipped sources can run).
- Changing `metadata_scan_source` / `_isbn_source` / `_asin_source`.
- Changing `metadata_default_region` (region-aware sources cache per
  region).
- Bulk-purging the metadata cache.

### Rate limiting and caching

| Key | Default | Purpose |
| --- | --- | --- |
| `metadata_rate_limit_rps` | `5` | Per-source outbound RPS ceiling. |
| `metadata_cache_ttl_days` | `30` | Positive-result TTL. Negative results use a shorter internal TTL. |
| `metadata_default_region` | `us` | ISO country code; matters for Amazon, Goodreads, Douban. |

Expired cache rows evict at the tail of each library scan, before the
scan event closes. No standalone eviction cron — if scans never run,
expired rows sit until read time.

### Inline enrichment

`scan_inline_enrich = true` runs enrichment synchronously after a scan
finishes. Suitable for small libraries; for large ones the scan event
will not close until every newly-enqueued ebook has been processed,
which can block the scheduler for a long time. Leave off for libraries
above a few thousand titles.

---

## 5. Troubleshooting

### A scrape source is failing

The HTML-scrape sources (Goodreads, Amazon, Anna's Archive,
FantasticFiction, ISFDB, LibraryThing, WorldCat, Douban) are
best-effort. The aggregator logs the failure and continues. No SLA. If
a specific source is critical, watch the plugin log; if it's just
noise, drop it from `metadata_sources_enabled`.

### Metadata is not appearing on an ebook

Check, in order:

1. **`metadata_enrichment_job` row** — is there one for the `ebook_id`?
   Scans only auto-enqueue ebooks that lack a prior successful
   enrichment. Force one with backfill.
2. **`status` and `last_error`** — a permanent failure (401 from a
   keyed source, repeated 5xx) parks the row.
3. **`metadata_sources_enabled`** — is the source you expect enabled?
4. **API key** — Google Books falls back to anonymous quota; ISBNdb and
   Hardcover are silently disabled without a key.
5. **`metadata_cache`** — a cached negative result suppresses re-querying
   until TTL elapses. Delete the row and re-run backfill.
6. **Identifier presence** — for ISBN-required sources, the parser must
   have extracted an ISBN. For PDFs that lack metadata, this commonly
   fails; fall back to a text-search source.

### `database_url` rejected at Configure

The DSN must include `search_path=local_ebooks` and the role must own
the schema. Migrations also need `CREATE TABLE` on that schema; if you
restricted the role after install, grant it back temporarily.

### Signed cover/file URLs return 401

`stream_signing_secret` (this plugin) and `media_signing_secret`
(portal) must match byte-for-byte. Watch for trailing whitespace from
copy-paste. Rotating either side invalidates all in-flight signed URLs
— coordinate rotation with portal restarts.

### Scans report "N file(s) failed to ingest"

Drill in via the plugin log: the per-file failure prints at WARN with
the path. The most common causes are format-specific — see
`troubleshooting.md`.

### Library missing from the portal but visible in admin

The portal filters on `enabled = TRUE`. Re-enable in the Libraries tab
(or `PATCH /admin/libraries/{id}` with `enabled: true`).

### Library row reappeared after deletion

Removing only deletes the *DB row*. If the host's `library_paths`
config still lists that path, the next `Configure` will re-seed it. To
permanently remove, drop it from the config blob too — or just let the
DB stay authoritative and rely on the admin UI.

---

## 6. Backups

The plugin schema contains the catalog index, cover bytes, scan
history, the metadata cache, and the enrichment queue. The on-disk
ebook files are authoritative content — losing the schema is
*recoverable* via a rescan (file metadata re-parses from each file).
Covers from sidecar files are recoverable; embedded covers re-extract
from each file's container.

What is **not** automatically recoverable: the metadata cache and any
in-flight enrichment queue rows. A fresh schema after DR will re-fetch
from external sources subject to rate limits — for a large library
this can take hours and burn through Google Books / ISBNdb quotas.

```
pg_dump --schema=local_ebooks continuum > local_ebooks.sql
```

…run periodically is sufficient to skip the rescan + re-enrichment
cost after a DR event.

The host's global-config (DSN, signing secret, API keys) is **not**
inside this schema. Back it up separately via the Continuum host.
