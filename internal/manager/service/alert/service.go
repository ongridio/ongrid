// Package alert is the manager-facing application service for the alert
// control plane. It is the only surface HTTP handlers consume:
// they never reach into biz.Usecase or model directly. Validation lives in
// biz.Usecase; this layer translates between transport DTOs and biz inputs.
package alert

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	bizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
)

type Caller struct {
	UserID uint64
	Role   string
}

type IncidentFilter struct {
	Status   string
	Severity string
	Query    string
	Page     int
	PageSize int
}

type Incident struct {
	ID             uint64            `json:"id"`
	RuleKey        string            `json:"rule_key"`
	RuleName       string            `json:"rule_name"`
	Severity       string            `json:"severity"`
	Status         string            `json:"status"`
	Summary        string            `json:"summary"`
	TargetType     string            `json:"target_type"`
	TargetID       string            `json:"target_id,omitempty"`
	TargetName     string            `json:"target_name,omitempty"`
	RunbookURL     string            `json:"runbook_url,omitempty"`
	DedupeKey      string            `json:"dedupe_key,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	EventCount     uint64            `json:"event_count"`
	Value          *float64          `json:"value,omitempty"`
	Threshold      *float64          `json:"threshold,omitempty"`
	FiredAt        time.Time         `json:"fired_at"`
	LastFiredAt    time.Time         `json:"last_fired_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	AcknowledgedAt *time.Time        `json:"acknowledged_at,omitempty"`
	ResolvedAt     *time.Time        `json:"resolved_at,omitempty"`
}

type IncidentMutationInput struct {
	Note string
}

// IncidentSilenceInput carries operator-supplied silence parameters from the
// HTTP layer. Until accepts duration ("30m", "2h"), RFC3339 timestamp, or
// unix seconds — biz.parseSilenceUntil owns the parsing.
type IncidentSilenceInput struct {
	Until  string
	Reason string
}

// Event is the transport-side timeline entry for an incident. Mirrors a
// curated subset of model.Event so the UI can render the timeline without
// leaking storage internals (snapshot_json, deleted_at, etc.).
type Event struct {
	ID             uint64    `json:"id"`
	IncidentID     uint64    `json:"incident_id"`
	EventType      string    `json:"event_type"`
	StatusAfter    string    `json:"status_after,omitempty"`
	Severity       string    `json:"severity,omitempty"`
	Title          string    `json:"title,omitempty"`
	Message        string    `json:"message,omitempty"`
	ActorType      string    `json:"actor_type"`
	OperatorUserID *uint64   `json:"operator_user_id,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
	CreatedAt      time.Time `json:"created_at"`
}

type Channel struct {
	ID             uint64    `json:"id"`
	Name           string    `json:"name"`
	Type           string    `json:"type"`
	Enabled        bool      `json:"enabled"`
	EndpointMasked string    `json:"endpoint,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type ChannelInput struct {
	Name     string
	Type     string
	Endpoint string
	Secret   string
	Enabled  bool
}

