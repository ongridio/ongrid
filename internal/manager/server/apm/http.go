// Package apm exposes authenticated, bounded APM queries. Telemetry ingestion
// remains in the existing nginx/Collector data plane.
package apm

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	biz "github.com/ongridio/ongrid/internal/manager/biz/apm"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
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
	return &Handler{svc: svc, log: log, slots: make(chan struct{}, 8)}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/apm/services", h.services)
	r.Get("/v1/apm/overview", h.overview)
	r.Get("/v1/apm/summary", h.summary)
	r.Get("/v1/apm/operations", h.operations)
	r.Get("/v1/apm/dependencies", h.dependencies)
	r.Get("/v1/apm/diagnostics", h.diagnostics)
	r.Get("/v1/apm/runtime", h.runtime)
	r.Get("/v1/apm/instances", h.instances)
	r.Get("/v1/apm/error-groups", h.errorGroups)
	r.Get("/v1/apm/alert-template", h.alertTemplate)
	r.Get("/v1/apm/repository-binding", h.repositoryBinding)
	r.Put("/v1/apm/repository-binding", h.repositoryBinding)
	r.Delete("/v1/apm/repository-binding", h.repositoryBinding)
}

// @Summary List observed application services
// @Router /api/v1/apm/services [get]
// @Success 200 {object} apm.ListResult
func (h *Handler) services(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, false, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.List(ctx, q, false) })
}

// @Summary Query application request trends
// @Router /api/v1/apm/overview [get]
// @Success 200 {object} apm.Overview
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Overview(ctx, q) })
}

// @Summary Query scoped application request totals without trend curves
// @Router /api/v1/apm/summary [get]
// @Success 200 {object} apm.Overview
func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Summary(ctx, q) })
}

// @Summary List application operation metrics
// @Router /api/v1/apm/operations [get]
// @Success 200 {object} apm.ListResult
func (h *Handler) operations(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.List(ctx, q, true) })
}

// @Summary Query observed application dependencies
// @Router /api/v1/apm/dependencies [get]
// @Success 200 {object} apm.Dependencies
func (h *Handler) dependencies(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, r.URL.Query().Get("service_name") != "", func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Dependencies(ctx, q) })
}

// @Summary Inspect application telemetry samples
// @Router /api/v1/apm/diagnostics [get]
// @Success 200 {object} apm.Diagnostics
func (h *Handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Diagnostics(ctx, q) })
}

// @Summary Read application runtime metrics
// @Router /api/v1/apm/runtime [get]
// @Success 200 {object} apm.Runtime
func (h *Handler) runtime(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Runtime(ctx, q) })
}

// @Summary Discover observed application instances without runtime curves
// @Router /api/v1/apm/instances [get]
// @Success 200 {object} apm.Runtime
func (h *Handler) instances(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.Instances(ctx, q) })
}

// @Summary Group matching error spans from a bounded sample of recent traces
// @Router /api/v1/apm/error-groups [get]
// @Success 200 {object} apm.ErrorGroups
func (h *Handler) errorGroups(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) { return h.svc.ErrorGroups(ctx, q) })
}

// @Summary Build a request-level alert template for the existing rule editor
// @Router /api/v1/apm/alert-template [get]
// @Success 200 {object} apm.AlertTemplate
func (h *Handler) alertTemplate(w http.ResponseWriter, r *http.Request) {
	h.serve(w, r, true, func(ctx context.Context, q biz.Query) (any, error) {
		values := r.URL.Query()
		threshold, err := strconv.ParseFloat(values.Get("threshold"), 64)
		if err != nil {
			return nil, errs.ErrInvalid
		}
		minimum, err := strconv.ParseFloat(values.Get("min_requests"), 64)
		if err != nil {
			return nil, errs.ErrInvalid
		}
		dwell, err := strconv.Atoi(values.Get("for_seconds"))
		if err != nil {
			return nil, errs.ErrInvalid
		}
		return h.svc.AlertTemplate(ctx, q, values.Get("metric"), threshold, minimum, dwell)
	})
}

// any is confined to JSON serialization; each route returns a typed biz DTO.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request, detail bool, fn func(context.Context, biz.Query) (any, error)) {
	if _, ok := tenantctx.From(r.Context()); !ok {
		h.respond(w, r, nil, errs.ErrUnauthorized)
		return
	}
	q, err := parseQuery(r)
	if err == nil {
		err = q.Validate(detail)
	}
	if err != nil {
		h.respond(w, r, nil, err)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.respond(w, r, nil, errs.ErrTooManyAttempts)
		return
	}
	ctx, cancel := context.WithTimeout(tracing.WithoutHTTPClientTracing(r.Context()), 25*time.Second)
	defer cancel()
	out, err := fn(ctx, q)
	h.respond(w, r, out, err)
}

func parseQuery(r *http.Request) (biz.Query, error) {
	values := r.URL.Query()
	q := biz.Query{MetricSource: values.Get("metric_source"), Protocol: values.Get("protocol"), MetricFormat: values.Get("metric_format"), ServiceName: values.Get("service_name"), Operation: values.Get("operation"), ServiceVersion: values.Get("service_version"), InstanceID: values.Get("instance_id"), SpanKind: values.Get("span_kind"), Sort: values.Get("sort"), Search: values.Get("search")}
	var err error
	q.DeviceID, q.ClusterID = values.Get("device_id"), values.Get("cluster_id")
	q.SnapshotID = values.Get("snapshot_id")
	if values.Has("cluster_node_id") {
		q.ClusterNodeID, err = strconv.ParseUint(values.Get("cluster_node_id"), 10, 64)
		if err != nil || q.ClusterNodeID == 0 {
			return q, errs.ErrInvalid
		}
	}
	q.Start, err = time.Parse(time.RFC3339Nano, values.Get("start"))
	if err != nil {
		return q, errs.ErrInvalid
	}
	q.End, err = time.Parse(time.RFC3339Nano, values.Get("end"))
	if err != nil {
		return q, errs.ErrInvalid
	}
	if values.Has("environment") {
		value := values.Get("environment")
		q.Environment = &value
	}
	if values.Has("service_namespace") {
		value := values.Get("service_namespace")
		q.ServiceNamespace = &value
	}
	for key, target := range map[string]*int{"page": &q.Page, "page_size": &q.PageSize} {
		if values.Has(key) {
			value, err := strconv.Atoi(values.Get(key))
			if err != nil || value <= 0 {
				return q, errs.ErrInvalid
			}
			*target = value
		}
	}
	return q, nil
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, data any, err error) {
	status, message := http.StatusOK, "success"
	if err != nil {
		status = errs.HTTPStatus(err)
		message = http.StatusText(status)
		if errors.Is(err, errs.ErrInvalid) {
			message = err.Error()
		} else if errors.Is(err, errs.ErrNotWiredYet) {
			status, message = http.StatusServiceUnavailable, "APM metrics backend is disabled"
		} else if status >= 500 {
			status, message = http.StatusBadGateway, "APM telemetry query failed"
			if !errors.Is(err, context.Canceled) {
				h.log.ErrorContext(r.Context(), "apm query failed", slog.Any("error", err))
			}
		}
	}
	body, marshalErr := json.Marshal(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}{status, message, data})
	if marshalErr != nil {
		h.log.ErrorContext(r.Context(), "apm encode response", slog.Any("error", marshalErr))
		http.Error(w, "response encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		h.log.DebugContext(r.Context(), "apm response write failed", slog.Any("error", err))
	}
}
