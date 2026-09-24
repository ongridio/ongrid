package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	bizaudit "github.com/ongridio/ongrid/internal/manager/biz/audit"
	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	auditmodel "github.com/ongridio/ongrid/internal/manager/model/audit"
	svc "github.com/ongridio/ongrid/internal/manager/service/alert"
	auditmw "github.com/ongridio/ongrid/internal/manager/server/middleware"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type IncidentService interface {
	ListIncidents(ctx context.Context, caller svc.Caller, in svc.IncidentFilter) ([]*svc.Incident, error)
	CountIncidents(ctx context.Context, caller svc.Caller, in svc.IncidentFilter) (int64, error)
	GetIncident(ctx context.Context, caller svc.Caller, id uint64) (*svc.Incident, error)
	AcknowledgeIncident(ctx context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error)
	ResolveIncident(ctx context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error)
	SilenceIncident(ctx context.Context, caller svc.Caller, id uint64, in svc.IncidentSilenceInput) (*svc.Incident, error)
	ListIncidentEvents(ctx context.Context, caller svc.Caller, id uint64, limit int) ([]*svc.Event, error)
	// GetIncidentModel returns the raw storage row for the trigger
	// endpoint (POST /v1/alerts/incidents/{id}/investigation) which
	// must pass the full alertmodel.Incident to Investigator.Enqueue.
	GetIncidentModel(ctx context.Context, caller svc.Caller, id uint64) (*alertmodel.Incident, error)
}

type ChannelService interface {
	ListChannels(ctx context.Context, caller svc.Caller, page, pageSize int) ([]*svc.Channel, error)
	GetChannel(ctx context.Context, caller svc.Caller, id uint64) (*svc.Channel, error)
	CreateChannel(ctx context.Context, caller svc.Caller, in svc.ChannelInput) (*svc.Channel, error)
	UpdateChannel(ctx context.Context, caller svc.Caller, id uint64, in svc.ChannelInput) (*svc.Channel, error)
	DeleteChannel(ctx context.Context, caller svc.Caller, id uint64) error
	TestChannel(ctx context.Context, caller svc.Caller, id uint64) (*svc.ChannelTestResult, error)
}

type RuleService interface {
	ListRules(ctx context.Context, caller svc.Caller, scopeType string) ([]*svc.Rule, error)
	GetRule(ctx context.Context, caller svc.Caller, id uint64) (*svc.Rule, error)
	CreateRule(ctx context.Context, caller svc.Caller, in svc.RuleInput) (*svc.Rule, error)
	UpdateRule(ctx context.Context, caller svc.Caller, id uint64, in svc.RuleInput) (*svc.Rule, error)
	SetRuleEnabled(ctx context.Context, caller svc.Caller, id uint64, enabled bool) (*svc.Rule, error)
	DeleteRule(ctx context.Context, caller svc.Caller, id uint64) error
	PreviewRule(ctx context.Context, caller svc.Caller, in svc.RuleInput, lookbackSeconds int) (*svc.PreviewResult, error)
}

// InvestigationReader is the narrow seam the GET-investigation handler
// needs from the data layer. Implemented by
// data/alert/store.InvestigationRepo.GetByIncident. nil = endpoint
// returns 404 across the board (binary without the investigator wired).
type InvestigationReader interface {
	GetByIncident(ctx context.Context, incidentID uint64) (*alertmodel.InvestigationReport, error)
}

// InvestigationTrigger is the write-side seam — POST endpoint always
// force-enqueues (kills any running worker on this incident, deletes
// the prior report row, spawns fresh). Manual trigger semantics =
// "give me a new investigation now", regardless of prior state.
// Implemented by biz/alert/investigator.Usecase.ForceEnqueue.
type InvestigationTrigger interface {
	ForceEnqueue(ctx context.Context, incident *alertmodel.Incident) error
	// ForceEnqueueWith carries per-request overrides; today the only one
	// that flows through is opts.Locale (from Accept-Language) so a
	// manual re-trigger returns a report in the operator's UI language.
	// Implementations satisfy this by extending ForceEnqueue with the
	// EnqueueOpts struct — see biz/alert/investigator.Usecase.
	ForceEnqueueWith(ctx context.Context, incident *alertmodel.Incident, opts investigator.EnqueueOpts) error
}

