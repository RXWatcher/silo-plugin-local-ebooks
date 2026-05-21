// Package metadataprovider implements the metadata_provider.v1 gRPC service
// over the local aggregator.
package metadataprovider

import (
	"context"
	"errors"
	"html"
	"regexp"
	"sync/atomic"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata"
	"github.com/RXWatcher/continuum-plugin-local-ebooks/internal/metadata/sources"
)

var reHTMLTag = regexp.MustCompile(`<[^>]+>`)

// stripHTML removes HTML tags and decodes HTML entities from s.
func stripHTML(s string) string {
	return html.UnescapeString(reHTMLTag.ReplaceAllString(s, ""))
}

// EnabledFn returns the per-source enabled map at request time so config
// changes take effect without a restart.
type EnabledFn func() map[string]bool

// RegionFn returns the configured default region.
type RegionFn func() string

// MetadataAggregator is the surface the gRPC server needs from the aggregator.
type MetadataAggregator interface {
	Search(ctx context.Context, query, region string,
		enabled map[string]bool, original *metadata.Candidate) ([]metadata.Match, error)
}

// SourceLookup is the source-by-id surface the gRPC server needs.
type SourceLookup interface {
	ForID(id string) sources.Source
}

// Server implements pluginv1.MetadataProviderServer.
type Server struct {
	pluginv1.UnimplementedMetadataProviderServer

	agg     atomic.Pointer[MetadataAggregator]
	reg     atomic.Pointer[SourceLookup]
	enabled atomic.Pointer[EnabledFn]
	region  atomic.Pointer[RegionFn]
}

// SetAggregator atomically swaps the aggregator. Called from main.go's Configure callback.
func (s *Server) SetAggregator(a MetadataAggregator) {
	s.agg.Store(&a)
}

// SetRegistry atomically swaps the source lookup. Called from main.go's Configure callback.
func (s *Server) SetRegistry(r SourceLookup) {
	s.reg.Store(&r)
}

// SetEnabled atomically swaps the enabled function. Called from main.go's Configure callback.
func (s *Server) SetEnabled(fn EnabledFn) {
	s.enabled.Store(&fn)
}

// SetRegion atomically swaps the region function. Called from main.go's Configure callback.
func (s *Server) SetRegion(fn RegionFn) {
	s.region.Store(&fn)
}

// aggregator loads the current aggregator atomically.
func (s *Server) aggregator() MetadataAggregator {
	p := s.agg.Load()
	if p == nil {
		return nil
	}
	return *p
}

// registry loads the current source lookup atomically.
func (s *Server) registry() SourceLookup {
	p := s.reg.Load()
	if p == nil {
		return nil
	}
	return *p
}

// enabledFn loads the enabled function atomically.
func (s *Server) enabledFn() map[string]bool {
	p := s.enabled.Load()
	if p == nil {
		return nil
	}
	return (*p)()
}

// regionFn loads the region function atomically.
func (s *Server) regionFn() string {
	p := s.region.Load()
	if p == nil {
		return "us"
	}
	return (*p)()
}

// Search handles metadata search requests, filtering to ebooks only.
func (s *Server) Search(ctx context.Context, req *pluginv1.SearchMetadataRequest) (*pluginv1.SearchMetadataResponse, error) {
	agg := s.aggregator()
	if agg == nil {
		return nil, status.Error(codes.Unavailable, "server not configured yet")
	}

	// Ebooks-only scope: reject non-book item types.
	if t := req.GetItemType(); t != "" && t != "book" {
		return &pluginv1.SearchMetadataResponse{}, nil
	}

	query := req.GetQuery()
	if query == "" {
		return nil, status.Error(codes.InvalidArgument, "query must not be empty")
	}

	// Build a synthetic Candidate from any provider_ids supplied by the caller
	// so the confidence scorer can award ASIN/ISBN/author bonus points.
	var original *metadata.Candidate
	if pids := req.GetProviderIds(); pids != nil {
		pm := pids.AsMap()
		c := &metadata.Candidate{}
		if v, ok := pm["asin"].(string); ok && v != "" {
			c.ASIN = v
		}
		if v, ok := pm["isbn"].(string); ok && v != "" {
			c.ISBN = v
		}
		if v, ok := pm["author"].(string); ok && v != "" {
			c.Authors = []string{v}
		}
		if c.ASIN != "" || c.ISBN != "" || len(c.Authors) > 0 {
			original = c
		}
	}

	matches, err := agg.Search(ctx, query, s.regionFn(), s.enabledFn(), original)
	if err != nil {
		return nil, err
	}

	results := make([]*pluginv1.ProviderSearchResult, 0, len(matches))
	for _, m := range matches {
		pids, err := candidateProviderIDs(m.Candidate)
		if err != nil {
			return nil, err
		}
		results = append(results, &pluginv1.ProviderSearchResult{
			ProviderId:  metadata.FormatExternalID(m.Candidate.Source, m.Candidate.ExternalID),
			ItemType:    "book",
			Title:       m.Candidate.Title,
			Year:        yearAsInt32(m.Candidate.PublishedAt),
			Overview:    m.Candidate.Description,
			ProviderIds: pids,
			ImageUrl:    m.Candidate.CoverURL,
		})
	}
	return &pluginv1.SearchMetadataResponse{Results: results}, nil
}

