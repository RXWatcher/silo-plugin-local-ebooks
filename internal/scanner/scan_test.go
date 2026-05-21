package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/ebookparse"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/store"
)

// fakeStore models the real store invariant under test (M1): the ebook id is
// STABLE per (path) — UpsertEbook on a known path keeps the original id and
// just updates content_sig — so a content edit never churns the PK.
type fakeStore struct {
	ebooks   map[string]string // id -> path
	sigs     map[string]string // id -> content_sig
	pathToID map[string]string // path -> stable id
	covers   map[string][]byte
	deleted  map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		ebooks: map[string]string{}, sigs: map[string]string{},
		pathToID: map[string]string{}, covers: map[string][]byte{},
		deleted: map[string]bool{},
	}
}

func (f *fakeStore) UpsertEbook(ctx context.Context, lpID int64, id, path, format string,
	size int64, mtime time.Time, contentSig string, p ebookparse.Parsed) (string, bool, error) {
	if cur, ok := f.pathToID[path]; ok {
		// Known path: PK stays stable; only the signature/state changes.
		f.sigs[cur] = contentSig
		f.ebooks[cur] = path
		f.deleted[cur] = false
		return cur, true, nil
	}
	f.ebooks[id] = path
	f.pathToID[path] = id
	f.sigs[id] = contentSig
	return id, false, nil
}
func (f *fakeStore) UpsertCover(ctx context.Context, id, ct, source string, b []byte) error {
	f.covers[id] = b
	return nil
}
func (f *fakeStore) ListEbookRefs(ctx context.Context, lpID int64) ([]store.EbookFileRef, error) {
	var out []store.EbookFileRef
	for id, p := range f.ebooks {
		if !f.deleted[id] {
			out = append(out, store.EbookFileRef{ID: id, Path: p, ContentSig: f.sigs[id]})
		}
	}
	return out, nil
}
func (f *fakeStore) SoftDelete(ctx context.Context, id string) error {
	f.deleted[id] = true
	return nil
}

type fakeEnqueuer struct{ ids []string }

func (f *fakeEnqueuer) Enqueue(ctx context.Context, id string) error {
	f.ids = append(f.ids, id)
	return nil
}

func TestScan_AddsAndEnqueues(t *testing.T) {
	dir := t.TempDir()
	// Create a file with .epub extension. It won't be a valid EPUB; scan
	// continues with empty metadata via the parse-failed branch.
	os.WriteFile(filepath.Join(dir, "book.epub"), []byte("not really an epub"), 0o644)
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	res, err := Walk(context.Background(), dir, 1, Deps{Store: store, EnrichmentQueue: enq})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("added=%d want 1", res.Added)
	}
	if len(enq.ids) != 1 {
		t.Errorf("expected 1 enqueue, got %d", len(enq.ids))
	}
}

func TestScan_SkipsNonEbookFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignore me"), 0o644)
	store := newFakeStore()
	res, _ := Walk(context.Background(), dir, 1, Deps{Store: store})
	if res.Added != 0 {
		t.Errorf("expected 0 added, got %d", res.Added)
	}
}

func TestScan_SoftDeletesMissingFiles(t *testing.T) {
	dir := t.TempDir()
	store := newFakeStore()
	store.ebooks["preexisting-id"] = filepath.Join(dir, "gone.epub")
	res, _ := Walk(context.Background(), dir, 1, Deps{Store: store})
	if res.Deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", res.Deleted)
	}
	if !store.deleted["preexisting-id"] {
		t.Error("expected preexisting-id to be soft-deleted")
	}
}

// TestScan_SkipsSymlinkedEbook guards the symlink-escape vector: a symlink
// placed inside a library root must NOT be followed and ingested, otherwise
// an attacker who can write into a scanned dir reads arbitrary host files.
func TestScan_SkipsSymlinkedEbook(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "book.epub"), []byte("real book"), 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "evil.epub")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	store := newFakeStore()
	res, err := Walk(context.Background(), dir, 1, Deps{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("added=%d, want 1 (only the real book, not the symlink)", res.Added)
	}
	for _, p := range store.ebooks {
		if filepath.Base(p) == "evil.epub" {
			t.Fatal("symlink ebook was ingested — symlink escape not prevented")
		}
	}
}

// TestScan_SkipsSymlinkedSidecarCover guards the same escape via cover.jpg.
func TestScan_SkipsSymlinkedSidecarCover(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "book.epub"), []byte("real book"), 0o644)
	if err := os.Symlink(secret, filepath.Join(dir, "cover.jpg")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	store := newFakeStore()
	if _, err := Walk(context.Background(), dir, 1, Deps{Store: store}); err != nil {
		t.Fatal(err)
	}
	for id, b := range store.covers {
		if string(b) == "PRIVATE KEY BYTES" {
			t.Fatalf("sidecar cover symlink exfiltrated secret bytes for id %s", id)
		}
	}
}

func TestScan_NilEnqueuerIsSafe(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "book.epub"), []byte("x"), 0o644)
	store := newFakeStore()
	res, err := Walk(context.Background(), dir, 1, Deps{Store: store, EnrichmentQueue: nil})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("added=%d", res.Added)
	}
}

// TestScan_ContentChangeKeepsStableID is the M1 regression: editing a file's
// content must NOT churn the ebook PK (the old code derived the id from
// size/mtime and rewrote it on conflict, FK-violating cover/enrichment rows).
func TestScan_ContentChangeKeepsStableID(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "book.epub")
	if err := os.WriteFile(fp, []byte("v1 contents"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	deps := Deps{Store: store, EnrichmentQueue: enq}

	if _, err := Walk(context.Background(), dir, 1, deps); err != nil {
		t.Fatal(err)
	}
	if len(store.pathToID) != 1 || len(enq.ids) != 1 {
		t.Fatalf("after first scan: pathToID=%v enq=%v", store.pathToID, enq.ids)
	}
	id1 := store.pathToID[fp]

	// Edit the file: new size + a strictly later mtime so the signature changes.
	if err := os.WriteFile(fp, []byte("v2 contents are longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(fp, later, later); err != nil {
		t.Fatal(err)
	}

	res, err := Walk(context.Background(), dir, 1, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.pathToID[fp]; got != id1 {
		t.Fatalf("PK churned on content edit: id1=%q id2=%q", id1, got)
	}
	if res.Changed != 1 || res.Failed != 0 {
		t.Fatalf("want Changed=1 Failed=0, got %+v", res)
	}
	if len(enq.ids) != 2 || enq.ids[1] != id1 {
		t.Fatalf("changed file must re-enqueue under the stable id; enq=%v id1=%q", enq.ids, id1)
	}
}

// TestScan_UnchangedNotReEnqueued guards the re-enqueue-storm regression:
// a rescan with no content change must NOT re-enqueue (which would reset
// every enriched row to pending every scan).
func TestScan_UnchangedNotReEnqueued(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "book.epub"), []byte("stable"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	deps := Deps{Store: store, EnrichmentQueue: enq}

	r1, err := Walk(context.Background(), dir, 1, deps)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Walk(context.Background(), dir, 1, deps)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Added != 1 {
		t.Fatalf("first scan Added=%d, want 1", r1.Added)
	}
	if r2.Added != 0 || r2.Changed != 0 || r2.Failed != 0 {
		t.Fatalf("unchanged rescan should be a no-op, got %+v", r2)
	}
	if len(enq.ids) != 1 {
		t.Fatalf("unchanged file re-enqueued: enq=%v (want exactly 1)", enq.ids)
	}
}