// InvestigationReport is the wire-level shape the handler returns.
// Mirrors model/alert.InvestigationReport but with parsed JSON
// payloads so the SPA doesn't have to double-decode and so we don't
// leak the storage representation.
type InvestigationReport struct {
	ID               string          `json:"id"`
	IncidentID       uint64          `json:"incident_id"`
	Status           string          `json:"status"`
	StatusReason     string          `json:"status_reason,omitempty"`
	RootCause        string          `json:"root_cause,omitempty"`
	AffectedWindow   string          `json:"affected_window,omitempty"`
	PinpointedTarget json.RawMessage `json:"pinpointed_target,omitempty"`
	RelatedAlerts    json.RawMessage `json:"related_alerts,omitempty"`
	Evidence         json.RawMessage `json:"evidence,omitempty"`
	SuggestedActions json.RawMessage `json:"suggested_actions,omitempty"`
	FindingsMD       string          `json:"findings_md,omitempty"`
	Confidence       *float64        `json:"confidence,omitempty"`
	ConfidenceFactors json.RawMessage `json:"confidence_factors,omitempty"`
	AuditSessionID   *string         `json:"audit_session_id,omitempty"`
	WorkerID         *string         `json:"worker_id,omitempty"`
	ToolCallCount    int             `json:"tool_call_count"`
	CreatedAt        string          `json:"created_at"`
	ReadyAt          *string         `json:"ready_at,omitempty"`
}

type Handler struct {
	incidents             IncidentService
	channels              ChannelService
	rules                 RuleService
	investigations        InvestigationReader
	investigationsTrigger InvestigationTrigger
	// runtime knobs exposed as informational metadata via
	// GET /v1/alerts/runtime-info — kept on the handler so the SPA can
	// surface "this rule evaluates every 5min" without the operator
	// having to read the env. Default zero values yield 0 in the API,
	// which the SPA treats as "unknown / not surfaced".
	evaluatorInterval time.Duration
	notifyCooldown    time.Duration
}

// NewHandler accepts the three services (or one combined Service satisfying
// all three interfaces). Pass nil for rules when the binary intentionally
// excludes rule endpoints.
func NewHandler(incidents IncidentService, channels ChannelService, rules RuleService) *Handler {
	return &Handler{incidents: incidents, channels: channels, rules: rules}
}

// WithRuntime wires the alert pipeline's runtime knobs (evaluator tick
// interval + notification cooldown) so GET /v1/alerts/runtime-info can
// report them. Optional: a nil call leaves them at 0 and the API
// reports zero-valued fields, which the SPA treats as "unknown".
func (h *Handler) WithRuntime(evaluatorInterval, notifyCooldown time.Duration) *Handler {
	h.evaluatorInterval = evaluatorInterval
	h.notifyCooldown = notifyCooldown
	return h
}

// WithInvestigations wires the read-only investigation reader used by
// GET /v1/alerts/incidents/{id}/investigation. nil keeps the endpoint
// returning 404 — which is the correct "feature off" UX for
// deployments without the investigator enabled.
func (h *Handler) WithInvestigations(r InvestigationReader) *Handler {
	h.investigations = r
	return h
}

