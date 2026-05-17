// Package httproutes adapts a stdlib http.Handler to the SDK's HttpRoutes.v1
// gRPC service. The plugin host invokes our gRPC service for each inbound
// HTTP request; we replay against the wrapped handler and return its
// response. ServeHTTP exposes the same handler to a standalone HTTP
// listener with X-Continuum-* header stripping (security: host-trust
// headers cannot reach handlers from the public listener).
package httproutes

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

type Server struct {
	pluginv1.UnimplementedHttpRoutesServer
	handler atomic.Pointer[http.Handler]
}

func NewServer() *Server { return &Server{} }

func (s *Server) SetHandler(h http.Handler) {
	if h == nil {
		s.handler.Store(nil)
		return
	}
	s.handler.Store(&h)
}

// ServeHTTP is used by the standalone listener. Strips X-Continuum-*
// headers before invoking the wrapped handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hPtr := s.handler.Load()
	if hPtr == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"not_ready","message":"plugin not configured"}}`))
		return
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-continuum-") {
			r.Header.Del(k)
		}
	}
	(*hPtr).ServeHTTP(w, r)
}

// maxBodyBytes caps the request body the host may hand us (JSON API/admin
// surface; file transfers are GET). The body is fully buffered in memory.
const maxBodyBytes = 8 << 20 // 8 MiB

// isHTTPToken reports whether s is a valid RFC7230 method token. httptest /
// http.ReadRequest panic on a method with spaces/control chars, and method
// comes straight from the untrusted RPC payload.
func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e || strings.ContainsRune("()<>@,;:\\\"/[]?={} \t", r) {
			return false
		}
	}
	return true
}

func errResponse(code int32, msg string) *pluginv1.HandleHTTPResponse {
	return &pluginv1.HandleHTTPResponse{
		StatusCode: code,
		Body:       []byte(`{"error":{"message":"` + msg + `"}}`),
		Headers:    map[string]string{"Content-Type": "application/json"},
	}
}

// Handle is the gRPC HttpRoutes.v1 RPC — used when the host plugin proxy
// forwards a request via gRPC instead of direct HTTP.
func (s *Server) Handle(ctx context.Context, req *pluginv1.HandleHTTPRequest) (resp *pluginv1.HandleHTTPResponse, _ error) {
	// Defense in depth: a panic in request reconstruction or the downstream
	// handler must not take down the gRPC serving goroutine.
	defer func() {
		if rec := recover(); rec != nil {
			resp = errResponse(http.StatusInternalServerError, "internal error")
		}
	}()

	hPtr := s.handler.Load()
	if hPtr == nil {
		return &pluginv1.HandleHTTPResponse{
			StatusCode: http.StatusServiceUnavailable,
			Body:       []byte(`{"error":{"code":"not_ready","message":"plugin not configured"}}`),
			Headers:    map[string]string{"Content-Type": "application/json"},
		}, nil
	}
	h := *hPtr

	if b := req.GetBody(); len(b) > maxBodyBytes {
		return errResponse(http.StatusRequestEntityTooLarge, "request body too large"), nil
	}

	rawQuery := ""
	if req.GetQuery() != nil {
		vals := url.Values{}
		for k, v := range req.GetQuery().GetFields() {
			// Switch on the structpb kind. The old fallback used v.String(),
			// the protobuf *debug* text (e.g. "number_value:20"), so a numeric
			// query param like ?limit=20 sent as a JSON number became
			// "limit=number_value:20" and broke all pagination once routing
			// reached the handlers.
			switch kind := v.GetKind().(type) {
			case *structpb.Value_StringValue:
				vals.Set(k, kind.StringValue)
			case *structpb.Value_NumberValue:
				vals.Set(k, strconv.FormatFloat(kind.NumberValue, 'f', -1, 64))
			case *structpb.Value_BoolValue:
				vals.Set(k, strconv.FormatBool(kind.BoolValue))
			default:
				// null / struct / list — query params are scalars; skip.
			}
		}
		rawQuery = vals.Encode()
	}
	method := req.GetMethod()
	if method == "" {
		method = http.MethodGet
	}
	if !isHTTPToken(method) {
		return errResponse(http.StatusBadRequest, "invalid method"), nil
	}

	u := &url.URL{Path: req.GetPath(), RawQuery: rawQuery}
	// http.NewRequestWithContext returns an error (rather than panicking like
	// httptest.NewRequest) on an unparseable method/URL, and propagates the
	// gRPC context so a client disconnect / deadline cancels downstream work.
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(req.GetBody()))
	if err != nil {
		return errResponse(http.StatusBadRequest, "invalid request"), nil
	}
	httpReq.RequestURI = u.RequestURI()
	for k, v := range req.GetHeaders() {
		httpReq.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	res := rec.Result()
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	headers := map[string]string{}
	for k, vs := range res.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	return &pluginv1.HandleHTTPResponse{
		StatusCode: int32(rec.Code),
		Headers:    headers,
		Body:       body,
	}, nil
}
