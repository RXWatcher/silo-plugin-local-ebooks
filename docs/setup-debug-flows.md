# Setup, Debugging, and Communication Flows

Operator-focused companion to the README. The README covers what the plugin
is, its capabilities, configuration keys, and the metadata source list. This
file goes deeper on **how the parts wire together** and **where things fail
in practice**.

If you just need a quick task recipe, see `operations.md` (Postgres bootstrap,
scan triggers, backfill). For ingest-failure patterns by file format see
`troubleshooting.md`. For a tour of the admin console see `admin-ui.md`.

---

## 1. Runtime layout

The plugin process registers five SDK capabilities and binds them to one
HTTP mux:

```
                ┌─────────────────────────────────────────┐
                │ continuum-plugin-local-ebooks (process) │
                └─────────────────────────────────────────┘
                                  │
        ┌─────────────────────────┼─────────────────────────────────┐
        ▼                         ▼                                 ▼
 ebook_backend.v1          metadata_provider.v1            scheduled_task.v1
 (local_ebooks)            (local_ebooks_meta)             (library_scan, *workers)
        │                         │                                 │
        ▼                         ▼                                 ▼
 internal/server          internal/metadata/aggregator      internal/scheduler
        │                         │                                 │
        ▼                         ▼                                 ▼
 internal/store ─────────► Postgres `local_ebooks` schema ◄─────────┘
        │
        ▼
 internal/scanner ──► internal/ebookparse ──► filesystem (library_paths)
```

Two HTTP servers can host the same mux:

- **Host-mounted** (always on) — the Continuum host loads the manifest's
  `http_routes.v1` declaration and proxies traffic to the plugin via gRPC.
  Public/authenticated/admin scopes are enforced by the host.
- **Standalone listener** (opt-in via `standalone_http_listen`) — the
  plugin binds its own listener and serves the *same* handler tree
  (`internal/httproutes/server.go`). Useful when the portal is offline or
  when running the catalog directly from another integration. The
  standalone handler strips `X-Continuum-*` headers from inbound requests
  to avoid header smuggling.

When both are active, both serve from the same in-memory state — there is
no cache divergence.

---

## 2. Where state lives

| Concern | Location | Notes |
| --- | --- | --- |
| Library configuration (path, name, media_type, enabled) | Postgres `library_path` | Authoritative. `library_paths` config is a non-destructive seed only. |
| Catalog (ebook, file, cover) | Postgres `ebook`, `ebook_file`, `cover` | Re-derivable by rescanning. |
| Scan history / audit | Postgres `scan_event` | Includes per-walk `added/changed/deleted/failed` counts. |
| Metadata cache | Postgres `metadata_cache` | Per-source + query hash; TTL = `metadata_cache_ttl_days`. |
| Enrichment queue | Postgres `metadata_enrichment_job` | Drained by the worker each minute. |
| App config (sources enabled, region, RPS, TTL, scan-source) | Postgres `app_config` | Edited via the admin Metadata tab. |
| Secrets (DSN, signing secret, API keys) | Host global-config | Never persisted in plugin schema; never echoed in admin responses. |

Only the filesystem and the Postgres schema are durable. The metadata
cache and enrichment queue *are not* rebuilt automatically after DR —
plan accordingly (see `operations.md` §Backups).

---

## 3. Supported file formats and media types

The scanner ingests these extensions and ignores everything else:

| Extension | Parser | Module |
| --- | --- | --- |
| `.epub` | OPF metadata + cover from container.xml | `internal/ebookparse/epub.go` |
| `.pdf` | XMP / info dict + first-page cover | `internal/ebookparse/pdf.go` (panic-recovered) |
| `.mobi`, `.azw`, `.azw3` | PalmDB record 0 header | `internal/ebookparse/mobi.go` |
| `.fb2` | XML root metadata | `internal/ebookparse/fb2.go` |

CBR/CBZ comic ingest is reserved in the manifest description but not yet
implemented; `media_type=comics` libraries currently match nothing on disk.

Each `library_path` row carries a `media_type` from the closed set in
`internal/libcfg/libcfg.go`:

```
book  |  comics  |  manga  |  documents
```

The `media_type` is a *classification* — it does not change which file
extensions the scanner accepts. The portal uses it to group libraries
into reader shelves; per-library media_type does not gate ingest.

---

## 4. Library scan semantics

Two trigger paths reach `scanner.Walk`:

- `POST /admin/scan` — global. Iterates **enabled** libraries only.
  Disabled libraries are skipped here so toggling "Enabled" off both
  hides a library from the portal *and* stops scheduled work against it.
- `POST /admin/libraries/{id}/scan` — per-library. **Runs even when the
  library is disabled.** This is an explicit operator override; useful
  for staging a library before exposing it to the portal.

Per scan:

1. `ListEbookRefs` loads (id, path, content_sig) for every existing file
   in this library. The map keys soft-delete decisions.
2. The filesystem walk skips non-regular files (symlinks rejected to
   avoid the symlink-escape vector) and any extension not in the
   supported set above.
3. For each known file, if `(size, mtime)` signature matches the stored
   sig, the file is marked seen and skipped — no reparse, no re-enqueue.
   This is critical: without it, every scheduled scan would reset every
   already-enriched ebook back to pending.
4. New or changed files are parsed. Parse failures bump
   `WalkResult.Failed` and are surfaced as
   `"<n> file(s) failed to ingest"` on the closing `scan_event` row.
