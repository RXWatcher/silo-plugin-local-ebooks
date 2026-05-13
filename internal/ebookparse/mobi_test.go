package ebookparse

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeMinimalMobi builds a tiny MOBI: PDB header + 1 record entry + record 0 (PalmDOC+Mobi+EXTH).
func writeMinimalMobi(t *testing.T, dir string) string {
	t.Helper()
	// PDB header layout:
	//   0..31  Name (32 bytes, NUL-padded)
	//   32..75 attributes, version, dates, offsets, type, creator, seed, nextList (44 bytes)
	//   76..77 numRecords (2 bytes, big-endian)
	//   78..85 first record entry (8 bytes: 4-byte offset + 4-byte attrs)
	pdb := make([]byte, 78) // 76 bytes of header + 2 bytes for numRecords
	copy(pdb, "Test Title")
	// numRecords = 1 at offset 76
	pdb[76] = 0
	pdb[77] = 1

	// Record entry: 4-byte offset to record data + 4-byte attributes/uniqueID
	// recOffset = 78 (numRecords) + 8 (one record entry) = 86
	recOffset := uint32(78 + 8) // = 86
	recordEntry := make([]byte, 8)
	binary.BigEndian.PutUint32(recordEntry[0:4], recOffset)
	pdb = append(pdb, recordEntry...)

	// Record 0:
	//   0..15  PalmDOC header (16 bytes)
	//   16..19 "MOBI" sig
	//   20..23 mobi header length (32 bits BE)
	//   then padding to fill mobiHeaderLen
	//   then EXTH header: "EXTH" + 4-byte length + 4-byte record count + records
	rec := make([]byte, 16)              // PalmDOC header (unused values)
	rec = append(rec, []byte("MOBI")...) // MOBI sig
	mobiHeaderLen := uint32(24)          // minimal mobi header length
	mh := make([]byte, 4)
	binary.BigEndian.PutUint32(mh, mobiHeaderLen)
	rec = append(rec, mh...)
	// pad mobi header to length 24: we have 8 bytes so far in the mobi header
	// ("MOBI" + length field), so add 16 more bytes of padding
	rec = append(rec, make([]byte, 16)...)

	// EXTH header now
	rec = append(rec, []byte("EXTH")...)
	rec = append(rec, 0, 0, 0, 0) // EXTH header length placeholder (not validated by our parser)
	exthCount := uint32(2)
	cb := make([]byte, 4)
	binary.BigEndian.PutUint32(cb, exthCount)
	rec = append(rec, cb...)

	// EXTH record: type=100 (author)
	exthAuthor := []byte("Test Author")
	rh1 := make([]byte, 8)
	binary.BigEndian.PutUint32(rh1[0:4], 100)
	binary.BigEndian.PutUint32(rh1[4:8], uint32(8+len(exthAuthor)))
	rec = append(rec, rh1...)
	rec = append(rec, exthAuthor...)

	// EXTH record: type=104 (ISBN)
	exthISBN := []byte("9780593135204")
	rh2 := make([]byte, 8)
	binary.BigEndian.PutUint32(rh2[0:4], 104)
	binary.BigEndian.PutUint32(rh2[4:8], uint32(8+len(exthISBN)))
	rec = append(rec, rh2...)
	rec = append(rec, exthISBN...)

	full := append(pdb, rec...)
	path := filepath.Join(dir, "sample.mobi")
	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseMOBI_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := writeMinimalMobi(t, dir)
	p, err := ParseMOBI(path, ".mobi")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Format != "mobi" {
		t.Errorf("format %q", p.Format)
	}
	// Title falls back to PDB name (EXTH 503 not provided in this fixture)
	if !bytes.Contains([]byte(p.Title), []byte("Test")) {
		t.Errorf("title %q", p.Title)
	}
	if len(p.Authors) == 0 || p.Authors[0] != "Test Author" {
		t.Errorf("authors %v", p.Authors)
	}
	if p.ISBN != "9780593135204" {
		t.Errorf("isbn %q", p.ISBN)
	}
}

func TestParseMOBI_TooShort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.mobi")
	os.WriteFile(path, []byte("short"), 0o644)
	_, err := ParseMOBI(path, ".mobi")
	if err == nil {
		t.Error("expected error on short file")
	}
}

func TestParseMOBI_AZWFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeMinimalMobi(t, dir)
	p, err := ParseMOBI(path, ".azw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Format != "azw" {
		t.Errorf("format %q, want 'azw'", p.Format)
	}
}
