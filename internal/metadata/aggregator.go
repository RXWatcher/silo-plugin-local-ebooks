package metadata

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// MaxResults caps the aggregated result count.
const MaxResults = 20

// Source is the interface every per-upstream adapter must satisfy.
// It mirrors sources.Source; defined here to avoid an import cycle
// (the sources package imports metadata, so metadata cannot import sources).
type Source interface {
	ID() string
	Enabled(cfg map[string]bool) bool
	Search(ctx context.Context, query, region string) ([]Candidate, error)
	Get(ctx context.Context, externalID, region string) (*Candidate, error)
}

// SourceRegistry is the minimal registry surface the Aggregator needs.
//
// Note: sources.Registry.All() returns []sources.Source, which is a different
// slice type from []metadata.Source even though sources.Source structurally
// satisfies metadata.Source. Callers wiring a *sources.Registry must wrap it
// with an adapter that converts the slice element-by-element.
type SourceRegistry interface {
	All() []Source
}

// Aggregator orchestrates parallel fan-out to registered sources, with
// per-source rate limiting, cache lookup, and error swallowing.
type Aggregator struct {
	registry SourceRegistry
	cache    *Cache
	limiters map[string]*rate.Limiter
	limitMu  sync.Mutex
	rps      int
}

// NewAggregator builds an Aggregator. `rps` is the per-source request budget.
// rps is clamped to a minimum of 1 to avoid a burst=0 limiter that silently
// drops all requests.
func NewAggregator(reg SourceRegistry, cache *Cache, rps int) *Aggregator {
	if rps < 1 {
		rps = 1 // avoid silent burst=0 → all-skipped behavior
	}
	return &Aggregator{
		registry: reg, cache: cache, rps: rps,
		limiters: map[string]*rate.Limiter{},
	}
}

func (a *Aggregator) limiter(sourceID string) *rate.Limiter {
	a.limitMu.Lock()
	defer a.limitMu.Unlock()
	if l, ok := a.limiters[sourceID]; ok {
		return l
	}
	l := rate.NewLimiter(rate.Limit(a.rps), a.rps)
	a.limiters[sourceID] = l
	return l
}

// Search runs all enabled sources in parallel and returns up to MaxResults
// matches sorted by descending confidence. Per-source errors are swallowed
// (a single source's outage does not fail the whole search). `original` is
// the ebook's current metadata used as the comparison baseline for the
// confidence formula; may be nil.
func (a *Aggregator) Search(ctx context.Context, query, region string,
	enabled map[string]bool, original *Candidate) ([]Match, error) {

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []Match
	)

	for _, s := range a.registry.All() {
		if !s.Enabled(enabled) {
			continue
		}
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			matches := a.searchOne(ctx, s, query, region, original)
			mu.Lock()
			out = append(out, matches...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Confidence > out[j].Confidence
	})
	if len(out) > MaxResults {
		out = out[:MaxResults]
	}
	return out, nil
}

func (a *Aggregator) searchOne(ctx context.Context, s Source,
	query, region string, original *Candidate) []Match {

	kind := classify(query)
	cacheKey := Key(s.ID(), kind, region, query)

	if entry, err := a.cache.Get(ctx, cacheKey); err == nil {
		if entry.NotFound {
			return nil
		}
		var cs []Candidate
		if err := json.Unmarshal(entry.Response, &cs); err == nil {
			return rank(cs, query, original)
		}
		// bad cache row; fall through to live fetch
	}

	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.limiter(s.ID()).Wait(rctx); err != nil {
		return nil
	}

	cs, err := s.Search(ctx, query, region)
	if err != nil {
		return nil
	}
	if len(cs) == 0 {
		_ = a.cache.PutNotFound(ctx, cacheKey, s.ID(), region)
		return nil
	}
	if payload, jerr := json.Marshal(cs); jerr == nil {
		_ = a.cache.PutHit(ctx, cacheKey, s.ID(), region, payload)
	}
	return rank(cs, query, original)
}

func classify(query string) LookupKind {
	if aggregatorASIN.MatchString(query) {
		return LookupKindASIN
	}
	if aggregatorISBN.MatchString(query) {
		return LookupKindISBN
	}
	return LookupKindSearch
}

// Separate from the sources-package asinRE to avoid an import cycle.
var aggregatorASIN = regexp.MustCompile(`^B0[0-9A-Z]{8}$`)
var aggregatorISBN = regexp.MustCompile(`^(978|979)?\d{9}[\dX]$`)

func rank(cs []Candidate, query string, original *Candidate) []Match {
	out := make([]Match, 0, len(cs))
	for _, c := range cs {
		out = append(out, Match{
			Source:     c.Source,
			Confidence: CalculateConfidence(query, c, original),
			Candidate:  c,
		})
	}
	return out
}