type ChannelTestResult struct {
	Accepted bool   `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

// RuleCondition mirrors model.RuleCondition for transport.
type RuleCondition struct {
	Metric     string  `json:"metric"`
	Operator   string  `json:"operator"`
	Threshold  float64 `json:"threshold"`
	Window     string  `json:"window,omitempty"`
	For        string  `json:"for,omitempty"`
	Aggregator string  `json:"aggregator,omitempty"`
}

// Rule is the DTO returned by rule endpoints.
type Rule struct {
	ID         uint64 `json:"id"`
	RuleKey    string `json:"rule_key"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	SourceType string `json:"source_type"`
	ScopeType  string `json:"scope_type"`
	JoinMode   string `json:"join_mode"`
	Severity   string `json:"severity"`
	Enabled    bool   `json:"enabled"`
	// Conditions is populated for kind=metric_threshold; empty for other kinds.
	Conditions []RuleCondition `json:"conditions,omitempty"`
	// Spec is populated for non-metric_threshold kinds (decoded from
	// ConditionsJSON), so the UI can render kind-specific forms.
	Spec       map[string]any    `json:"spec,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	RunbookURL string            `json:"runbook_url,omitempty"`
	// NotifyChannelIDs lists channels this rule pins notifications to.
	// Empty / nil → router falls back to global channel filters.
	NotifyChannelIDs []uint64 `json:"notify_channel_ids,omitempty"`
	// NotifyWindowSeconds + NotifyMinFires expose the per-rule
	// 「发送策略」 dampening config. Both zero = disabled (UI hides
	// the "已启用" badge); both > 0 = enabled.
	NotifyWindowSeconds int       `json:"notify_window_seconds,omitempty"`
	NotifyMinFires      int       `json:"notify_min_fires,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// RuleInput is the transport-side rule create/update payload.
type RuleInput struct {
	RuleKey   string
	Kind      string
	Name      string
	ScopeType string
	JoinMode  string
	Severity  string
	Enabled   bool
	// Conditions populates kind=metric_threshold rules.
	Conditions []RuleCondition
	// Spec is the kind-specific opaque payload for non-metric_threshold
	// kinds (e.g. {threshold_seconds: 60} for edge_offline). HTTP layer
	// reads it as map[string]any, biz validates per kind.
	Spec             map[string]any
	Labels           map[string]string
	RunbookURL       string
	NotifyChannelIDs []uint64 `json:"notify_channel_ids,omitempty"`
	// NotifyWindowSeconds + NotifyMinFires carry the 发送策略 dampening
	// config from the editor. Both zero = disabled.
	NotifyWindowSeconds int `json:"notify_window_seconds,omitempty"`
	NotifyMinFires      int `json:"notify_min_fires,omitempty"`
}

// Notifier is the narrow notify-router surface this service depends on for
// TestChannel deliveries. *notify.Router satisfies it; tests stub it.
type Notifier interface {
	Send(ctx context.Context, msg notify.Message, channels ...string) error
}

// PreviewSample mirrors bizalert.PreviewSample for transport.
type PreviewSample struct {
	Timestamp time.Time         `json:"ts"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Summary   string            `json:"summary"`
}

// PreviewSeriesPoint is one (ts, value) pair for the chart preview.
type PreviewSeriesPoint struct {
	Timestamp time.Time `json:"ts"`
	Value     float64   `json:"value"`
}

// PreviewResult mirrors bizalert.PreviewResult for transport.
type PreviewResult struct {
	FireCount     int                  `json:"fire_count"`
	FirstFireAt   *time.Time           `json:"first_fire_at,omitempty"`
	LastFireAt    *time.Time           `json:"last_fire_at,omitempty"`
	Samples       []PreviewSample      `json:"samples,omitempty"`
	Series        []PreviewSeriesPoint `json:"series,omitempty"`
	Threshold     *float64             `json:"threshold,omitempty"`
	Unit          string               `json:"unit,omitempty"`
	SkippedReason string               `json:"skipped_reason,omitempty"`
}

// Service wires HTTP handlers to the alert biz usecase + repo. It is
// constructed via New (DB-backed wiring) or NewStub (validation-only) for
// tests that don't want a live usecase.
type Service struct {
	uc          *bizalert.Usecase
	repo        bizalert.Repo
	notifier    Notifier
	previewDeps bizalert.PreviewDeps
	log         *slog.Logger
}

// New builds the wired application service. uc and repo must both be
// non-nil; pass NewStub() in tests where you only want input validation.
// notifier is optional — when nil, TestChannel returns ErrNotWiredYet.
func New(uc *bizalert.Usecase, repo bizalert.Repo, notifier Notifier, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uc: uc, repo: repo, notifier: notifier, log: log}
}

// SetPreviewDeps wires the read-only preview clients (Prom range + Loki
// range + event counter). Optional — when unset the corresponding kinds
// return skipped_reason instead of failing.
func (s *Service) SetPreviewDeps(d bizalert.PreviewDeps) {
	s.previewDeps = d
}

// NewStub returns a Service whose methods short-circuit to ErrNotWiredYet
// after minimal input validation. Useful in HTTP unit tests that don't want
// to stand up a biz usecase.
func NewStub() *Service { return &Service{} }

// ListIncidents returns the paged incident view. status filter is exact-match.
func (s *Service) ListIncidents(ctx context.Context, _ Caller, in IncidentFilter) ([]*Incident, error) {
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	limit, offset := pageBounds(in.Page, in.PageSize)
	rows, err := s.uc.ListIncidents(ctx, bizalert.IncidentFilter{
		Status:   in.Status,
		Severity: in.Severity,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Incident, 0, len(rows))
	for _, r := range rows {
		out = append(out, toServiceIncident(r))
	}
	return out, nil
}

// CountIncidents returns the unpaginated total matching the filter.
// Used by the http handler so list responses can carry the real total
// alongside the page items.
func (s *Service) CountIncidents(ctx context.Context, _ Caller, in IncidentFilter) (int64, error) {
	if s.uc == nil {
		return 0, errs.ErrNotWiredYet
	}
	return s.uc.CountIncidents(ctx, bizalert.IncidentFilter{
		Status:   in.Status,
		Severity: in.Severity,
	})
}

// GetIncident returns a single incident.
func (s *Service) GetIncident(ctx context.Context, _ Caller, id uint64) (*Incident, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := s.uc.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceIncident(row), nil
}

// GetIncidentModel returns the raw storage row for an incident — used
// by the manual investigation trigger which needs to pass the full
// model.Incident (with DeviceID + Rule fields) into the Investigator.
// The Caller parameter is kept for the symmetric signature with
// GetIncident; future RBAC checks attach here.
func (s *Service) GetIncidentModel(ctx context.Context, _ Caller, id uint64) (*model.Incident, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	return s.uc.GetIncident(ctx, id)
}

// AcknowledgeIncident transitions an incident to acknowledged. Note is
// optional — single-click ack from the UI is acceptable; the operator
// identity is captured from caller and an empty Message is stored on
// the timeline event.
func (s *Service) AcknowledgeIncident(ctx context.Context, caller Caller, id uint64, in IncidentMutationInput) (*Incident, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	if err := s.uc.AckIncident(ctx, id, caller.UserID, in.Note); err != nil {
		return nil, err
	}
	row, err := s.uc.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceIncident(row), nil
}

// ResolveIncident transitions an incident to resolved.
func (s *Service) ResolveIncident(ctx context.Context, caller Caller, id uint64, in IncidentMutationInput) (*Incident, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Note) == "" {
		return nil, fmt.Errorf("%w: resolve note required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	if err := s.uc.ResolveIncident(ctx, id, caller.UserID, in.Note); err != nil {
		return nil, err
	}
	row, err := s.uc.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceIncident(row), nil
}

// ListIncidentEvents returns the timeline (most-recent-first via repo) for an
// incident, capped at limit (defaults to 200, max 1000).
func (s *Service) ListIncidentEvents(ctx context.Context, _ Caller, incidentID uint64, limit int) ([]*Event, error) {
	if incidentID == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.uc.ListEvents(ctx, incidentID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, toServiceEvent(r))
	}
	return out, nil
}

// SilenceIncident transitions an incident to silenced and creates the
// matching silence row. Until accepts duration / RFC3339 / unix seconds; biz
// owns the parsing.
func (s *Service) SilenceIncident(ctx context.Context, caller Caller, id uint64, in IncidentSilenceInput) (*Incident, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Until) == "" {
		return nil, fmt.Errorf("%w: silence until required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return nil, fmt.Errorf("%w: silence reason required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	if err := s.uc.SilenceIncident(ctx, id, caller.UserID, in.Until, in.Reason); err != nil {
		return nil, err
	}
	row, err := s.uc.GetIncident(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceIncident(row), nil
}

// ListChannels returns the configured notification channels. PR-A seeds rows
// from ONGRID_NOTIFY_* env vars; PR-D unlocks UI-side CRUD.
func (s *Service) ListChannels(ctx context.Context, _ Caller, page, pageSize int) ([]*Channel, error) {
	if s.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	limit, offset := pageBounds(page, pageSize)
	rows, err := s.repo.ListChannels(ctx, bizalert.ChannelFilter{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, toServiceChannel(r))
	}
	return out, nil
}

func (s *Service) GetChannel(ctx context.Context, _ Caller, id uint64) (*Channel, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: channel id required", errs.ErrInvalid)
	}
	if s.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceChannel(row), nil
}

// CreateChannel persists a UI-supplied channel row. Endpoint + optional
// secret are encoded into ConfigJSON in the same shape SeedChannelsFromConfig
// uses, so env-seeded and UI-managed rows are interchangeable on read.
func (s *Service) CreateChannel(ctx context.Context, caller Caller, in ChannelInput) (*Channel, error) {
	if err := validateChannelInput(in, true); err != nil {
		return nil, err
	}
	if s.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row := &model.Channel{
		Name:        strings.TrimSpace(in.Name),
		ChannelType: strings.TrimSpace(in.Type),
		Enabled:     in.Enabled,
		ConfigJSON:  encodeChannelConfig(in.Endpoint, in.Secret),
	}
	if caller.UserID != 0 {
		uid := caller.UserID
		row.CreatedBy = &uid
	}
	if err := s.repo.CreateChannel(ctx, row); err != nil {
		return nil, err
	}
	return toServiceChannel(row), nil
}

// UpdateChannel applies the input fields to an existing channel. When the
// caller submits an empty Secret, the previously-stored secret_set marker is
// preserved; an explicit "-" clears it (mirrors the "no change vs. clear"
// pattern used by the rule editor for optional fields).
func (s *Service) UpdateChannel(ctx context.Context, _ Caller, id uint64, in ChannelInput) (*Channel, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: channel id required", errs.ErrInvalid)
	}
	if err := validateChannelInput(in, false); err != nil {
		return nil, err
	}
	if s.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	merged := *existing
	merged.Name = strings.TrimSpace(in.Name)
	if t := strings.TrimSpace(in.Type); t != "" {
		merged.ChannelType = t
	}
	merged.Enabled = in.Enabled
	merged.ConfigJSON = mergeChannelConfig(existing.ConfigJSON, in.Endpoint, in.Secret)
	if err := s.repo.UpdateChannel(ctx, id, &merged); err != nil {
		return nil, err
	}
	row, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceChannel(row), nil
}

func (s *Service) DeleteChannel(ctx context.Context, _ Caller, id uint64) error {
	if id == 0 {
		return fmt.Errorf("%w: channel id required", errs.ErrInvalid)
	}
	if s.repo == nil {
		return errs.ErrNotWiredYet
	}
	// Refuse delete when any rule pins this channel via
	// notify_channel_ids_json — otherwise an incident on that rule
	// would silently fall through to the global default channels.
	count, err := s.repo.CountRulesReferencingChannel(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: 该通道被 %d 条规则关联，删除前请先在规则中取消勾选", errs.ErrInvalid, count)
	}
	return s.repo.DeleteChannel(ctx, id)
}

// TestChannel synthesises an info-severity probe message and runs it through
// the notify router using the channel's name. Accepted=true means the router
// returned no error; accepted=false carries the error message verbatim so the
// UI can show the operator the actual upstream failure.
func (s *Service) TestChannel(ctx context.Context, _ Caller, id uint64) (*ChannelTestResult, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: channel id required", errs.ErrInvalid)
	}
	if s.repo == nil || s.notifier == nil {
		return nil, errs.ErrNotWiredYet
	}
	channel, err := s.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	msg := notify.Message{
		Subject:    fmt.Sprintf("[ongrid] 测试投递 — %s", channel.Name),
		Body:       "这是一条来自 ongrid 通知渠道测试的消息，收到即视为渠道投递链路正常。",
		Severity:   notify.SeverityInfo,
		Source:     "channel_test",
		DedupeKey:  fmt.Sprintf("channel-test-%d", channel.ID),
		OccurredAt: time.Now().UTC(),
	}
	// Build the typed sender from the channel's ChannelType + ConfigJSON and
	// send DIRECTLY — a manual test must attempt real delivery regardless of
	// the global ONGRID_NOTIFY_ENABLED master switch (an operator testing a
	// channel wants to know it works, not get a silent no-op). This also
	// fixes the prior by-name path, which only matched env-config channel
	// names and returned "channel not configured" for UI-created channels.
	sender, berr := bizalert.BuildSenderFromChannel(channel)
	if berr != nil {
		return &ChannelTestResult{Accepted: false, Message: berr.Error()}, nil
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sendErr := sender.Send(sctx, msg)
	out := &ChannelTestResult{Accepted: sendErr == nil}
	if sendErr != nil {
		out.Message = sendErr.Error()
	} else {
		out.Message = fmt.Sprintf("已投递到 %s", channel.Name)
	}
	return out, nil
}

// ListRules returns the rule set, optionally filtered by scope.
func (s *Service) ListRules(ctx context.Context, _ Caller, scopeType string) ([]*Rule, error) {
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	rows, err := s.uc.ListRules(ctx, scopeType)
	if err != nil {
		return nil, err
	}
	out := make([]*Rule, 0, len(rows))
	for _, r := range rows {
		dto, err := toServiceRule(r)
		if err != nil {
			s.log.Warn("alert: rule decode failed",
				slog.Uint64("rule_id", r.ID),
				slog.Any("err", err),
			)
			continue
		}
		out = append(out, dto)
	}
	return out, nil
}

func (s *Service) GetRule(ctx context.Context, _ Caller, id uint64) (*Rule, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := s.uc.GetRule(ctx, id)
	if err != nil {
		return nil, err
	}
	return toServiceRule(row)
}

func (s *Service) CreateRule(ctx context.Context, caller Caller, in RuleInput) (*Rule, error) {
	if err := validateRuleInput(in, true); err != nil {
		return nil, err
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	createdBy := caller.UserID
	row, err := s.uc.CreateRule(ctx, toBizRuleInput(in), &createdBy)
	if err != nil {
		return nil, err
	}
	return toServiceRule(row)
}

func (s *Service) UpdateRule(ctx context.Context, _ Caller, id uint64, in RuleInput) (*Rule, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	if err := validateRuleInput(in, false); err != nil {
		return nil, err
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := s.uc.UpdateRule(ctx, id, toBizRuleInput(in))
	if err != nil {
		return nil, err
	}
	return toServiceRule(row)
}

func (s *Service) SetRuleEnabled(ctx context.Context, _ Caller, id uint64, enabled bool) (*Rule, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := s.uc.SetRuleEnabled(ctx, id, enabled)
	if err != nil {
		return nil, err
	}
	return toServiceRule(row)
}

// PreviewRule runs the in-flight rule input against the past N seconds of
// data without persisting anything. lookbackSeconds defaults to 86400 when
// non-positive. The 10s deadline matches Prom range-query expectations on
// cold blocks.
func (s *Service) PreviewRule(ctx context.Context, _ Caller, in RuleInput, lookbackSeconds int) (*PreviewResult, error) {
	// Preview is "run the query without committing" — much looser than save:
	// rule_key / name / severity can be empty, only the kind-specific spec
	// must be present. Lets users iterate on a threshold + expr before they
	// even decide what to call the rule.
	if err := validatePreviewInput(in); err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := bizalert.PreviewRule(pctx, bizalert.PreviewInput{
		Input:           toBizRuleInput(in),
		LookbackSeconds: lookbackSeconds,
	}, s.previewDeps)
	if err != nil {
		return nil, err
	}
	return toServicePreview(res), nil
}

func toServicePreview(r *bizalert.PreviewResult) *PreviewResult {
	if r == nil {
		return &PreviewResult{}
	}
	out := &PreviewResult{
		FireCount:     r.FireCount,
		FirstFireAt:   r.FirstFireAt,
		LastFireAt:    r.LastFireAt,
		SkippedReason: r.SkippedReason,
		Unit:          r.Unit,
	}
	if r.Threshold != nil && isFinitePreviewValue(*r.Threshold) {
		tcopy := *r.Threshold
		out.Threshold = &tcopy
	}
	for _, p := range r.Series {
		if !isFinitePreviewValue(p.Value) {
			continue
		}
		out.Series = append(out.Series, PreviewSeriesPoint{
			Timestamp: p.Timestamp,
			Value:     p.Value,
		})
	}
	for _, s := range r.Samples {
		if !isFinitePreviewValue(s.Value) {
			continue
		}
		out.Samples = append(out.Samples, PreviewSample{
			Timestamp: s.Timestamp,
			Labels:    s.Labels,
			Value:     s.Value,
			Summary:   s.Summary,
		})
	}
	return out
}

func isFinitePreviewValue(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func (s *Service) DeleteRule(ctx context.Context, _ Caller, id uint64) error {
	if id == 0 {
		return fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	if s.uc == nil {
		return errs.ErrNotWiredYet
	}
	return s.uc.DeleteRule(ctx, id)
}

// validatePreviewInput is the loose validator used for the editor's
// preview / 查询 button. It only checks the bare minimum the kind-
// specific previewer needs to run a query — rule_key, name, severity,
// runbook are intentionally NOT required.
//
// metric_threshold is a UI-only INPUT kind: the friendly form sends
// kind=metric_threshold + conditions[], biz compiles it to metric_raw
// before reaching the previewer / DB. We still validate the
// conditions[] shape here so the editor's 试算 button gets a clean
// 400 instead of a confusing downstream error.
func validatePreviewInput(in RuleInput) error {
	if in.Kind == model.RuleKindMetricThreshold && len(in.Conditions) == 0 {
		return fmt.Errorf("%w: at least one condition required for metric_threshold preview", errs.ErrInvalid)
	}
	return nil
}

func validateRuleInput(in RuleInput, requireKey bool) error {
	if requireKey && strings.TrimSpace(in.RuleKey) == "" {
		return fmt.Errorf("%w: rule_key required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: rule name required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Severity) == "" {
		return fmt.Errorf("%w: severity required", errs.ErrInvalid)
	}
	// metric_threshold (UI-only friendly form) needs at least one
	// condition; other kinds rely on the biz-layer kind-specific
	// validator that runs after this.
	if in.Kind == model.RuleKindMetricThreshold && len(in.Conditions) == 0 {
		return fmt.Errorf("%w: at least one condition required", errs.ErrInvalid)
	}
	return nil
}

func validateChannelInput(in ChannelInput, requireType bool) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: channel name required", errs.ErrInvalid)
	}
	if requireType && strings.TrimSpace(in.Type) == "" {
		return fmt.Errorf("%w: channel type required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Endpoint) == "" {
		return fmt.Errorf("%w: channel endpoint required", errs.ErrInvalid)
	}
	return nil
}

func toServiceIncident(r *model.Incident) *Incident {
	if r == nil {
		return nil
	}
	out := &Incident{
		ID:             r.ID,
		RuleKey:        r.Rule,
		RuleName:       r.RuleName,
		Severity:       r.Severity,
		Status:         r.Status,
		Summary:        r.Summary,
		RunbookURL:     r.RunbookURL,
		DedupeKey:      r.DedupeKey,
		EventCount:     r.EventCount,
		Value:          r.Value,
		Threshold:      r.Threshold,
		FiredAt:        r.FirstFiredAt,
		LastFiredAt:    r.LastFiredAt,
		UpdatedAt:      r.UpdatedAt,
		AcknowledgedAt: r.AcknowledgedAt,
		ResolvedAt:     r.ResolvedAt,
	}
	if r.DeviceID != nil {
		out.TargetType = "edge"
		out.TargetID = fmt.Sprintf("%d", *r.DeviceID)
	}
	if r.LabelsJSON != "" {
		var labels map[string]string
		if err := json.Unmarshal([]byte(r.LabelsJSON), &labels); err == nil {
			out.Labels = labels
			// Some rules (e.g. device_offline — a global PromQL rule on
			// device_last_seen_seconds_ago) carry the device only as a
			// `device_id` label, not the incident's device_id column. Promote
			// it to the target so the UI / notifications show the device
			// instead of falling back to the raw dedupe key ("pipeline:...").
			if out.TargetID == "" {
				if did := strings.TrimSpace(labels["device_id"]); did != "" {
					out.TargetType = "edge"
					out.TargetID = did
				}
			}
		}
	}
	return out
}

// toServiceChannel converts a storage Channel row to the transport DTO.
// EndpointMasked extracts the "endpoint" / "url" / "webhook_url" field from
// ConfigJSON when present and masks anything past 50 characters so secrets
// embedded in the URL aren't echoed back to the UI verbatim.
func toServiceChannel(r *model.Channel) *Channel {
	if r == nil {
		return nil
	}
	out := &Channel{
		ID:        r.ID,
		Name:      r.Name,
		Type:      r.ChannelType,
		Enabled:   r.Enabled,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
	if r.ConfigJSON != "" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(r.ConfigJSON), &cfg); err == nil {
			for _, k := range []string{"endpoint", "url", "webhook_url"} {
				if v, ok := cfg[k]; ok {
					if s, ok := v.(string); ok && s != "" {
						out.EndpointMasked = maskEndpoint(s)
						break
					}
				}
			}
		}
	}
	return out
}

// maskEndpoint trims the endpoint to its first 50 characters and appends an
// ellipsis when truncated, so query-string secrets don't leak through the
// settings UI.
func maskEndpoint(s string) string {
	const maxLen = 50
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// encodeChannelConfig builds a fresh ConfigJSON blob in the same shape
// SeedChannelsFromConfig uses (endpoint + secret_set marker), so env-seeded
// rows and UI-managed rows are read by the notify router uniformly.
func encodeChannelConfig(endpoint, secret string) string {
	cfg := map[string]any{}
	if e := strings.TrimSpace(endpoint); e != "" {
		cfg["endpoint"] = e
	}
	if s := strings.TrimSpace(secret); s != "" {
		cfg["secret"] = s
		cfg["secret_set"] = "true"
	}
	if len(cfg) == 0 {
		return "{}"
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// mergeChannelConfig folds the input endpoint/secret into the existing
// ConfigJSON. Empty secret means "leave as is" so admins can rotate the
// endpoint without re-typing the webhook secret on every save; passing "-"
// explicitly clears the secret.
func mergeChannelConfig(existing, endpoint, secret string) string {
	cfg := map[string]any{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &cfg)
	}
	if e := strings.TrimSpace(endpoint); e != "" {
		cfg["endpoint"] = e
	} else {
		delete(cfg, "endpoint")
	}
	switch strings.TrimSpace(secret) {
	case "":
		// Preserve existing secret untouched.
	case "-":
		delete(cfg, "secret")
		delete(cfg, "secret_set")
	default:
		cfg["secret"] = strings.TrimSpace(secret)
		cfg["secret_set"] = "true"
	}
	if len(cfg) == 0 {
		return "{}"
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func toServiceEvent(r *model.Event) *Event {
	if r == nil {
		return nil
	}
	out := &Event{
		ID:             r.ID,
		IncidentID:     r.IncidentID,
		EventType:      r.EventType,
		StatusAfter:    r.StatusAfter,
		Severity:       r.Severity,
		Title:          r.Title,
		ActorType:      r.ActorType,
		OperatorUserID: r.OperatorUserID,
		Reason:         r.Reason,
		OccurredAt:     r.OccurredAt,
		CreatedAt:      r.CreatedAt,
	}
	if r.Message != nil {
		out.Message = *r.Message
	}
	return out
}

func toServiceRule(r *model.Rule) (*Rule, error) {
	if r == nil {
		return nil, errs.ErrNotFound
	}
	kind := r.Kind
	if kind == "" {
		// Post-Phase-3-final default. metric_threshold no longer exists
		// on disk (the friendly form is UI-only and compiles to
		// metric_raw at save time), so any row with empty kind is the
		// canonical metric_raw shape.
		kind = model.RuleKindMetricRaw
	}
	out := &Rule{
		ID:         r.ID,
		RuleKey:    r.RuleKey,
		Kind:       kind,
		Name:       r.Name,
		SourceType: r.SourceType,
		ScopeType:  r.ScopeType,
		JoinMode:   r.JoinMode,
		Severity:   r.Severity,
		Enabled:    r.Enabled,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
	// Every persisted kind stores an opaque spec in conditions_json
	// (the friendly metric_threshold form was rewritten to metric_raw
	// by buildRuleRow / migration before we got here).
	{
		var spec map[string]any
		if r.ConditionsJSON != "" {
			if err := json.Unmarshal([]byte(r.ConditionsJSON), &spec); err != nil {
				return nil, fmt.Errorf("decode spec: %w", err)
			}
		}
		out.Spec = spec
	}
	if r.LabelsJSON != nil && *r.LabelsJSON != "" {
		var labels map[string]string
		if err := json.Unmarshal([]byte(*r.LabelsJSON), &labels); err == nil {
			out.Labels = labels
		}
	}
	if r.RunbookURL != nil {
		out.RunbookURL = *r.RunbookURL
	}
	if r.NotifyChannelIDsJSON != nil && *r.NotifyChannelIDsJSON != "" {
		var ids []uint64
		if err := json.Unmarshal([]byte(*r.NotifyChannelIDsJSON), &ids); err == nil {
			out.NotifyChannelIDs = ids
		}
	}
	out.NotifyWindowSeconds = r.NotifyWindowSeconds
	out.NotifyMinFires = r.NotifyMinFires
	return out, nil
}

func toBizRuleInput(in RuleInput) bizalert.RuleInput {
	conds := make([]model.RuleCondition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, model.RuleCondition{
			Metric:     c.Metric,
			Operator:   c.Operator,
			Threshold:  c.Threshold,
			Window:     c.Window,
			For:        c.For,
			Aggregator: c.Aggregator,
		})
	}
	return bizalert.RuleInput{
		RuleKey:             in.RuleKey,
		Kind:                in.Kind,
		Name:                in.Name,
		ScopeType:           in.ScopeType,
		JoinMode:            in.JoinMode,
		Severity:            in.Severity,
		Enabled:             in.Enabled,
		Conditions:          conds,
		Spec:                in.Spec,
		Labels:              in.Labels,
		RunbookURL:          in.RunbookURL,
		NotifyChannelIDs:    in.NotifyChannelIDs,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
}

func pageBounds(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return pageSize, (page - 1) * pageSize
}