// WithInvestigationTrigger wires the POST-to-enqueue endpoint. nil
// makes the POST return 503 "feature disabled".
func (h *Handler) WithInvestigationTrigger(t InvestigationTrigger) *Handler {
	h.investigationsTrigger = t
	return h
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/alerts/incidents", h.listIncidents)
	r.Get("/v1/alerts/incidents/{id}", h.getIncident)
	r.Get("/v1/alerts/incidents/{id}/events", h.listIncidentEvents)
	r.Get("/v1/alerts/incidents/{id}/investigation", h.getIncidentInvestigation)
	r.Post("/v1/alerts/incidents/{id}/investigation", h.triggerIncidentInvestigation)
	r.Post("/v1/alerts/incidents/{id}/ack", h.ackIncident)
	r.Post("/v1/alerts/incidents/{id}/resolve", h.resolveIncident)
	r.Post("/v1/alerts/incidents/{id}/silence", h.silenceIncident)
	r.Get("/v1/alerts/runtime-info", h.getRuntimeInfo)

	r.Get("/v1/notification-channels", h.listChannels)
	r.Get("/v1/notification-channels/{id}", h.getChannel)
	r.Post("/v1/notification-channels", h.createChannel)
	r.Put("/v1/notification-channels/{id}", h.updateChannel)
	r.Delete("/v1/notification-channels/{id}", h.deleteChannel)
	r.Post("/v1/notification-channels/{id}/test", h.testChannel)

	if h.rules != nil {
		r.Get("/v1/alert-rules", h.listRules)
		r.Get("/v1/alert-rules/{id}", h.getRule)
		r.Post("/v1/alert-rules", h.createRule)
		r.Post("/v1/alert-rules/preview", h.previewRule)
		r.Put("/v1/alert-rules/{id}", h.updateRule)
		r.Post("/v1/alert-rules/{id}/enabled", h.setRuleEnabled)
		r.Delete("/v1/alert-rules/{id}", h.deleteRule)
	}
}

type listIncidentsResp struct {
	Items []*svc.Incident `json:"items"`
	Total int             `json:"total"`
}

type listIncidentEventsResp struct {
	Items []*svc.Event `json:"items"`
	Total int          `json:"total"`
}

type silenceReq struct {
	Until  string `json:"until"`
	Reason string `json:"reason"`
}

type listChannelsResp struct {
	Items []*svc.Channel `json:"items"`
	Total int            `json:"total"`
}

type listRulesResp struct {
	Items []*svc.Rule `json:"items"`
	Total int         `json:"total"`
}

type ruleReq struct {
	RuleKey    string              `json:"rule_key"`
	Kind       string              `json:"kind,omitempty"`
	Name       string              `json:"name"`
	ScopeType  string              `json:"scope_type"`
	JoinMode   string              `json:"join_mode"`
	Severity   string              `json:"severity"`
	Enabled    bool                `json:"enabled"`
	Conditions []svc.RuleCondition `json:"conditions,omitempty"`
	Spec       map[string]any      `json:"spec,omitempty"`
	Labels           map[string]string   `json:"labels,omitempty"`
	RunbookURL       string              `json:"runbook_url,omitempty"`
	NotifyChannelIDs []uint64            `json:"notify_channel_ids,omitempty"`
	// 发送策略 (send-policy / dampening). Both zero = disabled. The
	// biz layer rejects mixed (one zero, one >0) configurations with
	// a 400 carrying a clear message.
	NotifyWindowSeconds int `json:"notify_window_seconds,omitempty"`
	NotifyMinFires      int `json:"notify_min_fires,omitempty"`
}

type ruleEnabledReq struct {
	Enabled bool `json:"enabled"`
}

type rulePreviewReq struct {
	ruleReq
	LookbackSeconds int `json:"lookback_seconds,omitempty"`
}

type mutationReq struct {
	Note string `json:"note"`
}