5. Files that vanished between scans are *soft-deleted* (the ebook row
   flips to `deleted = TRUE`). Files that reappear later are reactivated
   on the next scan.
6. If a walk *itself* errored (unreadable subtree), the scan is closed
   with `walk_had_errors = TRUE` and per-file soft-deletes are
   conservatively suppressed — losing visibility cannot delete still-
   present rows.
7. After the walk, if `scan_inline_enrich` is set, the enrichment worker
   drains synchronously and the metadata cache evicts expired entries
   before the scan event closes.

The portal's view (`catalog` queries) filters on `lp.enabled = TRUE` and
`e.deleted = FALSE`. Soft-deleted rows persist in the DB for diagnostics
and reactivation.

---

## 5. Metadata source coordination

Fifteen sources live under `internal/metadata/sources/`, all registered
in `cmd/continuum-plugin-local-ebooks/main.go` and listed in the README.
Three accept API keys:

| Source | Key | Behavior when missing |
| --- | --- | --- |
| Google Books | `googlebooks_api_key` | Falls back to anonymous quota. Aggregate caps still apply. |
| ISBNdb | `isbndb_api_key` | Source is **silently disabled** — `Search` returns no results from it and the worker skips it. |
| Hardcover | `hardcover_api_key` | Same — silently disabled. |

The remaining twelve sources are anonymous and rate-limited per source.
The six HTML-scrape sources (Goodreads, Amazon, Anna's Archive, Fantastic
Fiction, ISFDB, LibraryThing, WorldCat, Douban) are best-effort; site
markup changes break them and the aggregator just logs and continues.

`metadata_sources_enabled` narrows which providers are *queried*.
`metadata_scan_source` picks which provider the enrichment worker
prefers during library scans (cascade order: ISBN match → ASIN match →
text fallback, with separate `_isbn_source` / `_asin_source` keys; see
`operations.md` §Metadata enrichment).

---

## 6. Stream signing secret coordination

`stream_signing_secret` is an HMAC secret shared with the Ebooks portal.
The portal exposes the same value as `media_signing_secret`. The contract:

- The portal *issues* signed URLs (`/api/v1/file/...`, `/api/v1/cover/...`)
  using its secret.
- This plugin *verifies* them with the matching secret in
  `internal/tokens/verify.go`. If the secret is empty, verification
  returns `ErrSecretUnconfigured` and signed downloads fail with HTTP
  401 even if the URL itself is well-formed.
- The secret is never logged (`runtime.LogValue` masks it) and never
  echoed back through the admin Metadata form.

Operational rules:

- The two values **must match byte-for-byte**. A copy-paste with
  trailing whitespace will break signing.
- Rotating the secret invalidates every in-flight signed URL. Coordinate
  rotation with portal restarts to avoid a "links work, downloads 401"
  symptom window.
- If you run the plugin standalone (no portal), this key is optional —
  you simply cannot serve signed downloads through that route.

---

## 7. Standalone HTTP listener

Setting `standalone_http_listen` (e.g. `:8089` or `127.0.0.1:8089`) binds
a plain HTTP server on the plugin process, serving the same handler as
the host-mounted routes. Use cases:

- Direct OPDS-style catalog access from another plugin or tool while the
  Ebooks portal is offline or not installed.
- Local development against the catalog without standing up the full
  Continuum host stack.
- Health probing from an external monitor that does not route through
  the Continuum host.

The listener has no built-in auth — protect it with a network ACL or
reverse-proxy in front of it. The `X-Continuum-*` header strip
(`internal/httproutes/server.go`) prevents the standalone path from
being abused to forge host-context headers.

Leave the value empty (default) to disable.

---

## 8. Plugin ↔ portal communication

```
┌───────────────────────────────┐                 ┌──────────────────────────────┐
│ continuum-plugin-ebooks       │                 │ continuum-plugin-local-      │
│ (portal)                      │                 │ ebooks (this plugin)         │
└───────────────────────────────┘                 └──────────────────────────────┘
              │                                                  │
              │ ebook_backend.v1 (catalog, search, details,      │
              │ cover URL, file URL)                             │
              ├─────────────────────────────────────────────────►│
              │                                                  │
              │ metadata_provider.v1 (Search by ISBN/ASIN/text)  │
              ├─────────────────────────────────────────────────►│
              │                                                  │
              │◄───── signed /api/v1/cover URL ──────────────────┤
              │◄───── signed /api/v1/file URL ───────────────────┤
              │                                                  │
              │ Reader GET (signed URL, browser/Kobo/Kindle)     │
              │ ───────────────► validated via                   │
              │                  stream_signing_secret           │
              │                                                  │
```

The portal owns OPDS, Kobo sync, Kindle delivery, and request workflows.
This plugin owns local discovery, ingestion, metadata aggregation, and
signed file/cover delivery.

---

## 9. Quick verification

After install:

1. `GET /admin/diagnostics` should show `database.ok: true`, at least one
   library, and `catalog.total > 0` after the first scan completes.
2. The host's plugin process log should print one
   `manifest loaded` / `routes registered` line at startup and one
   migration line per applied migration.
3. The Ebooks portal's "Library sources" picker should list "Local
   Ebooks" once the backend has been registered as a library source.

If any of those fail, start with `operations.md` §Troubleshooting and
`troubleshooting.md` (per-format ingest patterns).