// GetMetadata fetches full metadata for a single ebook by provider ID.
func (s *Server) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.GetMetadataResponse, error) {
	reg := s.registry()
	if reg == nil {
		return nil, status.Error(codes.Unavailable, "server not configured yet")
	}

	id := req.GetProviderId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "provider_id must not be empty")
	}

	src, nativeID, err := parseAndLookup(reg, id)
	if err != nil {
		return nil, err
	}

	cand, err := src.Get(ctx, nativeID, s.regionFn())
	if err != nil {
		if errors.Is(err, sources.ErrNotFound) {
			return &pluginv1.GetMetadataResponse{}, nil
		}
		return nil, err
	}
	if cand == nil {
		return &pluginv1.GetMetadataResponse{}, nil
	}

	item, err := candidateToMetadataItem(*cand, id)
	if err != nil {
		return nil, err
	}
	return &pluginv1.GetMetadataResponse{Item: item}, nil
}

// GetImages returns poster images for a single ebook by provider ID.
func (s *Server) GetImages(ctx context.Context, req *pluginv1.GetImagesRequest) (*pluginv1.GetImagesResponse, error) {
	reg := s.registry()
	if reg == nil {
		return nil, status.Error(codes.Unavailable, "server not configured yet")
	}

	id := req.GetProviderId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "provider_id must not be empty")
	}

	src, nativeID, err := parseAndLookup(reg, id)
	if err != nil {
		return nil, err
	}

	cand, err := src.Get(ctx, nativeID, s.regionFn())
	if err != nil {
		if errors.Is(err, sources.ErrNotFound) {
			return &pluginv1.GetImagesResponse{}, nil
		}
		return nil, err
	}
	if cand == nil || cand.CoverURL == "" {
		return &pluginv1.GetImagesResponse{}, nil
	}

	return &pluginv1.GetImagesResponse{
		Images: []*pluginv1.ImageRecord{
			{Kind: "poster", Url: cand.CoverURL},
		},
	}, nil
}

// ResolveImageURL passes through public cover URLs unchanged (no signing needed).
func (s *Server) ResolveImageURL(_ context.Context, req *pluginv1.ResolveImageURLRequest) (*pluginv1.ResolveImageURLResponse, error) {
	return &pluginv1.ResolveImageURLResponse{Url: req.GetPath()}, nil
}

// parseAndLookup parses a provider_id and resolves it to a Source.
// Returns codes.InvalidArgument if the ID is malformed, codes.NotFound if the
// source is not registered.
func parseAndLookup(reg SourceLookup, id string) (sources.Source, string, error) {
	srcID, nativeID, err := metadata.ParseExternalID(id)
	if err != nil {
		return nil, "", status.Errorf(codes.InvalidArgument, "invalid provider_id %q: %v", id, err)
	}
	src := reg.ForID(srcID)
	if src == nil {
		return nil, "", status.Errorf(codes.NotFound, "source %q not registered", srcID)
	}
	return src, nativeID, nil
}

// candidateProviderIDs builds the provider_ids structpb.Struct for a Candidate.
func candidateProviderIDs(c metadata.Candidate) (*structpb.Struct, error) {
	m := map[string]any{
		c.Source: c.ExternalID,
	}
	if c.ASIN != "" {
		m["asin"] = c.ASIN
	}
	if c.ISBN != "" {
		m["isbn"] = c.ISBN
	}
	return structpb.NewStruct(m)
}

// candidateToMetadataItem converts a Candidate to a MetadataItem proto.
func candidateToMetadataItem(c metadata.Candidate, providerID string) (*pluginv1.MetadataItem, error) {
	pids, err := candidateProviderIDs(c)
	if err != nil {
		return nil, err
	}

	// Extra source-specific fields stored in the metadata struct.
	extra := map[string]any{}
	if c.Source != "" {
		extra["source"] = c.Source
	}
	if c.Language != "" {
		extra["language"] = c.Language
	}
	if c.ASIN != "" {
		extra["asin"] = c.ASIN
	}
	if c.ISBN != "" {
		extra["isbn"] = c.ISBN
	}
	if c.Region != "" {
		extra["region"] = c.Region
	}
	if c.Publisher != "" {
		extra["publisher"] = c.Publisher
	}
	if c.PageCount > 0 {
		extra["page_count"] = float64(c.PageCount)
	}
	if c.Series != "" {
		extra["series_name"] = c.Series
	}
	if c.SeriesPos != "" {
		extra["series_position"] = c.SeriesPos
	}
	var metaStruct *structpb.Struct
	if len(extra) > 0 {
		metaStruct, err = structpb.NewStruct(extra)
		if err != nil {
			return nil, err
		}
	}

	// People: authors as Kind="Author".
	var people []*pluginv1.PersonRecord
	for _, a := range c.Authors {
		people = append(people, &pluginv1.PersonRecord{Name: a, Kind: "Author"})
	}

	return &pluginv1.MetadataItem{
		ProviderId:  providerID,
		ItemType:    "book",
		Title:       c.Title,
		Year:        yearAsInt32(c.PublishedAt),
		Overview:    stripHTML(c.Description),
		Genres:      append([]string(nil), c.Genres...),
		ProviderIds: pids,
		PosterPath:  c.CoverURL,
		ReleaseDate: c.PublishedAt,
		People:      people,
		Metadata:    metaStruct,
	}, nil
}

// yearAsInt32 extracts a 4-digit year from the leading characters of a date
// string (YYYY or YYYY-MM-DD). Returns 0 if the string is too short or
// contains non-digit characters in the year portion.
func yearAsInt32(s string) int32 {
	if len(s) < 4 {
		return 0
	}
	var y int32
	for i := 0; i < 4; i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		y = y*10 + int32(s[i]-'0')
	}
	return y
}
