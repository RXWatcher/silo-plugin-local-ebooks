package metadataprovider

import (
	"context"
	"testing"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata/sources"
)

// fakeSrc satisfies sources.Source for tests.
type fakeSrc struct {
	id   string
	cand *metadata.Candidate
}

func (f *fakeSrc) ID() string                     { return f.id }
func (f *fakeSrc) Enabled(_ map[string]bool) bool { return true }
func (f *fakeSrc) Get(_ context.Context, _, _ string) (*metadata.Candidate, error) {
	return f.cand, nil
}
func (f *fakeSrc) Search(_ context.Context, _, _ string) ([]metadata.Candidate, error) {
	if f.cand == nil {
		return nil, nil
	}
	return []metadata.Candidate{*f.cand}, nil
}

// fakeRegistry satisfies SourceLookup.
type fakeRegistry struct{ s sources.Source }

func (r *fakeRegistry) ForID(id string) sources.Source {
	if r.s != nil && r.s.ID() == id {
		return r.s
	}
	return nil
}

// fakeAggregator satisfies MetadataAggregator.
type fakeAggregator struct{ matches []metadata.Match }

func (a *fakeAggregator) Search(_ context.Context, _, _ string, _ map[string]bool, _ *metadata.Candidate) ([]metadata.Match, error) {
	return a.matches, nil
}

// capturingAggregator captures the original argument passed to Search.
type capturingAggregator struct {
	capturedOriginal *metadata.Candidate
}

func (a *capturingAggregator) Search(_ context.Context, _, _ string, _ map[string]bool, original *metadata.Candidate) ([]metadata.Match, error) {
	a.capturedOriginal = original
	return nil, nil
}

func newServerLite() *Server {
	src := &fakeSrc{id: "openlibrary", cand: &metadata.Candidate{
		Source:     "openlibrary",
		ExternalID: "OL12345W",
		Title:      "X",
		CoverURL:   "https://example/c.jpg",
	}}
	s := &Server{}
	s.SetEnabled(func() map[string]bool { return map[string]bool{"openlibrary": true} })
	s.SetRegion(func() string { return "us" })
	s.SetAggregator(&fakeAggregator{matches: []metadata.Match{{
		Source:     "openlibrary",
		Confidence: 50,
		Candidate:  metadata.Candidate{Source: "openlibrary", ExternalID: "OL12345W", Title: "X"},
	}}})
	s.SetRegistry(&fakeRegistry{s: src})
	return s
}

func TestServer_GetMetadata_HappyPath(t *testing.T) {
	s := newServerLite()
	resp, err := s.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{
		ProviderId: "openlibrary:OL12345W",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetItem().GetTitle() != "X" {
		t.Errorf("title %q", resp.GetItem().GetTitle())
	}
	if resp.GetItem().GetItemType() != "book" {
		t.Errorf("item_type = %q, want book", resp.GetItem().GetItemType())
	}
}

func TestServer_GetMetadata_BadExternalID(t *testing.T) {
	s := newServerLite()
	_, err := s.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{
		ProviderId: "noprefix",
	})
	if err == nil {
		t.Errorf("expected error")
	}
}

func TestServer_GetMetadata_SourceNotFound(t *testing.T) {
	s := newServerLite()
	_, err := s.GetMetadata(context.Background(), &pluginv1.GetMetadataRequest{
		ProviderId: "unknownsource:abc",
	})
	if err == nil {
		t.Errorf("expected NotFound error")
	}
}

