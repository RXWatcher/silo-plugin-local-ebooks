# Ingest Failure Patterns

When a `scan_event` reports `"<n> file(s) failed to ingest"`, the per-file
WARN lines in the plugin log carry the path and the format-prefixed error.
This file maps the common error strings to root cause and operator action.

For broader scan / metadata troubleshooting see `operations.md`
§Troubleshooting; for protocol detail see `setup-debug-flows.md`.

---

## EPUB (`internal/ebookparse/epub.go`)

| Error | Root cause | Action |
| --- | --- | --- |
| `epub: open: …` | File unreadable, truncated, or not a zip. | Verify the file opens in a reader. Re-download or remove. |
| `epub: container.xml: …` / `epub: container.xml read/parse: …` | Missing or malformed `META-INF/container.xml`. | Probably DRM-stripped or hand-assembled. Re-export with a real EPUB toolchain. |
| `epub: no rootfile in container.xml` | container.xml is valid but lists no rootfile. | Same as above. |
| `epub: open opf: …` / `epub: read opf: …` / `epub: parse opf: …` | OPF metadata file is missing or malformed. | The book is structurally broken. Re-export. |
| `epub: entry not found: <name>` | A referenced manifest entry is absent from the zip. | Typically a partial download. Refetch. |
| `unrecognized date: …` | An OPF `<dc:date>` field uses a non-standard format. | Logged but the file still ingests — metadata may be incomplete. |

The EPUB parser only reads OPF metadata; it does not attempt to repair
the container. Anything the OPF cannot tell us (e.g. embedded series
metadata in custom namespaces) is left for the metadata aggregator to
backfill.

---

## PDF (`internal/ebookparse/pdf.go`)

The PDF parser (`github.com/ledongthuc/pdf`) panics pervasively on
malformed PDFs (bad xref tables, mismatched object IDs, truncated
streams). The plugin wraps every parse in a `recover` boundary so one
planted `.pdf` cannot crash the whole scan.

| Error | Root cause | Action |
| --- | --- | --- |
| `pdf: panic parsing <path>: …` | Library panicked on a malformed PDF. Recovered. | Open the PDF in a viewer; if it opens cleanly, file a bug with the path. If it doesn't, the file is genuinely broken — re-source. |
| `pdf: open: …` | File unreadable or not a PDF. | Verify the file. |

The PDF parser extracts the XMP / Info dictionary and renders the first
page as a cover. Many PDFs (especially scans) have no title/author
metadata at all — the file ingests fine but the catalog row will have a
filename-derived title until the metadata enrichment worker fills it in
via text search. ISBN-required sources will skip PDFs lacking an ISBN
in metadata.

---

## MOBI / AZW / AZW3 (`internal/ebookparse/mobi.go`)

These three extensions all share the PalmDB record-0 header parser.

| Error | Root cause | Action |
| --- | --- | --- |
| `mobi: open: …` / `mobi: read: …` | File unreadable. | Check permissions and inode integrity. |
| `mobi: too short` | File is smaller than the PalmDB header. | Truncated download. Refetch. |
| `mobi: invalid record list` | Record list header is malformed. | Genuinely corrupt or non-Mobi content with a `.mobi` extension. |
| `mobi: record 0 offset out of range` | Record 0 header points past EOF. | Same as above. |
| `mobi: signature mismatch` | PalmDB signature missing — the file is not a Mobi/AZW. | Wrong extension. Identify with `file(1)` and rename or remove. |

MOBI/AZW/AZW3 commonly carries an ASIN but no ISBN — the enrichment
worker's ASIN-source path (`metadata_scan_asin_source`, default
`amazon`) is the relevant lookup. If ASIN-source is disabled or the
key-required source is unkeyed, expect the text-fallback path to be
used.

The parser has a dedicated `mobi_panic_test.go` to guard against
regressions on malformed inputs.

---

## FB2 (`internal/ebookparse/fb2.go`)

| Error | Root cause | Action |
| --- | --- | --- |
| `fb2: open: …` | File unreadable. | Check permissions. |
| `fb2: read: …` | IO error reading the XML body. | Filesystem or transport issue. |
| `fb2: parse: …` | XML is malformed. | The file is broken — typically caused by improperly closed CDATA blocks or BOM corruption. Re-source. |

FB2 metadata is in-document XML; the parser does not need an OPF or a
container. There is no panic-recover boundary because the underlying
XML decoder returns errors instead of panicking.

---

## Cross-cutting patterns

### Symlinks silently skipped, never failed

`scanner.Walk` rejects anything that isn't a regular file. Symlinks
inside a library root are *ignored* — they do not contribute to the
`Failed` count and they do not log a per-file WARN. If a file you
expect is missing from the catalog, check whether it is a symlink.

This is deliberate: a symlink inside a library root could otherwise
make the parser/`os.ReadFile` follow it and read arbitrary host files.

### `(size, mtime)` unchanged → skipped

If a file is being skipped despite changing on disk, check that the
mtime actually changed. Some sync tools (cp -p, restore-from-snapshot)
preserve mtimes — the file looks unchanged to the scanner. Touch the
file or force a content hash by writing one byte and reverting.

### Unsupported extensions silently ignored

The scanner ignores any extension not in `.epub / .pdf / .mobi / .azw
/ .azw3 / .fb2`. CBR/CBZ files in a `media_type=comics` library will
*not* ingest — comics ingest is reserved in the manifest description
but not yet implemented.

### A whole subtree is missing after a scan

If many ebooks vanished at once, look for `walk_errors = TRUE` on the
closing `scan_event`. A subtree that became briefly unreadable causes
the scanner to *conservatively suppress* per-file soft-deletes for that
library — rows persist until a clean scan can confirm absence. If you
see soft-deletions instead, the subtree really was empty at the time
of the scan.
