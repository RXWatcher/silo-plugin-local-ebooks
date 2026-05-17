package libcfg

import "testing"

func TestValidMediaType(t *testing.T) {
	for _, ok := range []string{"book", "comics", "manga", "documents"} {
		if !ValidMediaType(ok) {
			t.Errorf("ValidMediaType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Book", "audiobook", "x"} {
		if ValidMediaType(bad) {
			t.Errorf("ValidMediaType(%q) = true, want false", bad)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	good := map[string]string{
		"/srv/ebooks":        "/srv/ebooks",
		"/srv/ebooks/":       "/srv/ebooks",
		"/srv//a/../ebooks":  "/srv/ebooks",
	}
	for in, want := range good {
		got, err := NormalizePath(in)
		if err != nil || got != want {
			t.Errorf("NormalizePath(%q) = (%q,%v), want (%q,nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "relative/path", "ebooks", "/srv/a\x00b"} {
		if _, err := NormalizePath(bad); err == nil {
			t.Errorf("NormalizePath(%q) = nil error, want error", bad)
		}
	}
}
