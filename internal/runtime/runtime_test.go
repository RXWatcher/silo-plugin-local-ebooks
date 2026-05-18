package runtime

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConfigRedaction(t *testing.T) {
	cfg := Config{
		DatabaseURL:           "postgres://u:sup3rsecret@db/x",
		StreamSigningSecret:   "STREAMSECRET",
		GoogleBooksAPIKey:     "GKEY",
		ISBNdbAPIKey:          "IKEY",
		HardcoverAPIKey:       "HKEY",
		MetadataDefaultRegion: "us",
	}
	leaks := []string{"sup3rsecret", "STREAMSECRET", "GKEY", "IKEY", "HKEY"}
	if s := cfg.String(); containsAny(s, leaks) {
		t.Fatalf("String leaked a secret: %s", s)
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("cfg", "config", cfg)
	out := buf.String()
	if containsAny(out, leaks) {
		t.Fatalf("slog leaked a secret: %s", out)
	}
	if !strings.Contains(out, "us") {
		t.Fatalf("redaction hid the non-secret region: %s", out)
	}
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func configureRequest(t *testing.T, kv map[string]any) *pluginv1.ConfigureRequest {
	t.Helper()
	entries := make([]*pluginv1.ConfigEntry, 0, len(kv))
	for k, v := range kv {
		s, err := structpb.NewStruct(map[string]any{"value": v})
		if err != nil {
			t.Fatalf("structpb: %v", err)
		}
		entries = append(entries, &pluginv1.ConfigEntry{Key: k, Value: s})
	}
	return &pluginv1.ConfigureRequest{Config: entries}
}

func TestSnapshot_SlicesIsolated(t *testing.T) {
	s := New(nil, func(Config) error { return nil })
	s.mu.Lock()
	s.cfg = Config{
		LibraryPaths:           []string{"/a"},
		MetadataSourcesEnabled: []string{"openlibrary"},
		Libraries:              []LibraryConfig{{Path: "/a", Name: "A"}},
	}
	s.mu.Unlock()

	snap := s.Snapshot()
	snap.LibraryPaths[0] = "MUT"
	snap.MetadataSourcesEnabled[0] = "MUT"
	snap.Libraries[0].Path = "MUT"

	again := s.Snapshot()
	if again.LibraryPaths[0] != "/a" || again.MetadataSourcesEnabled[0] != "openlibrary" || again.Libraries[0].Path != "/a" {
		t.Fatalf("Snapshot aliases backing arrays: %+v", again)
	}
}

func TestConfigure_AllowsEmptyLibraryPaths(t *testing.T) {
	var got Config
	s := New(nil, func(c Config) error {
		got = c
		return nil
	})

	req := configureRequest(t, map[string]any{
		"database_url": "postgres://x",
	})
	if _, err := s.Configure(nil, req); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if got.DatabaseURL != "postgres://x" {
		t.Fatalf("DatabaseURL = %q, want postgres://x", got.DatabaseURL)
	}
	if len(got.LibraryPaths) != 0 {
		t.Fatalf("LibraryPaths = %#v, want empty", got.LibraryPaths)
	}
	if len(got.Libraries) != 0 {
		t.Fatalf("Libraries = %#v, want empty", got.Libraries)
	}
}
