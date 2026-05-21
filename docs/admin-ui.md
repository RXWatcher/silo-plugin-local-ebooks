# Admin Console

`GET /admin` (labelled "Local Ebooks" in the Continuum host navigation)
serves a single-file HTML/CSS/JS console from
`internal/server/admin_home.go`. The same template skeleton is shared
with the sibling `bw-audio`, `bw-ebook`, and `local-audiobooks`
plugins, so operators see consistent chrome.

This file is a reference for each tab — what it shows, which endpoints
back it, and what to do when something is wrong. For protocol detail
see `setup-debug-flows.md`; for operator workflows see `operations.md`.

---

## Status strip (always visible)

Above the tabs, five cells summarize plugin health at a glance:

| Cell | Source | Meaning |
| --- | --- | --- |
| **DB** | `GET /admin/diagnostics` → `database.ok` | Postgres reachable and migrations applied. |
| **Libraries** | `GET /admin/libraries` count | Number of configured library rows (enabled + disabled). |
| **Sources** | `app_config.metadata_sources_enabled` count | Currently enabled metadata sources. |
| **Last scan** | `GET /admin/scans[0]` | Newest `scan_event`, finished or running. |
| **Active scan** | `GET /admin/scans` filtered to `finished_at IS NULL` | Hot indicator while a scan is in flight. |

If the DB dot goes red, every other tab will show its empty state —
fix the DB first.

---

## Libraries tab

Backed by `internal/server/libraries.go`. Manages the
`library_path` rows directly; the host's `library_paths` config blob is
a non-destructive seed only.

**Endpoints used:**

| Endpoint | Purpose |
| --- | --- |
| `GET /admin/libraries` | List rows with last-scanned timestamp. |
| `POST /admin/libraries` | Add a new library (`path`, `name`, `media_type`, `enabled`). |
| `PATCH /admin/libraries/{id}` | Full-fields update of `name`, `media_type`, `enabled`. Path is immutable. |
| `DELETE /admin/libraries/{id}` | Delete catalog rows for the library. Files on disk are untouched. |
| `POST /admin/libraries/{id}/scan` | Per-library scan — **runs even when the library is disabled**. |
| `GET /admin/filesystem/browse?path=…` | Lists what the plugin runtime can see at `path`; used by the path picker. |

**Per-row controls:**

- Name input — blurs save a PATCH.
- `media_type` select (`book`/`comics`/`manga`/`documents`) — saves on
  change. Classification only; does not change which extensions ingest.
- Enabled checkbox — saves on change. Disabling hides the library from
  the portal and excludes it from global `POST /admin/scan`.
- **Scan** button — triggers `POST /admin/libraries/{id}/scan`.
  Confirmation badge is `"Queued"` while in flight.
- **Remove** button — prompts then deletes. Files on disk are never
  touched.

If a path you expect is missing from the browser, the plugin runtime
cannot see it — fix the host mount before anything else.

---

## Scans tab

Backed by `internal/server/admin.go` (`/admin/scan`, `/admin/scans`).

**Endpoints used:**

| Endpoint | Purpose |
| --- | --- |
| `POST /admin/scan` | Global scan — walks **enabled** libraries only. Returns `{ scan_event_id }`. |
| `GET /admin/scans` | Recent `scan_event` rows. Each row carries `added`, `changed`, `deleted`, `failed`, `started_at`, `finished_at`, and any `walk_errors` / `"<n> file(s) failed to ingest"` note. |

**What to watch:**

- An active scan (`finished_at` null) disables the "Scan all libraries"
  button to prevent overlapping runs.
- A finished scan with `failed > 0` shows a yellow warning card. The
  count is per-file ingest failures — drill into the plugin log for the
  per-path WARN line; see `troubleshooting.md` for what each format
  typically fails on.
- A finished scan with `walk_errors = TRUE` means the filesystem walk
  itself hit an unreadable entry; soft-deletes were suppressed for the
  affected library to avoid wrongly dropping still-present rows. Fix
  permissions and rescan.

---

## Metadata tab

Backed by `internal/server/admin.go` (`/admin/config`,
`/admin/metadata/backfill`, `/admin/metadata/queue`).

**Endpoints used:**

| Endpoint | Purpose |
| --- | --- |
| `GET /admin/config` | Current app config (sources enabled, region, TTL, RPS, scan-source, inline-enrich). API key fields are write-only. |
| `PUT /admin/config` | Save changes. Validates source IDs against the registered set; rejects with an error if any are unknown. |
| `POST /admin/metadata/backfill` | Enqueue every ebook for re-enrichment. Returns `{ queued }`. |
| `GET /admin/metadata/queue` | Queue stats (pending, in-flight, succeeded, failed counts). |

**Form fields:**

- **Enabled sources** — multi-select against the registered providers.
  Sources requiring an API key are silently inactive without one even
  if listed here.
- **Scan source** / **ISBN source** / **ASIN source** — pickers that
  drive the worker's identifier cascade.
- **Default region** — ISO country code; affects Amazon, Goodreads,
  Douban.
- **Cache TTL (days)** — positive integer; defaults to 30.
- **Rate limit (RPS)** — per-source ceiling; defaults to 5.
- **Inline enrichment** — toggles `scan_inline_enrich`. Off for large
  libraries.
- **Google Books / ISBNdb / Hardcover API keys** — write-only. The
  server **never echoes existing keys back into the form**; the input
  shows blank even when a key is set. A blank submit is interpreted as
  "leave unchanged" (clearing requires explicit empty-string semantics
  documented in the manifest).

**Backfill button:** queues a job per ebook and shows the queued count.
The minute-cadence worker will start draining at the next tick. With
inline enrichment on, the worker also drains after each scan.

---

## Diagnostics tab

Backed by `GET /admin/diagnostics`. Read-only snapshot of plugin state.

**What it shows:**

- **DB health** — Postgres reachable, schema migrations applied. Red on
  any connection or migration error.
- **Catalog totals** — total ebooks, total files, per-`media_type`
  counts.
- **Queue depth** — pending / in-flight / succeeded / failed
  enrichment-job counts.
- **Recent scans** — last 10 `scan_event` rows mirroring the Scans tab.
- **Configuration mirror** — non-secret app config (sources enabled
  count, region, scan source, TTL, RPS, inline-enrich flag). Use this
  to confirm `PUT /admin/config` actually applied.
- **Features list** — capability flags the diagnostics endpoint
  advertises (`multi_library`, `manual_scan`, `metadata_backfill`,
  `metadata_queue_status`, `catalog_health`).

When opening a support ticket, copy the JSON body of
`GET /admin/diagnostics` into the issue — it is the fastest
single-shot snapshot of plugin health.
