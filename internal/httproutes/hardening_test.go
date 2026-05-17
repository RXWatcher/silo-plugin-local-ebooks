package httproutes

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
)

func newServer(h http.Handler) *Server {
	s := NewServer()
	s.SetHandler(h)
	return s
}

func TestHandle_QueryParamScalars(t *testing.T) {
	var got string
	s := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("limit") + "|" + r.URL.Query().Get("active") + "|" + r.URL.Query().Get("q")
	}))
	q, _ := structpb.NewStruct(map[string]any{"limit": 20.0, "active": true, "q": "tolkien"})
	if _, err := s.Handle(context.Background(), &pluginv1.HandleHTTPRequest{
		Method: "GET", Path: "/api/v1/catalog", Query: q,
	}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got != "20|true|tolkien" {
		t.Fatalf("query params corrupted: %q", got)
	}
}

func TestHandle_BadMethodNoPanic(t *testing.T) {
	s := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached for an invalid method")
	}))
	for _, m := range []string{"G ET", "GET\r\nX: y", "\x00", "POST /x"} {
		resp, err := s.Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: m, Path: "/api/v1/catalog"})
		if err != nil {
			t.Fatalf("Handle errored (want graceful) for %q: %v", m, err)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("method %q -> %d, want 400", m, resp.StatusCode)
		}
	}
}

func TestHandle_BodyTooLarge(t *testing.T) {
	s := newServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("oversized body must be rejected before the handler")
	}))
	resp, err := s.Handle(context.Background(), &pluginv1.HandleHTTPRequest{
		Method: "POST", Path: "/admin/x", Body: []byte(strings.Repeat("x", maxBodyBytes+1)),
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body -> %d, want 413", resp.StatusCode)
	}
}

func TestHandle_NotConfigured(t *testing.T) {
	resp, err := NewServer().Handle(context.Background(), &pluginv1.HandleHTTPRequest{Method: "GET", Path: "/"})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured -> %d, want 503", resp.StatusCode)
	}
}