type channelReq struct {
	SMTP     *notify.SMTPConfig `json:"smtp,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"secret,omitempty"`
	Enabled  bool   `json:"enabled"`
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func (h *Handler) listIncidents(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	filter := svc.IncidentFilter{
		Status:   r.URL.Query().Get("status"),
		Severity: r.URL.Query().Get("severity"),
		Query:    r.URL.Query().Get("query"),
		Page:     intQuery(r, "page", 1),
		PageSize: intQuery(r, "page_size", 20),
	}
	items, err := h.incidents.ListIncidents(r.Context(), caller, filter)
	if err != nil {
		writeErr(w, err)
		return
	}
	// total = true unfiltered-by-pagination count, NOT len(items).
	// Sidebar / Home badge polls with page_size=1 to cheaply read this
	// number; len(items) would always be ≤ page_size and silently
	// under-report. Best-effort: a count failure shouldn't break the
	// list, fall back to len(items).
	total, cerr := h.incidents.CountIncidents(r.Context(), caller, filter)
	if cerr != nil {
		total = int64(len(items))
	}
	writeJSON(w, http.StatusOK, listIncidentsResp{Items: items, Total: int(total)})
}

func (h *Handler) getIncident(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	item, err := h.incidents.GetIncident(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// getIncidentInvestigation returns the InvestigationReport bound to
// the incident. 404 when:
//   - investigator not wired (h.investigations == nil)
//   - no report yet for this incident (errs.ErrNotFound from the repo)
// The SPA distinguishes the two cases by reading the response body —
// nil-wired path returns {"status":"feature_disabled"} so the operator
// sees a clear "投资分析未启用" badge instead of a misleading
// "尚未生成" / "investigating..." spinner.
func (h *Handler) getIncidentInvestigation(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Existence-check the incident itself (also enforces tenant scoping
	// + 404 for unknown ids before we touch the investigation repo).
	if _, err := h.incidents.GetIncident(r.Context(), caller, id); err != nil {
		writeErr(w, err)
		return
	}
	if h.investigations == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"incident_id": id,
			"status":      "feature_disabled",
			"status_reason": "investigator not wired on this manager " +
				"(set ONGRID_INVESTIGATOR_ENABLED=true and configure an LLM)",
		})
		return
	}
	rep, err := h.investigations.GetByIncident(r.Context(), id)
	if err != nil {
		if errors.Is(err, errs.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{
				"incident_id": id,
				"status":      "not_started",
			})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, investigationReportToWire(rep))
}

// triggerIncidentInvestigation manually enqueues an investigation for
// an incident that didn't auto-spawn one. Useful for incidents that
// fired before the investigator feature was enabled, were resolved /
// acknowledged before the auto-spawn could run, or where the operator
// wants a re-run. Returns 202 Accepted with the current report shape
// (which will be {status:"pending"} after a successful enqueue, or
// {status:"skipped", status_reason:"..."} when a gate dropped it).
func (h *Handler) triggerIncidentInvestigation(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.investigationsTrigger == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"incident_id":   id,
			"status":        "feature_disabled",
			"status_reason": "investigator not wired on this manager (set ONGRID_INVESTIGATOR_ENABLED=true and configure an LLM)",
		})
		return
	}
	incident, err := h.incidents.GetIncidentModel(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	// ForceEnqueue replaces any existing row (kills running worker if
	// any), then spawns fresh. The SPA continues to GET the same
	// incident's investigation endpoint and watches status transition
	// pending → running → ready. Threads the operator's Accept-Language
	// through so the regenerated report matches the current UI locale —
	// see [[feedback_ai_output_locale]]: AI 输出语言跟随用户 UI locale.
	opts := investigator.EnqueueOpts{Locale: localeFromRequest(r)}
	if err := h.investigationsTrigger.ForceEnqueueWith(r.Context(), incident, opts); err != nil {
		writeErr(w, fmt.Errorf("%w: %s", errs.ErrInvalid, err))
		return
	}

	// Best-effort echo of the current row state (post-enqueue) so the
	// SPA can avoid one extra round-trip. Read may race the row insert
	// (Enqueue is async); on race we return a synthetic "pending" stub.
	if h.investigations != nil {
		if rep, getErr := h.investigations.GetByIncident(r.Context(), id); getErr == nil {
			writeJSON(w, http.StatusAccepted, investigationReportToWire(rep))
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"incident_id": id,
		"status":      "pending",
	})
}