func TestServer_GetImages(t *testing.T) {
	s := newServerLite()
	resp, err := s.GetImages(context.Background(), &pluginv1.GetImagesRequest{
		ProviderId: "openlibrary:OL12345W",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetImages()) == 0 {
		t.Errorf("expected at least one image")
	}
	if resp.GetImages()[0].GetUrl() != "https://example/c.jpg" {
		t.Errorf("unexpected image url %q", resp.GetImages()[0].GetUrl())
	}
}

func TestServer_ResolveImageURL_Passthrough(t *testing.T) {
	s := newServerLite()
	resp, err := s.ResolveImageURL(context.Background(), &pluginv1.ResolveImageURLRequest{
		Path: "https://example/x.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetUrl() != "https://example/x.jpg" {
		t.Errorf("got %q", resp.GetUrl())
	}
}

func TestServer_Search_NonBookEmpty(t *testing.T) {
	s := newServerLite()
	resp, err := s.Search(context.Background(), &pluginv1.SearchMetadataRequest{
		Query:    "anything",
		ItemType: "movie",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetResults()) != 0 {
		t.Errorf("expected 0 results for movie itemType")
	}
}

func TestServer_Search_HappyPath(t *testing.T) {
	s := newServerLite()
	resp, err := s.Search(context.Background(), &pluginv1.SearchMetadataRequest{
		Query:    "X",
		ItemType: "book",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetResults()) != 1 {
		t.Errorf("expected 1 result, got %d", len(resp.GetResults()))
	}
	if resp.GetResults()[0].GetItemType() != "book" {
		t.Errorf("item_type = %q, want book", resp.GetResults()[0].GetItemType())
	}
}

// TestServer_Search_ProviderIDs_OriginalCandidate verifies that when
// req.ProviderIds contains an ASIN, Search is called with a non-nil original
// so the confidence scorer can award ASIN-match bonus points.
func TestServer_Search_ProviderIDs_OriginalCandidate(t *testing.T) {
	cap := &capturingAggregator{}
	s := &Server{}
	s.SetEnabled(func() map[string]bool { return nil })
	s.SetRegion(func() string { return "us" })
	s.SetAggregator(cap)

	pids, err := structpb.NewStruct(map[string]any{"asin": "B0TESTVALUE"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Search(context.Background(), &pluginv1.SearchMetadataRequest{
		Query:       "some book",
		ItemType:    "book",
		ProviderIds: pids,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.capturedOriginal == nil {
		t.Fatal("expected non-nil original to be passed to agg.Search")
	}
	if cap.capturedOriginal.ASIN != "B0TESTVALUE" {
		t.Errorf("expected ASIN %q, got %q", "B0TESTVALUE", cap.capturedOriginal.ASIN)
	}
}

// TestCandidateToMetadataItem_ExtrasAndPageCount verifies that a Candidate with
// page_count, ISBN, and source is mapped to a MetadataItem with extras.page_count,
// extras.source, and extras.isbn populated correctly.
func TestCandidateToMetadataItem_ExtrasAndPageCount(t *testing.T) {
	cand := metadata.Candidate{
		Source:     "openlibrary",
		ExternalID: "OL12345W",
		Title:      "Test Book",
		ISBN:       "9781234567890",
		PageCount:  321,
		Publisher:  "Acme Press",
	}
	item, err := candidateToMetadataItem(cand, "openlibrary:OL12345W")
	if err != nil {
		t.Fatal(err)
	}
	meta := item.GetMetadata().AsMap()

	// extras.page_count must be present as a numeric value matching the candidate.
	pcRaw, ok := meta["page_count"].(float64)
	if !ok {
		t.Fatalf("expected page_count float64 in extras, got %T %v", meta["page_count"], meta["page_count"])
	}
	if int(pcRaw) != 321 {
		t.Errorf("extras.page_count = %v, want 321", pcRaw)
	}

	// extras.source must equal "openlibrary".
	if meta["source"] != "openlibrary" {
		t.Errorf("extras.source = %v, want openlibrary", meta["source"])
	}

	// extras.isbn must equal "9781234567890".
	if meta["isbn"] != "9781234567890" {
		t.Errorf("extras.isbn = %v, want 9781234567890", meta["isbn"])
	}

	// extras.publisher must equal "Acme Press".
	if meta["publisher"] != "Acme Press" {
		t.Errorf("extras.publisher = %v, want Acme Press", meta["publisher"])
	}

	// Narrator must NOT appear in extras for ebooks.
	if _, ok := meta["narrator"]; ok {
		t.Errorf("extras.narrator must not be set for ebooks")
	}
}

// TestStripHTML verifies that HTML tags are removed and entities are decoded.
func TestStripHTML(t *testing.T) {
	input := "<p>Hello <b>world</b>&amp;more</p>"
	want := "Hello world&more"
	got := stripHTML(input)
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", input, got, want)
	}
}

// TestServer_Search_ProviderIDs_NoSignals verifies that when provider_ids
// contains no recognized fields, original remains nil (no spurious candidate).
func TestServer_Search_ProviderIDs_NoSignals(t *testing.T) {
	cap := &capturingAggregator{}
	s := &Server{}
	s.SetEnabled(func() map[string]bool { return nil })
	s.SetRegion(func() string { return "us" })
	s.SetAggregator(cap)

	pids, err := structpb.NewStruct(map[string]any{"unknown_key": "irrelevant"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Search(context.Background(), &pluginv1.SearchMetadataRequest{
		Query:       "some book",
		ProviderIds: pids,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cap.capturedOriginal != nil {
		t.Errorf("expected nil original when no recognized fields present, got %+v", cap.capturedOriginal)
	}
}
