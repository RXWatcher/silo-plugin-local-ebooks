// Package scheduler implements the scheduled_task.v1 RPC. The plugin
// dispatches two task keys: library_scan and metadata_enrichment_worker.
package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
)

// Tasks holds the registered task functions so both the admin trigger and
// the scheduled-task RPC run the same code path. ScanFn returns the
// scan_event id; concurrent triggers de-duplicate (the in-flight call's id
// is returned to subsequent callers). DrainFn drains the enrichment queue.
type Tasks struct {
	ScanFn  func(context.Context) (int64, error)
	DrainFn func(context.Context) error

	mu      sync.Mutex
	running atomic.Bool
}

// Server implements ScheduledTaskServer.
type Server struct {
	pluginv1.UnimplementedScheduledTaskServer
	t *Tasks
}

// New constructs a ScheduledTask gRPC server backed by t.
func New(t *Tasks) *Server { return &Server{t: t} }

// Run dispatches the named task. Unknown keys return an error.
func (s *Server) Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	switch req.GetTaskKey() {
	case "library_scan":
		if s.t == nil || s.t.ScanFn == nil {
			return &pluginv1.RunScheduledTaskResponse{}, nil
		}
		if !s.t.running.CompareAndSwap(false, true) {
			// Previous scan still running; drop this trigger.
			return &pluginv1.RunScheduledTaskResponse{}, nil
		}
		defer s.t.running.Store(false)
		_, err := s.t.ScanFn(ctx)
		return &pluginv1.RunScheduledTaskResponse{}, err

	case "metadata_enrichment_worker":
		if s.t == nil || s.t.DrainFn == nil {
			return &pluginv1.RunScheduledTaskResponse{}, nil
		}
		err := s.t.DrainFn(ctx)
		return &pluginv1.RunScheduledTaskResponse{}, err

	default:
		return nil, errors.New("unknown task key")
	}
}