// investigationReportToWire converts the DB row into the SPA's JSON
// shape: parses the stored *_json string columns into json.RawMessage
// so the SPA doesn't double-decode, and formats time fields as
// ISO-8601 strings.
func investigationReportToWire(rep *alertmodel.InvestigationReport) *InvestigationReport {
	out := &InvestigationReport{
		ID:                rep.ID,
		IncidentID:        rep.IncidentID,
		Status:            rep.Status,
		StatusReason:      rep.StatusReason,
		RootCause:         rep.RootCause,
		AffectedWindow:    rep.AffectedWindow,
		PinpointedTarget:  rawOrNil(rep.PinpointedTargetJSON),
		RelatedAlerts:     rawOrNil(rep.RelatedAlertsJSON),
		Evidence:          rawOrNil(rep.EvidenceJSON),
		SuggestedActions:  rawOrNil(rep.SuggestedActionsJSON),
		FindingsMD:        rep.FindingsMD,
		Confidence:        rep.Confidence,
		ConfidenceFactors: rawOrNil(rep.ConfidenceFactorsJSON),
		AuditSessionID:    rep.AuditSessionID,
		WorkerID:          rep.WorkerID,
		ToolCallCount:     rep.ToolCallCount,
		CreatedAt:         rep.CreatedAt.UTC().Format(time.RFC3339),
	}
	if rep.ReadyAt != nil {
		s := rep.ReadyAt.UTC().Format(time.RFC3339)
		out.ReadyAt = &s
	}
	return out
}

func rawOrNil(s string) json.RawMessage {
	s = stripWhitespace(s)
	if s == "" || s == "null" {
		return nil
	}
	return json.RawMessage(s)
}

func stripWhitespace(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != ' ' && s[i] != '\t' && s[i] != '\n' && s[i] != '\r' {
			return s[i:]
		}
	}
	return ""
}

func (h *Handler) ackIncident(w http.ResponseWriter, r *http.Request) {
	h.mutateIncident(w, r, auditmodel.ActionIncidentAck, func(ctx context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error) {
		return h.incidents.AcknowledgeIncident(ctx, caller, id, in)
	})
}

func (h *Handler) resolveIncident(w http.ResponseWriter, r *http.Request) {
	h.mutateIncident(w, r, auditmodel.ActionIncidentResolve, func(ctx context.Context, caller svc.Caller, id uint64, in svc.IncidentMutationInput) (*svc.Incident, error) {
		return h.incidents.ResolveIncident(ctx, caller, id, in)
	})
}

func (h *Handler) listIncidentEvents(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := intQuery(r, "limit", 200)
	items, err := h.incidents.ListIncidentEvents(r.Context(), caller, id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listIncidentEventsResp{Items: items, Total: len(items)})
}

func (h *Handler) silenceIncident(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req silenceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.incidents.SilenceIncident(r.Context(), caller, id, svc.IncidentSilenceInput{
		Until:  req.Until,
		Reason: req.Reason,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionIncidentSilence,
		ResourceType: auditmodel.ResourceIncident,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: item.RuleName,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"until": req.Until, "reason": req.Reason},
	})
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) mutateIncident(w http.ResponseWriter, r *http.Request, action string, fn func(context.Context, svc.Caller, uint64, svc.IncidentMutationInput) (*svc.Incident, error)) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req mutationReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := fn(r.Context(), caller, id, svc.IncidentMutationInput{Note: req.Note})
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       action,
		ResourceType: auditmodel.ResourceIncident,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: item.RuleName,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"note": req.Note},
	})
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	page := intQuery(r, "page", 1)
	pageSize := intQuery(r, "page_size", 20)
	items, err := h.channels.ListChannels(r.Context(), caller, page, pageSize)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listChannelsResp{Items: items, Total: len(items)})
}

