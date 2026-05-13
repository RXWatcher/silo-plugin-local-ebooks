package metadata

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeSource is a test double for the Source interface.
type fakeSource struct {
	id        string
	response  []Candidate
	searchErr error
}

func (f *fakeSource) ID() string                       { return f.id }
func (f *fakeSource) Enabled(cfg map[string]bool) bool { return cfg[f.id] }
func (f *fakeSource) Get(_ context.Context, _, _ string) (*Candidate, error) {
	return nil, errors.New("not found")
}
func (f *fakeSource) Search(_ context.Context, _, _ string) ([]Candidate, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.response, nil
}

// fakeRegistry is a test double for SourceRegistry.
type fakeRegistry struct {
	srcs []Source
}

func (r *fakeRegistry) Register(s Source) { r.srcs = append(r.srcs, s) }
func (r *fakeRegistry) All() []Source     { return r.srcs }

func newAggregatorTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("EBOOKSDB_TEST_DSN")
	if dsn == "" {
		t.Skip("EBOOKSDB_TEST_DSN unset")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	pool.Exec(context.Background(), `TRUNCATE metadata_cache`)
	t.Cleanup(func() {
		pool.Exec(context.Background(), `TRUNCATE metadata_cache`)
		pool.Close()
	})
	return pool
}

func TestAggregator_FanOutAndRank(t *testing.T) {
	pool := newAggregatorTestPool(t)
	reg := &fakeRegistry{}
	reg.Register(&fakeSource{id: "fakea", response: []Candidate{
		{Source: "fakea", ExternalID: "1", Title: "Foo"},
	}})
	reg.Register(&fakeSource{id: "fakeb", response: []Candidate{
		{Source: "fakeb", ExternalID: "2", Title: "Foo Bar"},
	}})
	cache := NewCache(pool, 30*24*time.Hour)
	a := NewAggregator(reg, cache, 100)
	matches, err := a.Search(context.Background(), "Foo", "us",
		map[string]bool{"fakea": true, "fakeb": true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].Confidence < matches[1].Confidence {
		t.Errorf("results must be sorted by confidence desc")
	}
}

func TestAggregator_PerSourceErrorSwallowed(t *testing.T) {
	pool := newAggregatorTestPool(t)
	reg := &fakeRegistry{}
	reg.Register(&fakeSource{id: "good", response: []Candidate{
		{Source: "good", ExternalID: "1", Title: "X"},
	}})
	reg.Register(&fakeSource{id: "bad", searchErr: errors.New("upstream down")})
	cache := NewCache(pool, 30*24*time.Hour)
	a := NewAggregator(reg, cache, 100)
	matches, err := a.Search(context.Background(), "X", "us",
		map[string]bool{"good": true, "bad": true}, nil)
	if err != nil {
		t.Fatalf("aggregator should not error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match (good source only), got %d", len(matches))
	}
}

func TestAggregator_CapsAtMaxResults(t *testing.T) {
	pool := newAggregatorTestPool(t)
	reg := &fakeRegistry{}
	big := make([]Candidate, 25)
	for i := range big {
		big[i] = Candidate{Source: "biga", ExternalID: "x", Title: "T"}
	}
	reg.Register(&fakeSource{id: "biga", response: big})
	cache := NewCache(pool, 30*24*time.Hour)
	a := NewAggregator(reg, cache, 100)
	matches, _ := a.Search(context.Background(), "T", "us",
		map[string]bool{"biga": true}, nil)
	if len(matches) != MaxResults {
		t.Errorf("expected %d matches (cap), got %d", MaxResults, len(matches))
	}
}

func TestAggregator_DisabledSourceSkipped(t *testing.T) {
	pool := newAggregatorTestPool(t)
	reg := &fakeRegistry{}
	reg.Register(&fakeSource{id: "yes", response: []Candidate{
		{Source: "yes", ExternalID: "1", Title: "T"},
	}})
	reg.Register(&fakeSource{id: "no", response: []Candidate{
		{Source: "no", ExternalID: "2", Title: "T"},
	}})
	cache := NewCache(pool, 30*24*time.Hour)
	a := NewAggregator(reg, cache, 100)
	matches, _ := a.Search(context.Background(), "T", "us",
		map[string]bool{"yes": true}, nil)
	if len(matches) != 1 {
		t.Errorf("expected 1 match from enabled source, got %d", len(matches))
	}
	if matches[0].Source != "yes" {
		t.Errorf("expected source 'yes', got %q", matches[0].Source)
	}
}
