package ebookparse

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupportedFormat is returned for file extensions we don't handle.
var ErrUnsupportedFormat = errors.New("ebookparse: unsupported format")

// Parse dispatches to the right format parser based on the file extension,
// then sanitize-bounds the (untrusted) result before returning it.
func Parse(path string) (Parsed, error) {
	p, err := parseByExt(path)
	if err != nil {
		return Parsed{}, err
	}
	p.sanitize()
	return p, nil
}

func parseByExt(path string) (Parsed, error) {
	ext := strings.ToLower(extOf(path))
	switch ext {
	case ".epub":
		return ParseEPUB(path)
	case ".pdf":
		return ParsePDF(path)
	case ".mobi", ".azw", ".azw3":
		return ParseMOBI(path, ext)
	case ".fb2":
		return ParseFB2(path)
	default:
		return Parsed{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
}

// extOf returns the extension (with leading dot, lowercase) or empty string.
// Returns empty if the rightmost "." is inside a path component before a "/".
func extOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			return path[i:]
		}
		if path[i] == '/' {
			return ""
		}
	}
	return ""
}

// IsSupported reports whether `path` has a recognized ebook extension.
func IsSupported(path string) bool {
	ext := strings.ToLower(extOf(path))
	switch ext {
	case ".epub", ".pdf", ".mobi", ".azw", ".azw3", ".fb2":
		return true
	}
	return false
}
