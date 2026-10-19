// Package agenttrace exposes authenticated Agent queries, never ingestion.
package agenttrace

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/agenttrace"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

type Handler struct {
	svc   *biz.Service
	log   *slog.Logger
	slots chan struct{}
}

func NewHandler(svc *biz.Service, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{svc: svc, log: log, slots: make(chan struct{}, 4)}
}

// Register requires the existing authentication middleware and a service wired
// through biz.ScopedQuerier, which enforces built-in chat ownership.
func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/agent-observability/runs", h.list)
	r.Get("/v1/agent-observability/sessions", h.list)
	r.Get("/v1/agent-observability/sessions/detail", h.list)
	r.Get("/v1/agent-observability/facets", h.list)
	r.Get("/v1/agent-observability/diagnostics", h.list)
	r.Get("/v1/agent-observability/traces/{trace_id}", h.trace)
	r.Get("/v1/agent-observability/ingestion", h.ingestion)
}

// GET /api/v1/agent-observability/runs returns a bounded result, including
// scoped sessions, facets, diagnostics and partial-result metadata. The SPA
// pages this one response locally so pages share the same query snapshot.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, func(ctx context.Context) (any, error) {
		q, err := parseQuery(r)
		if err != nil {
			return nil, err
		}
		if r.URL.Path == "/api/v1/agent-observability/sessions/detail" && q.SessionID == "" {
			return nil, errs.ErrInvalid
		}
		out, err := h.svc.List(ctx, q)
		if err != nil {
			return nil, err
		}
		switch r.URL.Path {
		case "/api/v1/agent-observability/sessions":
			return struct {
				Items    []biz.Session `json:"items"`
				Metadata biz.Metadata  `json:"metadata"`
			}{out.Sessions, out.Metadata}, nil
		case "/api/v1/agent-observability/facets":
			return struct {
				Facets   biz.Facets   `json:"facets"`
				Metadata biz.Metadata `json:"metadata"`
			}{out.Facets, out.Metadata}, nil
		case "/api/v1/agent-observability/diagnostics":
			return struct {
				Checks   []biz.Check  `json:"checks"`
				Metadata biz.Metadata `json:"metadata"`
			}{out.Checks, out.Metadata}, nil
		default:
			return out, nil
		}
	})
}

// GET /api/v1/agent-observability/traces/{trace_id} accepts a canonical trace
// ID and optional entry_span_id; all original steps remain available.
func (h *Handler) trace(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, func(ctx context.Context) (any, error) {
		out, err := h.svc.Trace(ctx, chi.URLParam(r, "trace_id"))
		if err != nil {
			return nil, err
		}
		if entry := r.URL.Query().Get("entry_span_id"); entry != "" {
			found := false
			for _, run := range out.Runs {
				if run.EntrySpanID == entry {
					found = true
					break
				}
			}
			if !found {
				return nil, errs.ErrNotFound
			}
		}
		return out, nil
	})
}

// GET /api/v1/agent-observability/ingestion resolves an explicit deployment
// target and returns OTLP environment settings. It does not modify that target.
func (h *Handler) ingestion(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, func(ctx context.Context) (any, error) {
		v := r.URL.Query()
		return h.svc.Ingestion(ctx, biz.IngestionRequest{SDK: v.Get("sdk"), Identity: biz.Identity{ServiceName: v.Get("service_name"), ServiceNamespace: v.Get("service_namespace"), Environment: v.Get("environment")}, Endpoint: v.Get("endpoint"), Target: biz.ReceiverTarget{Location: v.Get("location"), ClusterID: v.Get("cluster_id"), DeviceID: v.Get("device_id"), InstanceID: v.Get("instance_id"), ContainerName: v.Get("container_name")}})
	})
}

// any is confined to encoding the route-specific DTOs in one HTTP envelope.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, fn func(context.Context) (any, error)) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		h.respond(w, r, nil, errs.ErrUnauthorized)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.respond(w, r, nil, errs.ErrTooManyAttempts)
		return
	}
	// Observing a conversation must not emit database spans back into the trace store.
	ctx := trace.ContextWithSpanContext(r.Context(), trace.SpanContext{})
	ctx, cancel := context.WithTimeout(tracing.WithoutHTTPClientTracing(ctx), 25*time.Second)
	defer cancel()
	out, err := fn(ctx)
	h.respond(w, r, out, err)
}

func parseQuery(r *http.Request) (biz.Query, error) {
	v := r.URL.Query()
	q := biz.Query{ServiceName: v.Get("service_name"), SDK: v.Get("sdk"), Model: v.Get("model"), Kind: v.Get("kind"), Status: v.Get("status"), SessionID: v.Get("session_id"), Search: v.Get("search")}
	var err error
	q.Start, err = time.Parse(time.RFC3339Nano, v.Get("start"))
	if err != nil {
		return q, errs.ErrInvalid
	}
	q.End, err = time.Parse(time.RFC3339Nano, v.Get("end"))
	if err != nil {
		return q, errs.ErrInvalid
	}
	if v.Has("environment") {
		value := v.Get("environment")
		q.Environment = &value
	}
	if v.Has("service_namespace") {
		value := v.Get("service_namespace")
		q.ServiceNamespace = &value
	}
	return q, q.Validate()
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, data any, err error) {
	status, message := http.StatusOK, "success"
	if err != nil {
		status, message = errs.HTTPStatus(err), "Agent telemetry query failed"
		switch {
		case errors.Is(err, errs.ErrInvalid):
			message = err.Error()
		case errors.Is(err, errs.ErrUnauthorized), errors.Is(err, errs.ErrNotFound), errors.Is(err, errs.ErrTooManyAttempts):
			message = http.StatusText(status)
		case errors.Is(err, errs.ErrNotWiredYet):
			status, message = http.StatusServiceUnavailable, "Trace backend or receiver resolver is disabled"
		case errors.Is(err, tracequery.ErrResponseTooLarge):
			status, message = http.StatusRequestEntityTooLarge, "Trace exceeds the 16 MiB query limit; reduce captured content or inspect it directly in Tempo"
		case errors.Is(err, context.DeadlineExceeded):
			status, message = http.StatusGatewayTimeout, "Agent telemetry query timed out; narrow the time window"
		default:
			if !errors.Is(err, context.Canceled) {
				h.log.ErrorContext(r.Context(), "agent telemetry query failed", "error", err)
			}
			status = http.StatusBadGateway
		}
	}
	body, marshalErr := json.Marshal(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}{status, message, data})
	if marshalErr != nil {
		h.log.ErrorContext(r.Context(), "agent telemetry encode failed", "error", marshalErr)
		http.Error(w, "response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		h.log.DebugContext(r.Context(), "agent telemetry response write failed", "error", err)
	}
}
