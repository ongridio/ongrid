// Package integration hosts HTTP routes that bridge ongrid to third-party
// observability stacks (today: Grafana). They delegate to the matching
// biz/{grafana,...} services; the handlers themselves are thin auth +
// error-mapping shims.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	bizgrafana "github.com/ongridio/ongrid/internal/manager/biz/grafana"
	bizsetting "github.com/ongridio/ongrid/internal/manager/biz/setting"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

// GrafanaService is the narrow surface the handler depends on. *bizgrafana.Service
// satisfies it structurally.
type GrafanaService interface {
	Test(ctx context.Context) error
	Sync(ctx context.Context) (*bizgrafana.SyncResult, error)
	FetchDashboardJSON(ctx context.Context, uid string) ([]byte, error)
}

// PromQuerier is the narrow surface used to exercise the configured
// Prometheus on the test endpoint. *promquery.Client satisfies it.
type PromQuerier interface {
	Query(ctx context.Context, expr string, ts time.Time) (any, error)
}

// URLProbe is the narrow surface for the Loki / Tempo test endpoints.
// The Probe(ctx) method does whatever lightweight check the resolver
// owner thinks proves the URL is reachable + auth-accepted (Loki:
// /ready; Tempo: /ready or /api/echo). Returning nil = ok.
type URLProbe interface {
	Probe(ctx context.Context) error
}

// promQueryAdapter bridges *promquery.Client (which returns
// *InstantResult) into the looser PromQuerier shape so the handler
// doesn't need to import that concrete return type.
type promQueryAdapter struct {
	q func(ctx context.Context, expr string, ts time.Time) error
}

func (a promQueryAdapter) Query(ctx context.Context, expr string, ts time.Time) (any, error) {
	return nil, a.q(ctx, expr, ts)
}

// AdaptPromQuerier wraps a function in the PromQuerier shape. cmd/ongrid
// uses it to inject promquery.Client without leaking that type into the
// handler package.
func AdaptPromQuerier(q func(ctx context.Context, expr string, ts time.Time) error) PromQuerier {
	return promQueryAdapter{q: q}
}

// WebSearchProbe is the narrow surface for the web_search test endpoint.
// The implementation typically runs a 1-result probe against whichever
// provider is currently configured (SearXNG / Tavily / Brave) and
// reports the provider name + a sample title back to the SPA so the
// operator sees a tangible signal that the wiring works.
type WebSearchProbe interface {
	Probe(ctx context.Context) (provider, sample string, err error)
}

// LLMRouterInvalidator drops the in-process LLM provider catalog cache
// so the next chat call re-reads system_settings.llm.* rows. Wired to
// *llm.MultiClient — used by the LLM settings UI's save flow so admin
// edits propagate instantly instead of waiting up to 60s for the
// router's TTL to roll over.
type LLMRouterInvalidator interface {
	Invalidate()
}

// LLMConfigProbe validates an unsaved provider draft. The concrete
// biz/setting implementation performs one minimal upstream completion and
// returns stable, secret-free failure codes.
type LLMConfigProbe interface {
	Probe(ctx context.Context, in bizsetting.LLMProbeInput) (bizsetting.LLMProbeResult, error)
}

// Handler bundles the integration routes.
type Handler struct {
	grafana   GrafanaService
	prom      PromQuerier
	loki      URLProbe
	tempo     URLProbe
	webSearch WebSearchProbe
	llmRouter LLMRouterInvalidator
	llmProbe  LLMConfigProbe
}

// NewHandler builds the handler. prom may be nil when ONGRID_PROM_ENABLED=false;
// the test route then 503s instead of crashing. loki/tempo/webSearch probes
// are optional — when nil the corresponding /test endpoint 503s.
func NewHandler(grafana GrafanaService, prom PromQuerier, loki URLProbe, tempo URLProbe, webSearch WebSearchProbe) *Handler {
	return &Handler{grafana: grafana, prom: prom, loki: loki, tempo: tempo, webSearch: webSearch}
}

// SetLLMRouter wires the LLM provider catalog invalidator post-construction.
// Optional — without it the /v1/integrations/llm/invalidate endpoint 503s,
// admin saves still take effect within the router's 60s TTL.
func (h *Handler) SetLLMRouter(r LLMRouterInvalidator) { h.llmRouter = r }

// SetLLMProbe wires draft validation without widening NewHandler's existing
// integration dependencies.
func (h *Handler) SetLLMProbe(p LLMConfigProbe) { h.llmProbe = p }

// Register attaches routes:
//
//	POST /v1/integrations/grafana/test           (admin)  — verify connectivity
//	POST /v1/integrations/grafana/sync           (admin)  — push folder + datasource + dashboards
//	POST /v1/integrations/prom/test              (admin)  — run "up" PromQL probe
//	GET  /v1/observability/dashboards/{uid}      (any auth user) — proxy Grafana dashboard JSON
//
// The dashboards proxy lives under /v1/observability rather than
// /v1/integrations because it's a read path used by the Monitor page on
// every user load — semantically a query, not an admin action — and we
// want the route to read like a noun, not a verb. Auth is plain bearer
// (the manager middleware already gates this group).
func (h *Handler) Register(r chi.Router) {
	r.Post("/v1/integrations/grafana/test", h.testGrafana)
	r.Post("/v1/integrations/grafana/sync", h.syncGrafana)
	r.Post("/v1/integrations/prom/test", h.testProm)
	r.Post("/v1/integrations/loki/test", h.testLoki)
	r.Post("/v1/integrations/tempo/test", h.testTempo)
	r.Post("/v1/integrations/websearch/test", h.testWebSearch)
	r.Post("/v1/integrations/llm/test", h.testLLMConfiguration)
	r.Post("/v1/integrations/llm/invalidate", h.invalidateLLM)
	r.Get("/v1/observability/dashboards/{uid}", h.fetchDashboard)
}

