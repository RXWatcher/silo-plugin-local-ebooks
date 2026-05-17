package sources

import (
	"strings"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-local-ebooks/internal/metadata"
)

// An unknown/hostile region must never be interpolated into the Amazon host
// (SSRF) — it falls back to the US host.
func TestAmazonHostFor_NoSSRF(t *testing.T) {
	a := &Amazon{baseURL: amazonBaseURL}
	for _, region := range []string{
		"com@169.254.169.254", "evil.example/", "x/../y", "co.uk ", "\nhost",
	} {
		got := a.amazonHostFor(region)
		if got != "https://www.amazon.com" {
			t.Fatalf("region %q -> %q, want the US host (no interpolation)", region, got)
		}
	}
	if got := a.amazonHostFor("de"); got != "https://www.amazon.de" {
		t.Fatalf("known region broken: %q", got)
	}
}

// A deeply nested __NEXT_DATA__ tree must not blow the goroutine stack.
func TestTraverseGoodreadsNextData_DepthBounded(t *testing.T) {
	var v interface{} = map[string]interface{}{"leaf": "x"}
	for i := 0; i < 200000; i++ {
		v = []interface{}{v}
	}
	var out []metadata.Candidate
	traverseGoodreadsNextData(v, &out, 0) // must return, not panic/stack-overflow
}

func TestParseGoodreadsSearchPage_ResultsCapped(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString(`href="/book/show/`)
		b.WriteString("1")
		b.WriteString(`x"><span itemprop="name">T</span>`)
	}
	got := parseGoodreadsSearchPage([]byte(b.String()))
	if len(got) > grMaxResults {
		t.Fatalf("returned %d results, want <= %d", len(got), grMaxResults)
	}
}
