// Package libcfg holds pure library-configuration validation shared by the
// admin API and the Configure seed. No I/O — filesystem existence checks
// live in the HTTP handler.
package libcfg

import (
	"errors"
	"path/filepath"
	"strings"
)

// MediaTypes is the closed set of allowed library media types (matches the
// manifest library_paths json_schema enum).
var MediaTypes = []string{"book", "comics", "manga", "documents"}

// ValidMediaType reports whether mt is one of MediaTypes.
func ValidMediaType(mt string) bool {
	for _, v := range MediaTypes {
		if mt == v {
			return true
		}
	}
	return false
}

// NormalizePath validates and cleans a library root path. It must be a
// non-empty, absolute path with no NUL byte. Returns the cleaned path.
func NormalizePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains NUL")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	return filepath.Clean(p), nil
}