func (h *Handler) getChannel(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	item, err := h.channels.GetChannel(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req channelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.channels.CreateChannel(r.Context(), caller, svc.ChannelInput{
		SMTP:     req.SMTP,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		Secret:   req.Secret,
		Enabled:  req.Enabled,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionChannelCreate,
		ResourceType: auditmodel.ResourceChannel,
		ResourceID:   strconv.FormatUint(item.ID, 10),
		ResourceName: item.Name,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"type": req.Type, "enabled": req.Enabled},
	})
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req channelReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.channels.UpdateChannel(r.Context(), caller, id, svc.ChannelInput{
		SMTP:     req.SMTP,
		Name:     req.Name,
		Type:     req.Type,
		Endpoint: req.Endpoint,
		Secret:   req.Secret,
		Enabled:  req.Enabled,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionChannelUpdate,
		ResourceType: auditmodel.ResourceChannel,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: item.Name,
		Status:       auditmodel.StatusSuccess,
	})
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.channels.DeleteChannel(r.Context(), caller, id); err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionChannelDelete,
		ResourceType: auditmodel.ResourceChannel,
		ResourceID:   strconv.FormatUint(id, 10),
		Status:       auditmodel.StatusSuccess,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	out, err := h.channels.TestChannel(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	scope := r.URL.Query().Get("scope")
	items, err := h.rules.ListRules(r.Context(), caller, scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listRulesResp{Items: items, Total: len(items)})
}

func (h *Handler) getRule(w http.ResponseWriter, r *http.Request) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	item, err := h.rules.GetRule(r.Context(), caller, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req ruleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.rules.CreateRule(r.Context(), caller, ruleReqToSvc(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRuleCreate,
		ResourceType: auditmodel.ResourceRule,
		ResourceID:   strconv.FormatUint(item.ID, 10),
		ResourceName: item.Name,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"kind": req.Kind, "severity": req.Severity, "enabled": req.Enabled},
	})
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req ruleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.rules.UpdateRule(r.Context(), caller, id, ruleReqToSvc(req))
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRuleUpdate,
		ResourceType: auditmodel.ResourceRule,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: item.Name,
		Status:       auditmodel.StatusSuccess,
	})
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) setRuleEnabled(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var req ruleEnabledReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	item, err := h.rules.SetRuleEnabled(r.Context(), caller, id, req.Enabled)
	if err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRuleUpdate,
		ResourceType: auditmodel.ResourceRule,
		ResourceID:   strconv.FormatUint(id, 10),
		ResourceName: item.Name,
		Status:       auditmodel.StatusSuccess,
		Payload:      map[string]any{"enabled": req.Enabled},
	})
	writeJSON(w, http.StatusOK, item)
}

// previewRule runs the in-flight rule input against a backfill window
// (default 24h) and returns the would-have-fired count + sample fires.
// Gated on the same admin role as PUT/POST so unprivileged tokens can't
// burn cycles on a 24h Prom range.
func (h *Handler) previewRule(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	var req rulePreviewReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, errors.Join(errs.ErrInvalid, err))
		return
	}
	out, err := h.rules.PreviewRule(r.Context(), caller, ruleReqToSvc(req.ruleReq), req.LookbackSeconds)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := parseID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := h.rules.DeleteRule(r.Context(), caller, id); err != nil {
		writeErr(w, err)
		return
	}
	auditmw.SetAuditEvent(r, bizaudit.Event{
		Action:       auditmodel.ActionRuleDelete,
		ResourceType: auditmodel.ResourceRule,
		ResourceID:   strconv.FormatUint(id, 10),
		Status:       auditmodel.StatusSuccess,
	})
	w.WriteHeader(http.StatusNoContent)
}