// testLLMConfiguration validates one unsaved provider draft.
//
// @Summary Validate an unsaved LLM provider configuration
// @Tags integrations
// @Accept json
// @Produce json
// @Param request body bizsetting.LLMProbeInput true "LLM provider draft"
// @Success 200 {object} bizsetting.LLMProbeResult
// @Failure 400 {object} errorBody
// @Failure 401 {object} errorBody
// @Failure 403 {object} errorBody
// @Failure 503 {object} errorBody
// @Router /v1/integrations/llm/test [post]
func (h *Handler) testLLMConfiguration(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.llmProbe == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "llm configuration probe not wired",
			Code:  "llm-probe-disabled",
		})
		return
	}

	const maxBodyBytes = 32 << 10
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var in bizsetting.LLMProbeInput
	if err := dec.Decode(&in); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeErr(w, errors.Join(errs.ErrInvalid, errors.New("request body must contain one JSON object")))
		return
	}

	result, err := h.llmProbe.Probe(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// invalidateLLM drops the LLM router's provider-catalog cache so admin
// edits to system_settings.llm.* rows take effect on the next chat call
// instead of waiting up to 60s for the router's TTL. Admin-only; the
// underlying setting.Service.Set already invalidates its own per-row
// cache, this hook just nudges the router on top.
func (h *Handler) invalidateLLM(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.llmRouter == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "llm router invalidator not wired",
			Code:  "llm-router-disabled",
		})
		return
	}
	h.llmRouter.Invalidate()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// fetchDashboard is the SPA's read-only window into Grafana's dashboard
// JSON. The biz layer holds the Grafana credential (sa_token / api_key);
// the browser never sees it, so this proxy is what makes the
// external-Grafana case work without CORS or cookie sharing.
//
// Response is the verbatim Grafana envelope:
//
//	{ "dashboard": { "uid", "title", "panels": [...] }, "meta": {...} }
//
// We don't reshape — Monitor.tsx walks panels[] and Grafana's full
// schema is richer than we want to model in Go.
func (h *Handler) fetchDashboard(w http.ResponseWriter, r *http.Request) {
	if !h.requireUser(w, r) {
		return
	}
	uid := chi.URLParam(r, "uid")
	if uid == "" {
		writeErr(w, errs.ErrInvalid)
		return
	}
	body, err := h.grafana.FetchDashboardJSON(r.Context(), uid)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *Handler) testGrafana(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if err := h.grafana.Test(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) syncGrafana(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	res, err := h.grafana.Sync(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// testProm runs a tiny PromQL probe ("up") against the configured Prom
// using the admin-supplied auth + URL. Success means the URL is
// reachable, the auth header is accepted, and the TSDB returns a valid
// PromQL response.
func (h *Handler) testProm(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.prom == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "prom not enabled (set ONGRID_PROM_ENABLED=true)",
			Code:  "prom-disabled",
		})
		return
	}
	if _, err := h.prom.Query(r.Context(), "up", time.Now()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// testLoki runs a /ready probe against the configured Loki URL. Loki
// returns 200 OK + body "ready" once the in-memory chunks are replayed.
func (h *Handler) testLoki(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.loki == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "loki probe not wired",
			Code:  "loki-disabled",
		})
		return
	}
	if err := h.loki.Probe(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// testWebSearch runs a 1-result probe against the currently selected
// web search provider (SearXNG / Tavily / Brave). Response includes
// the provider name + a sample title so operators see tangible
// confirmation the wiring is good. SearXNG-unreachable / missing-key
// failures bubble up as 502 with the provider's reason intact.
func (h *Handler) testWebSearch(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.webSearch == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "websearch probe not wired",
			Code:  "websearch-disabled",
		})
		return
	}
	provider, sample, err := h.webSearch.Probe(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"provider": provider,
		"sample":   sample,
	})
}

// testTempo runs a /ready probe against the configured Tempo URL. Tempo
// returns 200 OK once the in-memory blocks are loaded.
func (h *Handler) testTempo(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	if h.tempo == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			Error: "tempo probe not wired",
			Code:  "tempo-disabled",
		})
		return
	}
	if err := h.tempo.Probe(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- helpers ----------------------------------------------------------

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	t, ok := tenantctx.From(r.Context())
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return false
	}
	if t.Role != "admin" {
		writeErr(w, errs.ErrForbidden)
		return false
	}
	return true
}

// requireUser only checks that a tenant context is present. Used for
// read-only endpoints like dashboard fetch where any authenticated user
// (admin or regular operator) may invoke. The auth middleware already
// rejects missing-bearer; this is a defence-in-depth check.
func (h *Handler) requireUser(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := tenantctx.From(r.Context()); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return false
	}
	return true
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if body == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	slug := "internal"
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		status, slug = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		status, slug = http.StatusForbidden, "forbidden"
	case errors.Is(err, errs.ErrInvalid):
		status, slug = http.StatusBadRequest, "invalid"
	case errors.Is(err, errs.ErrNotFound), errors.Is(err, pkggrafana.ErrDashboardNotFound):
		status, slug = http.StatusNotFound, "not-found"
	default:
		// Connection failures, auth-from-Grafana, dashboards parse errors all
		// surface here as 502; the body carries the operator-readable reason.
		status, slug = http.StatusBadGateway, "upstream"
	}
	writeJSON(w, status, errorBody{Error: err.Error(), Code: slug})
}
