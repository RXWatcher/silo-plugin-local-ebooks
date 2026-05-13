package metadata

import "testing"

func TestFormatExternalID(t *testing.T) {
	got := FormatExternalID("openlibrary", "OL7353617M")
	if got != "openlibrary:OL7353617M" {
		t.Fatalf("got %q", got)
	}
}

func TestParseExternalID(t *testing.T) {
	cases := []struct {
		in         string
		wantSource string
		wantID     string
		wantErr    bool
	}{
		{"openlibrary:OL7353617M", "openlibrary", "OL7353617M", false},
		{"googlebooks:zyTCAlFPjgYC", "googlebooks", "zyTCAlFPjgYC", false},
		{"isbndb:9780262035613", "isbndb", "9780262035613", false},
		{"", "", "", true},
		{"noprefix", "", "", true},
		{":missingsource", "", "", true},
		{"missingid:", "", "", true},
	}
	for _, c := range cases {
		src, id, err := ParseExternalID(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("Parse(%q) err = %v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if src != c.wantSource || id != c.wantID {
			t.Errorf("Parse(%q) = (%q,%q), want (%q,%q)", c.in, src, id, c.wantSource, c.wantID)
		}
	}
}