func ruleReqToSvc(req ruleReq) svc.RuleInput {
	return svc.RuleInput{
		RuleKey:             req.RuleKey,
		Kind:                req.Kind,
		Name:                req.Name,
		ScopeType:           req.ScopeType,
		JoinMode:            req.JoinMode,
		Severity:            req.Severity,
		Enabled:             req.Enabled,
		Conditions:          req.Conditions,
		Spec:                req.Spec,
		Labels:              req.Labels,
		RunbookURL:          req.RunbookURL,
		NotifyChannelIDs:    req.NotifyChannelIDs,
		NotifyWindowSeconds: req.NotifyWindowSeconds,
		NotifyMinFires:      req.NotifyMinFires,
	}
}

func callerFromRequest(r *http.Request) (svc.Caller, bool) {
	tenant, ok := tenantctx.From(r.Context())
	if !ok {
		return svc.Caller{}, false
	}
	return svc.Caller{UserID: tenant.UserID, Role: tenant.Role}, true
}

func requireAdmin(w http.ResponseWriter, r *http.Request) (svc.Caller, bool) {
	caller, ok := callerFromRequest(r)
	if !ok {
		writeErr(w, errs.ErrUnauthorized)
		return svc.Caller{}, false
	}
	if caller.Role != "admin" {
		writeErr(w, errs.ErrForbidden)
		return svc.Caller{}, false
	}
	return caller, true
}

func parseID(r *http.Request) (uint64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.Join(errs.ErrInvalid, fmt.Errorf("id: %w", err))
	}
	if id == 0 {
		return 0, fmt.Errorf("%w: id must be positive", errs.ErrInvalid)
	}
	return id, nil
}

func intQuery(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
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
	code := errCode(err)
	body := errorBody{Error: err.Error(), Code: errSlug(err)}
	writeJSON(w, code, body)
}

func errCode(err error) int {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, errs.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, errs.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, errs.ErrNotWiredYet):
		return http.StatusNotImplemented
	case errors.Is(err, errs.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// localeFromRequest picks the operator's preferred locale from the
// Accept-Language header (sent by the SPA from web/src/i18n/locale.ts).
// Returns a primary subtag — "en" or "zh" — or "" when unset / unknown.
// The investigator's Config.DefaultLocale catches the empty case for
// auto-fire / backfill (no request context). See
// [[feedback_ai_output_locale]] for the convention.
func localeFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	raw := strings.TrimSpace(r.Header.Get("Accept-Language"))
	if raw == "" {
		return ""
	}
	// Take the first language-tag; ignore q-values. "en-US,en;q=0.9" → "en-US".
	first := strings.SplitN(raw, ",", 2)[0]
	// Drop region: "en-US" → "en".
	primary := strings.ToLower(strings.SplitN(strings.TrimSpace(first), "-", 2)[0])
	switch primary {
	case "en", "zh":
		return primary
	default:
		return ""
	}
}

// getRuntimeInfo exposes the alert pipeline's runtime cadence knobs so
// the SPA can show operators "this rule evaluates every N minutes" without
// the operator having to read env vars. Values come from env at startup
// (ONGRID_ALERT_EVAL_INTERVAL / ONGRID_ALERT_COOLDOWN) — they're per-
// deployment, not per-rule, so the SPA shows them as a global banner /
// chip rather than per-rule editable fields.
func (h *Handler) getRuntimeInfo(w http.ResponseWriter, r *http.Request) {
	if _, ok := callerFromRequest(r); !ok {
		writeErr(w, errs.ErrUnauthorized)
		return
	}
	out := map[string]any{
		"evaluator_interval_seconds": int64(h.evaluatorInterval / time.Second),
		"notify_cooldown_seconds":    int64(h.notifyCooldown / time.Second),
	}
	writeJSON(w, http.StatusOK, out)
}

func errSlug(err error) string {
	switch {
	case errors.Is(err, errs.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errs.ErrForbidden):
		return "forbidden"
	case errors.Is(err, errs.ErrNotFound):
		return "not-found"
	case errors.Is(err, errs.ErrNotWiredYet):
		return "not-wired-yet"
	case errors.Is(err, errs.ErrInvalid):
		return "invalid-argument"
	default:
		return "internal"
	}
}
