package ebookparse

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseMOBI extracts metadata from MOBI/AZW/AZW3 files.
// All three share Mobipocket headers inside a PDB container.
func ParseMOBI(filePath, ext string) (Parsed, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Parsed{}, fmt.Errorf("mobi: open: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return Parsed{}, fmt.Errorf("mobi: read: %w", err)
	}
	if len(data) < 78 {
		return Parsed{}, fmt.Errorf("mobi: too short")
	}

	format := strings.TrimPrefix(ext, ".")
	out := Parsed{Format: format}

	// PDB name (32 bytes, NUL-padded)
	pdbName := string(bytes.TrimRight(data[0:32], "\x00"))

	// PDB record list at offset 76: 2-byte big-endian count, then 8-byte entries
	numRecords := binary.BigEndian.Uint16(data[76:78])
	if numRecords < 1 || len(data) < 78+8*int(numRecords) {
		return Parsed{}, fmt.Errorf("mobi: invalid record list")
	}
	rec0Offset := binary.BigEndian.Uint32(data[78:82])
	if int(rec0Offset)+20 >= len(data) {
		return Parsed{}, fmt.Errorf("mobi: record 0 offset out of range")
	}

	// MOBI sig at rec0+16
	mobiSig := data[rec0Offset+16 : rec0Offset+20]
	if !bytes.Equal(mobiSig, []byte("MOBI")) {
		return Parsed{}, fmt.Errorf("mobi: signature mismatch")
	}

	mobiHeaderLen := binary.BigEndian.Uint32(data[rec0Offset+20 : rec0Offset+24])
	mobiEnd := rec0Offset + 16 + mobiHeaderLen
	if int(mobiEnd) > len(data) {
		mobiEnd = uint32(len(data))
	}

	// EXTH header right after mobi header (if present)
	exthStart := mobiEnd
	if int(exthStart)+12 > len(data) ||
		!bytes.Equal(data[exthStart:exthStart+4], []byte("EXTH")) {
		out.Title = pdbName
		return out, nil
	}
	exthCount := binary.BigEndian.Uint32(data[exthStart+8 : exthStart+12])
	pos := exthStart + 12

	for i := uint32(0); i < exthCount; i++ {
		if int(pos)+8 > len(data) {
			break
		}
		recType := binary.BigEndian.Uint32(data[pos : pos+4])
		recLen := binary.BigEndian.Uint32(data[pos+4 : pos+8])
		if recLen < 8 || int(pos)+int(recLen) > len(data) {
			break
		}
		recData := data[pos+8 : pos+recLen]
		switch recType {
		case 100:
			out.Authors = append(out.Authors, strings.TrimSpace(string(recData)))
		case 101:
			out.Publisher = strings.TrimSpace(string(recData))
		case 103:
			out.Description = strings.TrimSpace(string(recData))
		case 104:
			out.ISBN = strings.TrimSpace(string(recData))
		case 106:
			if t, err := tryParseDate(strings.TrimSpace(string(recData))); err == nil {
				out.PublishedAt = t
			}
		case 113:
			out.ASIN = strings.TrimSpace(string(recData))
		case 503:
			out.Title = strings.TrimSpace(string(recData))
		}
		pos += recLen
	}
	if out.Title == "" {
		out.Title = pdbName
	}
	return out, nil
}
