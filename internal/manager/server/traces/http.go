// Package traces proxies Tempo query API to authenticated SPA users so
// the Traces page can run TraceQL / facet searches against the embedded
// Tempo without exposing /api/* through nginx for queries (the data
// plane /v1/traces ingest route is auth_request-gated for OTLP push
// only —).
//
// Routes mounted under /api/v1 by cmd/ongrid/main.go:
//
//	GET /v1/traces/search — proxy /api/search
//	GET /v1/traces/{trace_id} — proxy /api/traces/<id>
//	GET /v1/traces/tags/{tag}/values — proxy /api/search/tag/<tag>/values
package traces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

// Querier is the narrow surface this handler needs. *tracequery.Client
// satisfies it.
type Querier interface {
	SearchTraces(ctx context.Context, opts tracequery.SearchOptions) (*tracequery.SearchResult, error)
	GetTrace(ctx context.Context, traceID string) (*tracequery.TraceResult, error)
	TagValues(ctx context.Context, tag string) ([]string, error)
}

// Handler exposes the auth'd Tempo query proxy. Requires an underlying
// Querier; when nil the routes return 503 so the SPA can show a clear
// "traces disabled" state instead of failing silently.
type Handler struct {
	q Querier
}

// NewHandler builds the handler. q may be nil when Tempo is disabled.
func NewHandler(q Querier) *Handler {
	return &Handler{q: q}
}

// Register attaches routes on r. Caller must wrap r in the auth
// middleware before calling. The querier must enforce built-in chat ownership.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/traces/search", h.search)
	r.Get("/v1/traces/tags/{tag}/values", h.tagValues)
	// The trace_id route comes last so it doesn't shadow /tags/...
	r.Get("/v1/traces/{trace_id}", h.getTrace)
}

type searchResp struct {
	Traces  json.RawMessage `json:"traces"`
	Metrics json.RawMessage `json:"metrics,omitempty"`
	From    string          `json:"from"`
	To      string          `json:"to"`
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	if h.q == nil {
		writeErr(w, http.StatusServiceUnavailable, "traces backend disabled")
		return
	}
	q := r.URL.Query()

	// Time window: required end, optional start (Tempo falls back to its
	// default lookback when start is zero — but the SPA always sends both
	// so requiring them keeps the contract symmetric with logs).
	from, err := parseTime(q.Get("start"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("start: %v", err))
		return
	}
	to, err := parseTime(q.Get("end"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("end: %v", err))
		return
	}

	limit := 100
	if s := q.Get("limit"); s != "" {
		n, perr := strconv.Atoi(s)
		if perr != nil || n <= 0 || n > 1000 {
			writeErr(w, http.StatusBadRequest, "limit must be 1..1000")
			return
		}
		limit = n
	}

	var minDur, maxDur time.Duration
	if s := q.Get("minDuration"); s != "" {
		d, perr := time.ParseDuration(s)
		if perr != nil || d < 0 {
			writeErr(w, http.StatusBadRequest, "minDuration must be a non-negative duration")
			return
		}
		minDur = d
	}
	if s := q.Get("maxDuration"); s != "" {
		d, perr := time.ParseDuration(s)
		if perr != nil || d < 0 {
			writeErr(w, http.StatusBadRequest, "maxDuration must be a non-negative duration")
			return
		}
		maxDur = d
	}

	opts := tracequery.SearchOptions{
		Query:       q.Get("q"),
		Limit:       limit,
		Start:       from,
		End:         to,
		MinDuration: minDur,
		MaxDuration: maxDur,
	}
	// Facet form: when q is empty, build a Tags map from service +
	// operation. Tempo accepts the legacy `tags=key=v key=v` form for
	// non-TraceQL search.
	if opts.Query == "" {
		tags := map[string]string{}
		if v := q.Get("service"); v != "" {
			tags["service.name"] = v
		}
		if v := q.Get("operation"); v != "" {
			tags["name"] = v
		}
		if len(tags) > 0 {
			opts.Tags = tags
		}
	}

	ctx, cancel := context.WithTimeout(tracing.WithoutHTTPClientTracing(r.Context()), 30*time.Second)
	defer cancel()
	out, err := h.q.SearchTraces(ctx, opts)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, searchResp{
		Traces:  out.Traces,
		Metrics: out.Metrics,
		From:    from.UTC().Format(time.RFC3339),
		To:      to.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) getTrace(w http.ResponseWriter, r *http.Request) {
	if h.q == nil {
		writeErr(w, http.StatusServiceUnavailable, "traces backend disabled")
		return
	}
	id := chi.URLParam(r, "trace_id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "trace_id required")
		return
	}
	ctx, cancel := context.WithTimeout(tracing.WithoutHTTPClientTracing(r.Context()), 30*time.Second)
	defer cancel()
	out, err := h.q.GetTrace(ctx, id)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			writeErr(w, http.StatusNotFound, http.StatusText(http.StatusNotFound))
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Tempo returns OTLP-shaped JSON; pass it through verbatim so the SPA
	// can walk resourceSpans / scopeSpans / spans without a re-encode.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Body)
}

func (h *Handler) tagValues(w http.ResponseWriter, r *http.Request) {
	if h.q == nil {
		writeErr(w, http.StatusServiceUnavailable, "traces backend disabled")
		return
	}
	tag := chi.URLParam(r, "tag")
	if tag == "" {
		writeErr(w, http.StatusBadRequest, "tag required")
		return
	}
	ctx, cancel := context.WithTimeout(tracing.WithoutHTTPClientTracing(r.Context()), 15*time.Second)
	defer cancel()
	out, err := h.q.TagValues(ctx, tag)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": out})
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("missing")
	}
	// Accept RFC3339 + unix-seconds-as-string for easy curl testing.
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// >1e12 = millis; >1e15 = nanos; smaller = seconds.
		switch {
		case n > 1e15:
			return time.Unix(0, n), nil
		case n > 1e12:
			return time.UnixMilli(n), nil
		default:
			return time.Unix(n, 0), nil
		}
	}
	return time.Time{}, errs.ErrInvalid
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
