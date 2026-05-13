package metadata

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/go-hclog"
)

// ErrNotFound is the sentinel sources return when a lookup yields no record.
// Defined here (in the metadata package) to avoid an import cycle with the
// sources sub-package. Main.go's registry adapter must map sources.ErrNotFound
// to this value when wrapping *sources.Registry for EnrichmentRegistry.
var ErrNotFound = errors.New("source: not found")

// EnrichmentStore is the subset of *store.Store the worker needs.
type EnrichmentStore interface {
	LoadEbookRow(ctx context.Context, id string) (EbookRow, error)
	UpdateEbookMetadata(ctx context.Context, row EbookRow) error
}

// EnrichmentRegistry is the minimal source-lookup surface the worker needs.
// Like SourceRegistry in aggregator.go, this is an interface because the
// concrete *sources.Registry can't be imported here (import cycle).
type EnrichmentRegistry interface {
	ForID(id string) Source // metadata.Source from aggregator.go
}

// EnrichmentWorker drains metadata_enrichment_job using a single configured
// source per the spec's scan-path trigger model.
type EnrichmentWorker struct {
	Queue    *Queue
	Store    EnrichmentStore
	Registry EnrichmentRegistry
	SourceID string
	Region   string
	Logger   hclog.Logger
}

// NewEnrichmentWorker constructs the worker.
func NewEnrichmentWorker(q *Queue, s EnrichmentStore, reg EnrichmentRegistry,
	sourceID, region string, logger hclog.Logger) *EnrichmentWorker {
	return &EnrichmentWorker{
		Queue: q, Store: s, Registry: reg,
		SourceID: sourceID, Region: region, Logger: logger,
	}
}

// Drain processes pending jobs until the queue is empty or ctx is canceled.
// The queue's FOR UPDATE SKIP LOCKED in ClaimNext is the concurrency guard.
func (w *EnrichmentWorker) Drain(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		job, err := w.Queue.ClaimNext(ctx)
		if errors.Is(err, ErrQueueEmpty) {
			return nil
		}
		if err != nil {
			w.Logger.Warn("claim next job", "err", err)
			return err
		}
		if procErr := w.process(ctx, job); procErr != nil {
			_ = w.Queue.MarkFailed(ctx, job.EbookID, job.Attempts, procErr.Error())
			w.Logger.Warn("enrichment failed", "ebook_id", job.EbookID,
				"attempts", job.Attempts, "err", procErr)
			continue
		}
		if err := w.Queue.MarkCompleted(ctx, job.EbookID); err != nil {
			w.Logger.Warn("mark completed", "err", err)
		}
	}
}

// process runs the enrichment for a single job using the cascade:
// ISBN → ASIN (rare for ebooks) → (title + author) text. Single source per the spec.
func (w *EnrichmentWorker) process(ctx context.Context, j Job) error {
	src := w.Registry.ForID(w.SourceID)
	if src == nil {
		return errors.New("configured scan source not registered: " + w.SourceID)
	}
	row, err := w.Store.LoadEbookRow(ctx, j.EbookID)
	if err != nil {
		return err
	}

	var candidate *Candidate
	switch {
	case row.ISBN != "":
		cs, serr := src.Search(ctx, row.ISBN, w.Region)
		err = serr
		if len(cs) > 0 {
			candidate = &cs[0]
		}
	case row.ASIN != "":
		candidate, err = src.Get(ctx, row.ASIN, w.Region)
	default:
		q := strings.TrimSpace(row.Title + " " + row.Author)
		if q == "" {
			return errors.New("no enrichable identifier on ebook row")
		}
		cs, serr := src.Search(ctx, q, w.Region)
		err = serr
		if len(cs) > 0 {
			candidate = &cs[0]
		}
	}
	if errors.Is(err, ErrNotFound) {
		// Treat as completed-with-no-change rather than failed.
		return nil
	}
	if err != nil {
		return err
	}
	if candidate == nil {
		return nil
	}
	merged := ApplyMatch(row, *candidate)
	return w.Store.UpdateEbookMetadata(ctx, merged)
}
