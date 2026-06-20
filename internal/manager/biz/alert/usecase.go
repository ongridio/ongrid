package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/notify"
	"github.com/ongridio/ongrid/internal/pkg/prom"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Notifier is the narrow notify surface this package needs. The notify
// package's *Router satisfies it; tests stub it.
type Notifier interface {
	Send(ctx context.Context, msg notify.Message, channels ...string) error
	// SendVia delivers through an explicitly-built sender — used for
	// persisted channels whose Sender is constructed per-row from
	// ChannelType + ConfigJSON (BuildSenderFromChannel), rather than
	// looked up by name. Keeps delivery on the Notifier seam so router
	// gating + test stubs still apply.
	SendVia(ctx context.Context, msg notify.Message, sender notify.Sender) error
}

// NoopHostMetricIngester is the post-Phase-3-final stand-in for the
// deleted HostMetricDecorator. push_host_metrics is still wired (legacy
// edges) but we no longer evaluate inline — every host-metric alert is
// a metric_raw rule the PipelineEvaluator runs on its 30s ticker. The
// frontierbound Wiring contract requires a non-nil MetricIngester; this
// type satisfies it by accepting and dropping the batch so old edges
// keep getting 200s instead of churning on errors.
type NoopHostMetricIngester struct{}

// NewNoopHostMetricIngester builds a no-op ingester. Returned value is
// safe to share across goroutines.
func NewNoopHostMetricIngester() *NoopHostMetricIngester { return &NoopHostMetricIngester{} }

// Push satisfies metric.IngestService. It always returns nil — the
// edge's batch is silently dropped because cloud Prom (push_prom_samples)
// is the canonical metric path now.
func (NoopHostMetricIngester) Push(_ context.Context, _ uint64, _ []tunnel.HostMetricPoint) error {
	return nil
}

// Investigator is the narrow surface RecordFiring uses to kick off the
// proactive AI investigation. The aiops/investigator.Investigator
// satisfies it. Declared here so this package doesn't import aiops/
// (which would invert the dependency direction). Optional — when nil,
// no investigation is dispatched.
type Investigator interface {
	// InvestigateAsync MUST be non-blocking — the firing path can't
	// wait on an LLM round-trip. Implementations dispatch to a worker
	// pool and return immediately, dropping on backpressure.
	InvestigateAsync(incident *model.Incident)
}

type Usecase struct {
	repo  Repo
	clock Clock
	log   *slog.Logger
	// investigator is optional; nil-safe everywhere. main.go injects via
	// SetInvestigator only when LLM is configured.
	investigator Investigator
}

func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, clock: realClock{}, log: log}
}

// SetInvestigator wires the proactive AI investigator. main.go calls
// this after constructing the aiops/investigator. Safe to leave unset:
// RecordFiring nil-checks before dispatch.
func (u *Usecase) SetInvestigator(inv Investigator) {
	u.investigator = inv
}

// createEvent persists an alert event row and increments the
// alert_events_total counter. Centralised here so every CreateEvent call
// site (RecordFiring, transition, SilenceIncident, SystemResolveIncident,
// RecordRepeatSuppressed, FinishDelivery, MaybeNotify inhibited) feeds
// the same counter. The metric powers the canonical "通知投递失败" /
// "告警风暴" metric_raw rules (collapse) — keep in sync
// if new event_type values get added. ruleKey may be empty for
// system-level events (e.g. delivery row notifications without a parent
// rule context).
func (u *Usecase) createEvent(ctx context.Context, ev *model.Event, ruleKey string) error {
	err := u.repo.CreateEvent(ctx, ev)
	if err == nil {
		// Emit the metric on success only — a failed write would inflate
		// the counter past the actual row count and confuse rules that
		// alert on event-write outcome.
		prom.IncAlertEvent(ev.EventType, ev.Severity, ruleKey)
	}
	return err
}

func (u *Usecase) ListIncidents(ctx context.Context, f IncidentFilter) ([]*model.Incident, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.ListIncidents(ctx, f)
}

// CountIncidents proxies to the repo so callers (sidebar badge / Home
// status row / Alerts page header) get the *real* total — pagination
// must not lower this number.
func (u *Usecase) CountIncidents(ctx context.Context, f IncidentFilter) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	return u.repo.CountIncidents(ctx, f)
}

func (u *Usecase) GetIncident(ctx context.Context, id uint64) (*model.Incident, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id == 0 {
		return nil, fmt.Errorf("%w: incident id must be positive", errs.ErrInvalid)
	}
	return u.repo.GetIncidentByID(ctx, id)
}

func (u *Usecase) AckIncident(ctx context.Context, id, operatorUserID uint64, note string) error {
	return u.transition(ctx, id, model.EventTypeAcknowledged, model.IncidentStatusAcknowledged, operatorUserID, note)
}

func (u *Usecase) ResolveIncident(ctx context.Context, id, operatorUserID uint64, note string) error {
	return u.transition(ctx, id, model.EventTypeResolved, model.IncidentStatusResolved, operatorUserID, note)
}

func (u *Usecase) SilenceIncident(ctx context.Context, id, operatorUserID uint64, until, reason string) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	incident, err := u.GetIncident(ctx, id)
	if err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("%w: silence reason required", errs.ErrInvalid)
	}
	endsAt, err := parseSilenceUntil(u.clock.Now(), until)
	if err != nil {
		return fmt.Errorf("%w: silence_until: %s", errs.ErrInvalid, err)
	}
	now := u.clock.Now()
	if !endsAt.After(now) {
		return fmt.Errorf("%w: silence_until must be in the future", errs.ErrInvalid)
	}
	if err := u.repo.UpdateIncidentStatus(ctx, id, model.IncidentStatusSilenced, &operatorUserID, now); err != nil {
		return err
	}
	matchers := `[]`
	if incident.DeviceID != nil {
		matchers = fmt.Sprintf(`[{"field":"device_id","operator":"=","value":"%d"}]`, *incident.DeviceID)
	}
	reasonCopy := reason
	if err := u.repo.CreateSilence(ctx, &model.Silence{
		Name:         incident.Title,
		ScopeType:    incident.ScopeType,
		Status:       model.SilenceStatusActive,
		MatchersJSON: matchers,
		Reason:       &reasonCopy,
		CreatedBy:    &operatorUserID,
		StartsAt:     now,
		EndsAt:       endsAt,
	}); err != nil {
		return fmt.Errorf("create silence: %w", err)
	}
	return u.createEvent(ctx, &model.Event{
		IncidentID:  id,
		EventType:   model.EventTypeSilenced,
		StatusAfter: model.IncidentStatusSilenced,
		Severity:    incident.Severity,
		Title:       incident.Title,
		Message:     &reasonCopy,
		ActorType:   model.ActorTypeUser,
		ActorID:     &operatorUserID,
		OccurredAt:  now,
	}, incident.Rule)
}

func (u *Usecase) ListEvents(ctx context.Context, incidentID uint64, limit int) ([]*model.Event, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if incidentID == 0 {
		return nil, fmt.Errorf("%w: incident id must be positive", errs.ErrInvalid)
	}
	return u.repo.ListEventsByIncident(ctx, incidentID, limit)
}

func (u *Usecase) transition(ctx context.Context, id uint64, eventType string, status string, operatorUserID uint64, note string) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	incident, err := u.GetIncident(ctx, id)
	if err != nil {
		return err
	}
	note = strings.TrimSpace(note)
	now := u.clock.Now()
	if err := u.repo.UpdateIncidentStatus(ctx, id, status, &operatorUserID, now); err != nil {
		return err
	}
	evt := &model.Event{
		IncidentID:  id,
		EventType:   eventType,
		StatusAfter: status,
		Severity:    incident.Severity,
		Title:       incident.Title,
		ActorType:   model.ActorTypeUser,
		ActorID:     &operatorUserID,
		OccurredAt:  now,
	}
	if note != "" {
		noteCopy := note
		evt.Message = &noteCopy
	}
	return u.createEvent(ctx, evt, incident.Rule)
}

// FiringInput is the parameter object for RecordFiring. Fields below the
// dotted line are persisted into the incident row when the firing creates a
// fresh incident; on a re-firing they are ignored (the original incident's
// title / labels stay authoritative until ack/resolve).
type FiringInput struct {
	ScopeType  string
	Scope      string
	Rule       string
	RuleName   string
	Severity   string
	DeviceID   *uint64
	OccurredAt time.Time

	// DedupeKey overrides the default scope/edge/rule dedupe construction.
	// Pipeline evaluators use this to encode per-instance dedupe keys
	// (e.g. "pipeline:scrape_down:host:9100:node-exporter").
	DedupeKey string

	// SourceType records who emitted the firing. Empty defaults to
	// model.RuleSourceBuiltin.
	SourceType string

	// ........................

	Title       string
	Summary     string
	Description string
	Value       *float64
	Threshold   *float64
	Labels      map[string]string
	Annotations map[string]string
	RunbookURL  string
}

// FiringResult tells the caller what the firing did to the underlying incident
// and whether the silence/cooldown gate would suppress a notification.
type FiringResult struct {
	Incident *model.Incident

	// IsNew is true when the firing created a fresh incident row. False on
	// every subsequent firing of the same dedupe_key.
	IsNew bool
	// IsReopen is true when the firing transitioned a resolved incident back
	// to open. Mutually exclusive with IsNew.
	IsReopen bool

	// Silenced is true when an active silence row matches the firing scope/
	// edge/rule, OR when the incident itself is in silenced status with an
	// unexpired silenced_until. Caller skips the notify step.
	Silenced bool
	// SilencedBy is the matching silence id, when Silenced is true and the
	// match comes from a silence row (vs. incident-level silenced state).
	SilencedBy *uint64
}

// RecordFiring upserts an alert incident keyed by its dedupe_key and writes
// the firing event. It does not perform notification; the caller decides
// whether to notify based on Silenced / cooldown.
//
// Concurrency: a race between two RecordFiring calls for the same dedupe_key
// can produce a duplicate insert error on the second goroutine. We treat that
// as a retry: re-fetch and bump the existing row.
func (u *Usecase) RecordFiring(ctx context.Context, in FiringInput) (*FiringResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if err := validateFiring(in); err != nil {
		return nil, err
	}

	occurredAt := in.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = u.clock.Now()
	}
	dedupeKey := in.DedupeKey
	if dedupeKey == "" {
		dedupeKey = buildDedupeKey(in.ScopeType, in.DeviceID, in.Rule)
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = model.RuleSourceBuiltin
	}

	existing, err := u.repo.GetIncidentByDedupeKey(ctx, dedupeKey)
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, fmt.Errorf("get incident by dedupe: %w", err)
	}

	var (
		incident *model.Incident
		isNew    bool
		isReopen bool
	)
	if existing == nil {
		// Create.
		labelsJSON := encodeLabels(in.Labels)
		annJSON := encodeLabels(in.Annotations)
		title := in.Title
		if title == "" {
			title = defaultTitle(in)
		}
		newInc := &model.Incident{
			DeviceID:        in.DeviceID,
			Title:           title,
			Scope:           in.Scope,
			ScopeType:       in.ScopeType,
			Rule:            in.Rule,
			RuleName:        firstNonEmpty(in.RuleName, in.Rule),
			Severity:        in.Severity,
			Status:          model.IncidentStatusOpen,
			Summary:         in.Summary,
			Description:     in.Description,
			DedupeKey:       dedupeKey,
			Value:           in.Value,
			Threshold:       in.Threshold,
			LabelsJSON:      labelsJSON,
			AnnotationsJSON: annJSON,
			RunbookURL:      in.RunbookURL,
			EventCount:      1,
			FirstFiredAt:    occurredAt,
			LastFiredAt:     occurredAt,
			SourceType:      sourceType,
		}
		if err := u.repo.CreateIncident(ctx, newInc); err != nil {
			// If a concurrent goroutine won the race, fall through to bump.
			if again, getErr := u.repo.GetIncidentByDedupeKey(ctx, dedupeKey); getErr == nil {
				existing = again
			} else {
				return nil, fmt.Errorf("create incident: %w", err)
			}
		} else {
			incident = newInc
			isNew = true
		}
	}
	if incident == nil {
		// Existing incident path (cold or race-recovery).
		switch existing.Status {
		case model.IncidentStatusResolved:
			if err := u.repo.ReopenIncident(ctx, existing.ID, occurredAt); err != nil {
				return nil, fmt.Errorf("reopen incident: %w", err)
			}
			existing.Status = model.IncidentStatusOpen
			existing.SilencedUntil = nil
			existing.ResolvedAt = nil
			existing.ResolvedBy = nil
			isReopen = true
		default:
			if err := u.repo.BumpIncidentFiring(ctx, existing.ID, occurredAt); err != nil {
				return nil, fmt.Errorf("bump incident: %w", err)
			}
		}
		// Repo update authoritative — do not predict in-memory counters.
		incident = existing
	}

	// Always write a firing event so the timeline reflects every sample that
	// crossed threshold. Silence + cooldown gate notification, not events.
	eventType := model.EventTypeFiring
	if isReopen {
		eventType = model.EventTypeReopened
	}
	snapshotJSON := encodeFiringSnapshot(in)
	ev := &model.Event{
		IncidentID:   incident.ID,
		EventType:    eventType,
		StatusAfter:  incident.Status,
		Severity:     incident.Severity,
		Title:        incident.Title,
		ActorType:    model.ActorTypeSystem,
		SnapshotJSON: snapshotJSON,
		OccurredAt:   occurredAt,
	}
	if err := u.createEvent(ctx, ev, incident.Rule); err != nil {
		// Event write is best-effort relative to the incident row — don't
		// surface as fatal; log and proceed.
		u.log.Warn("alert: write firing event failed",
			slog.Uint64("incident_id", incident.ID),
			slog.Any("err", err),
		)
	}

	// Silence matching: incident status takes precedence; otherwise consult
	// active silence rows.
	silenced, silencedBy := u.matchSilence(ctx, incident, occurredAt)

	// Proactive AI investigation (P2). Only fires on a brand-new
	// incident — re-firings of an existing dedupe_key already have a
	// diagnosis, and reopens of resolved incidents would only churn
	// the LLM bill on what is usually a flap. The investigator
	// dispatches to a worker pool and returns immediately, so this
	// stays off the firing-path critical timing.
	if isNew && u.investigator != nil {
		u.investigator.InvestigateAsync(incident)
	}

	return &FiringResult{
		Incident:   incident,
		IsNew:      isNew,
		IsReopen:   isReopen,
		Silenced:   silenced,
		SilencedBy: silencedBy,
	}, nil
}

// RecordRepeatSuppressed writes a repeat_suppressed event for an existing
// incident. Used by the firing-path cooldown gate so the timeline reflects
// every threshold-crossing sample even when the notify is throttled.
func (u *Usecase) RecordRepeatSuppressed(ctx context.Context, incidentID uint64, occurredAt time.Time, reason string) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if occurredAt.IsZero() {
		occurredAt = u.clock.Now()
	}
	return u.createEvent(ctx, &model.Event{
		IncidentID:  incidentID,
		EventType:   model.EventTypeRepeatSuppressed,
		StatusAfter: model.IncidentStatusOpen,
		ActorType:   model.ActorTypeSystem,
		Reason:      reason,
		OccurredAt:  occurredAt,
	}, "")
}

// MarkNotified updates incident.last_notified_at — the firing-path cooldown
// gate reads this column on the next sample.
func (u *Usecase) MarkNotified(ctx context.Context, incidentID uint64, at time.Time) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if at.IsZero() {
		at = u.clock.Now()
	}
	return u.repo.MarkIncidentNotified(ctx, incidentID, at)
}

// RecordDelivery captures a single channel attempt against an incident. It
// inserts a notification_deliveries row in pending state, returns the row id,
// and lets the caller transition it to success/failed via FinishDelivery.
func (u *Usecase) RecordDelivery(ctx context.Context, incidentID, channelID uint64) (uint64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	d := &model.Delivery{
		IncidentID:   &incidentID,
		ChannelID:    channelID,
		Status:       model.DeliveryStatusPending,
		AttemptCount: 1,
	}
	if err := u.repo.CreateDelivery(ctx, d); err != nil {
		return 0, err
	}
	return d.ID, nil
}

// FinishDelivery transitions a previously-pending delivery to success or
// failed and writes the corresponding incident event so the timeline shows
// per-channel notification outcome.
func (u *Usecase) FinishDelivery(ctx context.Context, deliveryID, incidentID uint64, channelName string, sendErr error, occurredAt time.Time) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if occurredAt.IsZero() {
		occurredAt = u.clock.Now()
	}
	finishedAt := occurredAt
	sentAt := &occurredAt
	status := model.DeliveryStatusSuccess
	var errMsg *string
	eventType := model.EventTypeNotificationSent
	if sendErr != nil {
		status = model.DeliveryStatusFailed
		s := sendErr.Error()
		errMsg = &s
		eventType = model.EventTypeNotificationFailed
	}
	if err := u.repo.UpdateDeliveryStatus(ctx, deliveryID, status, 1, nil, nil, errMsg, sentAt, &finishedAt); err != nil {
		return err
	}
	reason := channelName
	if sendErr != nil {
		reason = fmt.Sprintf("%s: %s", channelName, sendErr.Error())
	}
	// FinishDelivery sits one layer below the rule context (the delivery
	// row carries the incident id; the incident carries the rule). For
	// the alert_events_total counter we leave rule="" — operators who
	// want rule-keyed delivery outcomes can still join the events table
	// against incidents in BI, but the simple metric stays simple.
	return u.createEvent(ctx, &model.Event{
		IncidentID:  incidentID,
		EventType:   eventType,
		StatusAfter: model.IncidentStatusOpen,
		ActorType:   model.ActorTypeSystem,
		Reason:      reason,
		OccurredAt:  occurredAt,
	}, "")
}

// RuleInput is the parameter object for CreateRule / UpdateRule. Validation
// happens in the usecase so HTTP and any other future entry point share one
// definition of "well-formed rule".
type RuleInput struct {
	RuleKey   string
	Kind      string
	Name      string
	ScopeType string
	JoinMode  string
	Severity  string
	Enabled   bool
	// Conditions is the spec for kind=metric_threshold (one row per
	// closed-set host metric condition). Other kinds use Spec instead.
	Conditions []model.RuleCondition
	// Spec is the kind-specific opaque payload that gets serialised into
	// Rule.ConditionsJSON for non-metric_threshold kinds. Validation per
	// kind happens in buildRuleRow.
	Spec       map[string]any
	Labels     map[string]string
	RunbookURL string
	// NotifyChannelIDs pins this rule's incidents to specific channels.
	// Nil / empty → router uses its default filter logic. See
	// model.Rule.NotifyChannelIDsJSON for storage shape.
	NotifyChannelIDs []uint64
	// NotifyWindowSeconds + NotifyMinFires implement the 「发送策略」
	// (send-policy) dampening gate. Both zero = disabled (default,
	// every firing notifies subject to cooldown). Both > 0 = enabled
	// (skip notify until the rule has fired ≥ NotifyMinFires times in
	// the trailing window). Mixed values are rejected.
	NotifyWindowSeconds int
	NotifyMinFires      int
}

// ListRules returns the rule set, optionally filtered by scope.
func (u *Usecase) ListRules(ctx context.Context, scopeType string) ([]*model.Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.ListRules(ctx, scopeType)
}

// GetRule reads a rule by primary key.
func (u *Usecase) GetRule(ctx context.Context, id uint64) (*model.Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	return u.repo.GetRuleByID(ctx, id)
}

// CreateRule validates and inserts a new rule, returning the persisted row.
func (u *Usecase) CreateRule(ctx context.Context, in RuleInput, createdBy *uint64) (*model.Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := buildRuleRow(in, true)
	if err != nil {
		return nil, err
	}
	if existing, err := u.repo.GetRuleByKey(ctx, row.RuleKey); err == nil && existing != nil {
		return nil, fmt.Errorf("%w: rule_key %q already in use", errs.ErrConflict, row.RuleKey)
	} else if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return nil, err
	}
	row.CreatedBy = createdBy
	if err := u.repo.CreateRule(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

// UpdateRule replaces editable fields on an existing rule.
func (u *Usecase) UpdateRule(ctx context.Context, id uint64, in RuleInput) (*model.Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	existing, err := u.repo.GetRuleByID(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err := buildRuleRow(in, false)
	if err != nil {
		return nil, err
	}
	row.ID = existing.ID
	row.RuleKey = existing.RuleKey
	row.SourceType = existing.SourceType
	if err := u.repo.UpdateRule(ctx, id, row); err != nil {
		return nil, err
	}
	return u.repo.GetRuleByID(ctx, id)
}

// SetRuleEnabled flips the enabled bit. Returns the updated row.
func (u *Usecase) SetRuleEnabled(ctx context.Context, id uint64, enabled bool) (*model.Rule, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if id == 0 {
		return nil, fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	if err := u.repo.UpdateRuleEnabled(ctx, id, enabled); err != nil {
		return nil, err
	}
	return u.repo.GetRuleByID(ctx, id)
}

// DeleteRule removes a custom rule. Built-in seed rules are managed by
// boot-time seeding and must be disabled instead of deleted.
func (u *Usecase) DeleteRule(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	if id == 0 {
		return fmt.Errorf("%w: rule id required", errs.ErrInvalid)
	}
	existing, err := u.repo.GetRuleByID(ctx, id)
	if err != nil {
		return err
	}
	if existing.SourceType == model.RuleSourceBuiltin {
		return fmt.Errorf("%w: built-in rules cannot be deleted", errs.ErrForbidden)
	}
	return u.repo.DeleteRule(ctx, id)
}

func buildRuleRow(in RuleInput, requireKey bool) (*model.Rule, error) {
	key := strings.TrimSpace(in.RuleKey)
	if requireKey && key == "" {
		return nil, fmt.Errorf("%w: rule_key required", errs.ErrInvalid)
	}
	if requireKey && !validRuleKey(key) {
		return nil, fmt.Errorf("%w: rule_key %q must be lower_snake [a-z0-9_]", errs.ErrInvalid, key)
	}
	kind := model.NormalizeKind(in.Kind)
	if !model.IsKnownKind(kind) {
		return nil, fmt.Errorf("%w: kind %q unsupported", errs.ErrInvalid, kind)
	}
	scope := in.ScopeType
	if scope == "" {
		scope = defaultScopeForKind(kind)
	}
	if !scopeAllowedForKind(scope, kind) {
		return nil, fmt.Errorf("%w: scope_type %q not allowed for kind %q (allowed: %v)",
			errs.ErrInvalid, scope, kind, allowedScopesForKind(kind))
	}
	join := in.JoinMode
	if join == "" {
		join = model.RuleJoinModeAll
	}
	if join != model.RuleJoinModeAll && join != model.RuleJoinModeAny {
		return nil, fmt.Errorf("%w: join_mode %q unsupported", errs.ErrInvalid, join)
	}
	if in.Severity == "" {
		return nil, fmt.Errorf("%w: severity required", errs.ErrInvalid)
	}
	condJSON, err := buildConditionsJSON(kind, in)
	if err != nil {
		return nil, err
	}
	// metric_threshold is a UI-only entry form. After buildConditionsJSON
	// has compiled the closed-set conditions into a single PromQL
	// expression, downgrade the storage kind to metric_raw so the
	// evaluator (and every other reader) sees one canonical shape.
	storageKind := kind
	if kind == model.RuleKindMetricThreshold {
		storageKind = model.RuleKindMetricRaw
	}
	// 发送策略 (send-policy / dampening) validation: both zero = disabled
	// (default); both > 0 = enabled; mixed = reject. Caps mirror the UI
	// dropdowns to keep operators inside sane bounds — runaway windows
	// produce user-hostile incidents that never notify.
	if in.NotifyWindowSeconds < 0 || in.NotifyMinFires < 0 {
		return nil, fmt.Errorf("%w: send policy fields must be non-negative", errs.ErrInvalid)
	}
	if (in.NotifyWindowSeconds > 0) != (in.NotifyMinFires > 0) {
		return nil, fmt.Errorf("%w: send policy requires both window and threshold (or both zero)", errs.ErrInvalid)
	}
	if in.NotifyWindowSeconds > 0 {
		// Window: 1 minute to 24 hours.
		if in.NotifyWindowSeconds < 60 || in.NotifyWindowSeconds > 1440*60 {
			return nil, fmt.Errorf("%w: send policy window must be between 1 and 1440 minutes", errs.ErrInvalid)
		}
		if in.NotifyMinFires < 1 || in.NotifyMinFires > 100 {
			return nil, fmt.Errorf("%w: send policy threshold must be between 1 and 100", errs.ErrInvalid)
		}
	}

	row := &model.Rule{
		RuleKey:             key,
		Kind:                storageKind,
		Name:                strings.TrimSpace(in.Name),
		ScopeType:           scope,
		JoinMode:            join,
		Severity:            in.Severity,
		Enabled:             in.Enabled,
		ConditionsJSON:      condJSON,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
	if len(in.Labels) > 0 {
		labelsBlob, _ := json.Marshal(in.Labels)
		labels := string(labelsBlob)
		row.LabelsJSON = &labels
	}
	if in.RunbookURL != "" {
		runbook := in.RunbookURL
		row.RunbookURL = &runbook
	}
	if len(in.NotifyChannelIDs) > 0 {
		// De-dup defensively in case the UI sent the same id twice.
		seen := make(map[uint64]struct{}, len(in.NotifyChannelIDs))
		uniq := make([]uint64, 0, len(in.NotifyChannelIDs))
		for _, id := range in.NotifyChannelIDs {
			if id == 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			uniq = append(uniq, id)
		}
		if len(uniq) > 0 {
			blob, err := json.Marshal(uniq)
			if err != nil {
				return nil, fmt.Errorf("encode notify_channel_ids: %w", err)
			}
			s := string(blob)
			row.NotifyChannelIDsJSON = &s
		}
	}
	return row, nil
}

// buildConditionsJSON validates the kind-specific spec inside RuleInput
// and serialises it into the legacy Rule.ConditionsJSON column. Returns
// the JSON string ready to write.
//
// metric_threshold is a UI-only entry form: the friendly closed-set
// HostMetric + operator + threshold list gets COMPILED here into a
// single PromQL expression and stored as metric_raw. From the DB's
// perspective every saved rule is metric_raw — there is one evaluator,
// one storage shape. The caller that hits this path with
// kind=metric_threshold gets the compiled expression back; their
// downstream Rule row should bear kind=metric_raw (buildRuleRow handles
// that overwrite).
func buildConditionsJSON(kind string, in RuleInput) (string, error) {
	switch kind {
	case model.RuleKindMetricThreshold:
		if len(in.Conditions) == 0 {
			return "", fmt.Errorf("%w: at least one condition required", errs.ErrInvalid)
		}
		for _, c := range in.Conditions {
			if c.Metric == "" || c.Operator == "" {
				return "", fmt.Errorf("%w: condition missing metric or operator", errs.ErrInvalid)
			}
			if !validHostOperator(c.Operator) {
				return "", fmt.Errorf("%w: condition operator %q unsupported", errs.ErrInvalid, c.Operator)
			}
			if _, ok := metricExprFor(c.Metric); !ok {
				return "", fmt.Errorf("%w: condition metric %q not in host closed-set", errs.ErrInvalid, c.Metric)
			}
		}
		expr, err := compileMetricThresholdExpr(in.Conditions, in.JoinMode)
		if err != nil {
			return "", err
		}
		blob, err := json.Marshal(map[string]any{"expr": expr})
		if err != nil {
			return "", fmt.Errorf("marshal compiled metric_threshold spec: %w", err)
		}
		return string(blob), nil
	case model.RuleKindMetricRaw:
		// Phase-3 collapse: expr alone is the predicate. We don't
		// extract operator/threshold/for_seconds — PromQL's own
		// comparison operators (`up == 0`, `cpu_pct > 90`) are the
		// canonical predicate, validated by Prom at query time. Any
		// extra fields the UI happens to send are ignored.
		expr, _ := stringFromSpec(in.Spec, "expr")
		if strings.TrimSpace(expr) == "" {
			return "", fmt.Errorf("%w: metric_raw.expr required", errs.ErrInvalid)
		}
		blob, err := json.Marshal(map[string]any{
			"expr": expr,
		})
		if err != nil {
			return "", fmt.Errorf("marshal metric_raw spec: %w", err)
		}
		return string(blob), nil
	case model.RuleKindMetricAnomaly:
		metric, _ := stringFromSpec(in.Spec, "metric")
		if strings.TrimSpace(metric) == "" {
			return "", fmt.Errorf("%w: metric_anomaly.metric required", errs.ErrInvalid)
		}
		method, _ := stringFromSpec(in.Spec, "method")
		if method == "" {
			method = "zscore"
		}
		if method != "zscore" && method != "mad" {
			return "", fmt.Errorf("%w: metric_anomaly.method must be zscore or mad", errs.ErrInvalid)
		}
		baseline, _ := stringFromSpec(in.Spec, "baseline_window")
		if baseline == "" {
			baseline = "1h"
		}
		step, _ := stringFromSpec(in.Spec, "baseline_step")
		if step == "" {
			step = "5m"
		}
		dev, _ := numericFromSpec(in.Spec, "deviation")
		if dev <= 0 {
			dev = 3
		}
		forSec, _ := numericFromSpec(in.Spec, "for_seconds")
		selector, _ := stringFromSpec(in.Spec, "selector")
		blob, err := json.Marshal(map[string]any{
			"metric":          metric,
			"selector":        selector,
			"method":          method,
			"baseline_window": baseline,
			"baseline_step":   step,
			"deviation":       dev,
			"for_seconds":     int(forSec),
		})
		if err != nil {
			return "", fmt.Errorf("marshal metric_anomaly spec: %w", err)
		}
		return string(blob), nil
	case model.RuleKindMetricForecast:
		metric, _ := stringFromSpec(in.Spec, "metric")
		if strings.TrimSpace(metric) == "" {
			return "", fmt.Errorf("%w: metric_forecast.metric required", errs.ErrInvalid)
		}
		fitWindow, _ := stringFromSpec(in.Spec, "fit_window")
		if fitWindow == "" {
			fitWindow = "1h"
		}
		predict, _ := numericFromSpec(in.Spec, "predict_seconds")
		if predict <= 0 {
			return "", fmt.Errorf("%w: metric_forecast.predict_seconds must be > 0", errs.ErrInvalid)
		}
		op, _ := stringFromSpec(in.Spec, "operator")
		if !validHostOperator(op) {
			return "", fmt.Errorf("%w: metric_forecast.operator %q unsupported", errs.ErrInvalid, op)
		}
		thr, _ := numericFromSpec(in.Spec, "threshold")
		forSec, _ := numericFromSpec(in.Spec, "for_seconds")
		selector, _ := stringFromSpec(in.Spec, "selector")
		blob, err := json.Marshal(map[string]any{
			"metric":          metric,
			"selector":        selector,
			"fit_window":      fitWindow,
			"predict_seconds": int(predict),
			"operator":        op,
			"threshold":       thr,
			"for_seconds":     int(forSec),
		})
		if err != nil {
			return "", fmt.Errorf("marshal metric_forecast spec: %w", err)
		}
		return string(blob), nil
	case model.RuleKindMetricBurnRate:
		sli, _ := stringFromSpec(in.Spec, "sli")
		sli = normalizeBurnRateSLIExpression(sli)
		if strings.TrimSpace(sli) == "" {
			return "", fmt.Errorf("%w: metric_burn_rate.sli required", errs.ErrInvalid)
		}
		if !burnRateSLIUsesWindow(sli) {
			return "", fmt.Errorf("%w: metric_burn_rate.sli must use $window or a PromQL range selector", errs.ErrInvalid)
		}
		slo, _ := numericFromSpec(in.Spec, "slo")
		slo = normalizeBurnRateSLOPercent(slo)
		if slo <= 0 || slo >= 100 {
			return "", fmt.Errorf("%w: metric_burn_rate.slo must be in (0, 100)", errs.ErrInvalid)
		}
		burnsRaw, _ := burnsFromSpec(in.Spec, "burns")
		if len(burnsRaw) == 0 {
			return "", fmt.Errorf("%w: metric_burn_rate.burns must have at least one window", errs.ErrInvalid)
		}
		blob, err := json.Marshal(map[string]any{
			"sli":   sli,
			"slo":   slo,
			"burns": burnsRaw,
		})
		if err != nil {
			return "", fmt.Errorf("marshal metric_burn_rate spec: %w", err)
		}
		return string(blob), nil
	case model.RuleKindLogMatch, model.RuleKindLogVolume,
		model.RuleKindTraceLatency, model.RuleKindTraceErrorRate:
		// Persist whatever the caller passed in verbatim so the UI can
		// round-trip a draft. The engine silently skips these kinds (see
		// CachedRulesProvider.Refresh).
		blob, err := json.Marshal(in.Spec)
		if err != nil {
			return "", fmt.Errorf("marshal placeholder spec: %w", err)
		}
		return string(blob), nil
	}
	return "", fmt.Errorf("%w: kind %q has no condition builder", errs.ErrInvalid, kind)
}

// allowedScopesForKind enumerates which scopes make semantic sense for
// each kind. The first element is the default. Kinds with a single entry
// are scope-locked: the API rejects any other scope, the UI renders a
// readonly badge. Kinds with multiple entries let the user choose.
//
//   - metric_threshold : per-host evaluator → host only
//   - metric_burn_rate / trace_* : fleet/service aggregate → global only
//   - metric_anomaly / metric_forecast / metric_raw / log_match / log_volume :
//     PromQL/LogQL controls aggregation, user picks host vs global
func allowedScopesForKind(kind string) []string {
	switch kind {
	case model.RuleKindMetricThreshold:
		return []string{model.RuleScopeHost}
	case model.RuleKindMetricBurnRate, model.RuleKindTraceLatency, model.RuleKindTraceErrorRate:
		return []string{model.RuleScopeGlobal}
	case model.RuleKindMetricRaw:
		return []string{model.RuleScopeGlobal, model.RuleScopeHost}
	case model.RuleKindMetricAnomaly, model.RuleKindMetricForecast,
		model.RuleKindLogMatch, model.RuleKindLogVolume:
		return []string{model.RuleScopeHost, model.RuleScopeGlobal}
	default:
		return []string{model.RuleScopeGlobal, model.RuleScopeHost, model.RuleScopeMonitoringPipeline}
	}
}

func defaultScopeForKind(kind string) string {
	allowed := allowedScopesForKind(kind)
	if len(allowed) == 0 {
		return model.RuleScopeGlobal
	}
	return allowed[0]
}

func scopeAllowedForKind(scope, kind string) bool {
	for _, s := range allowedScopesForKind(kind) {
		if s == scope {
			return true
		}
	}
	return false
}

func numericFromSpec(spec map[string]any, key string) (float64, bool) {
	if spec == nil {
		return 0, false
	}
	v, ok := spec[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		var n float64
		if _, err := fmt.Sscanf(x, "%f", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// burnsFromSpec extracts the "burns" array from a metric_burn_rate spec.
// Each entry must look like {"window": "1h", "multiplier": 14.4}. The
// caller has already validated the kind, so unknown shapes return an
// empty slice — buildConditionsJSON then rejects with the canonical
// "at least one window" error.
func burnsFromSpec(spec map[string]any, key string) ([]map[string]any, bool) {
	if spec == nil {
		return nil, false
	}
	raw, ok := spec[key]
	if !ok {
		return nil, false
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		win, _ := m["window"].(string)
		if win == "" {
			continue
		}
		mult, _ := numericFromSpec(m, "multiplier")
		if mult <= 0 {
			continue
		}
		out = append(out, map[string]any{"window": win, "multiplier": mult})
	}
	return out, len(out) > 0
}

func stringFromSpec(spec map[string]any, key string) (string, bool) {
	if spec == nil {
		return "", false
	}
	v, ok := spec[key]
	if !ok {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	return "", false
}

// compileMetricThresholdExpr renders a metric_threshold condition list
// into a single PromQL predicate suitable for the metric_raw evaluator.
// Each condition becomes `(<metricExprFor(metric)>) <op> <thr>`; the
// list is joined with `and on(device_id)` (join_mode=all) or `or`
// (join_mode=any). Single-condition rules emit just the comparison so
// the resulting expression stays terse.
//
// Caller has already validated that every condition's metric resolves
// via metricExprFor and that each operator is in the host closed set,
// so no error path here.
func compileMetricThresholdExpr(conds []model.RuleCondition, joinMode string) (string, error) {
	if len(conds) == 0 {
		return "", fmt.Errorf("%w: at least one condition required", errs.ErrInvalid)
	}
	parts := make([]string, 0, len(conds))
	for _, c := range conds {
		base, ok := metricExprFor(c.Metric)
		if !ok {
			return "", fmt.Errorf("%w: condition metric %q not in host closed-set", errs.ErrInvalid, c.Metric)
		}
		parts = append(parts, fmt.Sprintf("(%s) %s %g", base, c.Operator, c.Threshold))
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	join := joinMode
	if join == "" {
		join = model.RuleJoinModeAll
	}
	switch join {
	case model.RuleJoinModeAny:
		return strings.Join(parts, " or "), nil
	default:
		return strings.Join(parts, " and on(device_id) "), nil
	}
}

func validRuleKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// SystemResolveIncident resolves an incident keyed by its dedupe_key when
// the underlying condition recovers. Used by the pipeline evaluator: edge
// heartbeat resumes -> resolve "host:<id>:edge_offline"; up==1 returns ->
// resolve "pipeline:scrape_down:<inst>:<job>". Returns (false, nil) when
// no active incident matches (recovery without prior firing).
func (u *Usecase) SystemResolveIncident(ctx context.Context, dedupeKey, reason string, occurredAt time.Time) (bool, error) {
	if u.repo == nil {
		return false, errs.ErrNotWiredYet
	}
	if dedupeKey == "" {
		return false, fmt.Errorf("%w: dedupe_key required", errs.ErrInvalid)
	}
	if occurredAt.IsZero() {
		occurredAt = u.clock.Now()
	}
	incident, err := u.repo.GetIncidentByDedupeKey(ctx, dedupeKey)
	if errors.Is(err, errs.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if incident.Status == model.IncidentStatusResolved {
		return false, nil
	}
	if err := u.repo.UpdateIncidentStatus(ctx, incident.ID, model.IncidentStatusResolved, nil, occurredAt); err != nil {
		return false, fmt.Errorf("update incident status: %w", err)
	}
	reasonCopy := reason
	_ = u.createEvent(ctx, &model.Event{
		IncidentID:  incident.ID,
		EventType:   model.EventTypeResolved,
		StatusAfter: model.IncidentStatusResolved,
		Severity:    incident.Severity,
		Title:       incident.Title,
		ActorType:   model.ActorTypeSystem,
		Reason:      reasonCopy,
		OccurredAt:  occurredAt,
	}, incident.Rule)
	return true, nil
}

// NotifyOpts bundles the notify dependencies the firing-path callers share.
// PipelineEvaluator constructs one and passes it to MaybeNotify so the
// cooldown / silence / delivery-row / routing / inhibition logic stays
// in a single place. (Pre-Phase-3-final, the deleted HostMetricDecorator
// shared this helper too.)
//
// Resolver is the preferred channel selector. When nil, MaybeNotify falls
// back to DefaultChannels (legacy fan-out — kept for tests and migrations).
// Inhibitor is optional; when nil, no inhibition is applied.
type NotifyOpts struct {
	Notifier        Notifier
	Resolver        ChannelResolver
	DefaultChannels []string
	Cooldown        time.Duration
	Inhibitor       Inhibitor
}

// MaybeNotify gates the notify step on silence + cooldown for a fired
// incident, dispatches to each enabled default channel, and persists one
// notification_deliveries row per attempt. Failures are logged but never
// propagated — alert-pipeline outages must not back-pressure the firing path.
func (u *Usecase) MaybeNotify(ctx context.Context, res *FiringResult, msg notify.Message, opts NotifyOpts) {
	if u.repo == nil || res == nil || res.Incident == nil {
		return
	}
	if opts.Notifier == nil {
		return
	}
	if res.Silenced {
		return
	}
	occurredAt := msg.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = u.clock.Now()
	}
	if !u.CooldownExceeded(res.Incident, opts.Cooldown, occurredAt) {
		if err := u.RecordRepeatSuppressed(ctx, res.Incident.ID, occurredAt, "cooldown"); err != nil {
			u.log.Warn("alert: record repeat_suppressed failed", slog.Any("err", err))
		}
		return
	}

	// 发送策略 (send-policy) dampening: if the rule has both window and
	// threshold set, only notify once we have ≥ threshold firing events
	// for this rule_key within the trailing window. The firing event row
	// itself was already written by RecordFiring (caller-side); we count
	// it here. Below threshold → record a repeat_suppressed event and
	// skip notify; the next firing that pushes the count over threshold
	// releases the gate.
	if rule := u.lookupRuleForIncident(ctx, res.Incident); rule != nil &&
		rule.NotifyWindowSeconds > 0 && rule.NotifyMinFires > 0 {
		since := occurredAt.Add(-time.Duration(rule.NotifyWindowSeconds) * time.Second)
		count, err := u.repo.CountEventsByType(ctx, model.EventTypeFiring, since, res.Incident.Rule, "")
		if err != nil {
			u.log.Warn("alert: count firing events for dampening failed",
				slog.String("rule", res.Incident.Rule),
				slog.Any("err", err))
		} else if count < int64(rule.NotifyMinFires) {
			reason := fmt.Sprintf("dampened: %d/%d fires in %ds window",
				count, rule.NotifyMinFires, rule.NotifyWindowSeconds)
			if err := u.RecordRepeatSuppressed(ctx, res.Incident.ID, occurredAt, reason); err != nil {
				u.log.Warn("alert: record repeat_suppressed (dampening) failed", slog.Any("err", err))
			}
			return
		}
	}

	// Inhibition: a higher-priority active incident on the same scope can
	// suppress the notify. The firing event has already been recorded; we
	// add an `inhibited` event so operators see why nothing was sent.
	if opts.Inhibitor != nil {
		if reason, ok := opts.Inhibitor.Suppress(ctx, res.Incident); ok {
			if err := u.createEvent(ctx, &model.Event{
				IncidentID:  res.Incident.ID,
				EventType:   model.EventTypeInhibited,
				StatusAfter: res.Incident.Status,
				Severity:    res.Incident.Severity,
				Title:       res.Incident.Title,
				ActorType:   model.ActorTypeSystem,
				Reason:      reason,
				OccurredAt:  occurredAt,
			}, res.Incident.Rule); err != nil {
				u.log.Warn("alert: record inhibited event failed", slog.Any("err", err))
			}
			return
		}
	}

	channels := u.resolveChannels(ctx, res.Incident, opts)
	if len(channels) == 0 {
		return
	}

	anySuccess := false
	for _, ch := range channels {
		if ch == nil || ch.Name == "" {
			continue
		}
		if !ch.Enabled {
			continue
		}
		// A persisted channel (ID>0) with no destination in its config is
		// the seeded placeholder an operator never filled in. The notifier
		// no-ops on it and we'd otherwise write a misleading
		// "notification_sent" event — exactly the timeline noise operators
		// reported for the un-configured default channels. Skip it so the
		// timeline only records real sends. Synthetic fallback channels
		// (ID==0) bridge to the env-configured notifier and never write
		// events, so leave those alone.
		if ch.ID > 0 && !channelHasDestination(ch) {
			continue
		}

		var deliveryID uint64
		if ch.ID > 0 {
			id, recErr := u.RecordDelivery(ctx, res.Incident.ID, ch.ID)
			if recErr != nil {
				u.log.Warn("alert: record delivery failed",
					slog.String("channel", ch.Name),
					slog.Any("err", recErr),
				)
			}
			deliveryID = id
		}

		var sendErr error
		if ch.ID > 0 {
			// Persisted channel: build the typed sender from its
			// ChannelType + ConfigJSON, then deliver through the Notifier
			// seam (SendVia) so router gating/timeout — and test stubs —
			// still apply. (Synthetic rows, ID==0, bridge to the
			// env-config notifier by name in the else branch.)
			if sender, berr := BuildSenderFromChannel(ch); berr != nil {
				sendErr = berr
			} else {
				sendErr = opts.Notifier.SendVia(ctx, msg, sender)
			}
		} else {
			sendErr = opts.Notifier.Send(ctx, msg, ch.Name)
		}
		if sendErr == nil {
			anySuccess = true
		} else {
			u.log.Warn("alert: notify failed",
				slog.String("channel", ch.Name),
				slog.Uint64("incident_id", res.Incident.ID),
				slog.Any("err", sendErr),
			)
		}
		if deliveryID > 0 {
			if err := u.FinishDelivery(ctx, deliveryID, res.Incident.ID, ch.Name, sendErr, occurredAt); err != nil {
				u.log.Warn("alert: finish delivery failed",
					slog.Uint64("delivery_id", deliveryID),
					slog.Any("err", err),
				)
			}
		}
	}

	if anySuccess {
		if err := u.MarkNotified(ctx, res.Incident.ID, occurredAt); err != nil && !errors.Is(err, errs.ErrNotFound) {
			u.log.Warn("alert: mark notified failed", slog.Any("err", err))
		}
	}
}

// lookupRuleForIncident loads the rule that produced an incident, by
// rule_key. Returns nil on any miss (no rule key, repo not wired, lookup
// error) so callers can treat "no rule" identically — dampening etc.
// degrade gracefully when the rule has been deleted or the firing came
// from a synthetic path that bypassed CreateRule.
func (u *Usecase) lookupRuleForIncident(ctx context.Context, incident *model.Incident) *model.Rule {
	if u.repo == nil || incident == nil || incident.Rule == "" {
		return nil
	}
	rule, err := u.repo.GetRuleByKey(ctx, incident.Rule)
	if err != nil || rule == nil {
		return nil
	}
	return rule
}

// channelHasDestination reports whether a persisted channel has a usable
// delivery target — a non-empty `url` in its config. The seeded
// placeholder channels (webhook / slack / feishu / dingtalk, created
// disabled with an empty `{}` config so the UI can list the supported
// types) return false, so MaybeNotify skips them instead of recording a
// no-op "notification_sent". Channels carry their destination under the
// "url" key (see encodeChannelConfig); url-less configs aren't deliverable.
func channelHasDestination(ch *model.Channel) bool {
	if ch == nil {
		return false
	}
	cfg, err := ch.Config()
	if err != nil {
		return false
	}
	return strings.TrimSpace(cfg["endpoint"]) != "" || strings.TrimSpace(cfg["url"]) != ""
}

// BuildSenderFromChannel constructs a notify.Sender from a persisted
// channel's ChannelType + ConfigJSON. Until this existed, UI/seeded
// channels were never actually delivered: MaybeNotify sent by name to the
// env-config Router (which only knows env-config channel names), and
// ChannelType/ConfigJSON were stored but unused — so WeCom and every
// hand-created channel silently no-op'd. The URL lives under the
// "endpoint" key (encodeChannelConfig); "secret" is the optional signing /
// credential field (it carries the chat_id for telegram).
func BuildSenderFromChannel(ch *model.Channel) (notify.Sender, error) {
	cfg, err := ch.Config()
	if err != nil {
		return nil, fmt.Errorf("decode channel config: %w", err)
	}
	endpoint := strings.TrimSpace(cfg["endpoint"])
	if endpoint == "" {
		endpoint = strings.TrimSpace(cfg["url"]) // defensive: legacy key
	}
	if endpoint == "" {
		return nil, fmt.Errorf("channel %q has no endpoint", ch.Name)
	}
	secret := strings.TrimSpace(cfg["secret"])
	switch ch.ChannelType {
	case model.ChannelTypeSlack:
		return notify.NewSlackSender(ch.Name, endpoint, nil), nil
	case model.ChannelTypeFeishu:
		return notify.NewFeishuSender(ch.Name, endpoint, secret, nil), nil
	case model.ChannelTypeDingTalk:
		return notify.NewDingTalkSender(ch.Name, endpoint, secret, nil), nil
	case model.ChannelTypeWeCom:
		return notify.NewWeComSender(ch.Name, endpoint, nil), nil
	case model.ChannelTypeTelegram:
		// endpoint = bot sendMessage URL; secret field carries the chat_id.
		return notify.NewTelegramSender(ch.Name, endpoint, secret, nil), nil
	case model.ChannelTypeWebhook, "":
		return notify.NewGenericWebhookSender(ch.Name, endpoint, secret, nil), nil
	default:
		return nil, fmt.Errorf("unknown channel type %q", ch.ChannelType)
	}
}

// resolveChannels picks the recipients for an incident. Resolver wins when
// supplied (the production path); DefaultChannels is the test / legacy
// fallback. Channels are loaded by name from the repo so deliveries get the
// real channel_id; failed lookups fall through to a synthetic row (Name
// only) so the notify still happens but no delivery row is recorded.
func (u *Usecase) resolveChannels(ctx context.Context, incident *model.Incident, opts NotifyOpts) []*model.Channel {
	if opts.Resolver != nil {
		return opts.Resolver.ChannelsFor(ctx, incident)
	}
	out := make([]*model.Channel, 0, len(opts.DefaultChannels))
	for _, name := range opts.DefaultChannels {
		if name == "" {
			continue
		}
		ch, err := u.repo.GetChannelByName(ctx, name)
		if err != nil {
			out = append(out, &model.Channel{Name: name, Enabled: true})
			continue
		}
		out = append(out, ch)
	}
	return out
}

// CooldownExceeded returns true when enough wall-clock time has elapsed since
// the last successful notify on this incident that another notification is
// permitted. A nil last_notified_at always returns true (first notify path).
func (u *Usecase) CooldownExceeded(incident *model.Incident, cooldown time.Duration, now time.Time) bool {
	if incident == nil {
		return true
	}
	if cooldown <= 0 {
		return true
	}
	if incident.LastNotifiedAt == nil || incident.LastNotifiedAt.IsZero() {
		return true
	}
	return now.Sub(*incident.LastNotifiedAt) >= cooldown
}

// matchSilence checks whether an incident is silenced. Two sources count:
//   - The incident itself is in status=silenced and silenced_until is in the
//     future (operator-issued silence via SilenceIncident).
//   - An active silence row matches the incident's scope/edge/rule (operator
//     created a forward-looking silence before the incident existed).
func (u *Usecase) matchSilence(ctx context.Context, incident *model.Incident, at time.Time) (bool, *uint64) {
	if incident.Status == model.IncidentStatusSilenced {
		if incident.SilencedUntil != nil && incident.SilencedUntil.After(at) {
			return true, nil
		}
	}
	silences, err := u.repo.ListActiveSilences(ctx, at)
	if err != nil {
		u.log.Warn("alert: list active silences failed", slog.Any("err", err))
		return false, nil
	}
	for _, s := range silences {
		if !silenceMatches(s, incident) {
			continue
		}
		id := s.ID
		return true, &id
	}
	return false, nil
}

// silenceMatches applies the structured matcher set
// (PR-A scope: scope_type / edge_id / rule). Empty fields on the silence row
// act as wildcards.
func silenceMatches(s *model.Silence, inc *model.Incident) bool {
	if s.ScopeType != "" && s.ScopeType != inc.ScopeType {
		return false
	}
	if s.DeviceID != nil {
		if inc.DeviceID == nil || *s.DeviceID != *inc.DeviceID {
			return false
		}
	}
	if s.Rule != "" && s.Rule != inc.Rule {
		return false
	}
	return true
}

func validateFiring(in FiringInput) error {
	if strings.TrimSpace(in.Rule) == "" {
		return fmt.Errorf("%w: rule required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.ScopeType) == "" {
		return fmt.Errorf("%w: scope_type required", errs.ErrInvalid)
	}
	if strings.TrimSpace(in.Severity) == "" {
		return fmt.Errorf("%w: severity required", errs.ErrInvalid)
	}
	if in.ScopeType == model.RuleScopeHost && (in.DeviceID == nil || *in.DeviceID == 0) {
		return fmt.Errorf("%w: device_id required for host scope", errs.ErrInvalid)
	}
	return nil
}

func buildDedupeKey(scopeType string, edgeID *uint64, rule string) string {
	switch scopeType {
	case model.RuleScopeHost:
		var id uint64
		if edgeID != nil {
			id = *edgeID
		}
		return fmt.Sprintf("host:%d:%s", id, rule)
	case model.RuleScopeMonitoringPipeline:
		return fmt.Sprintf("pipeline:%s", rule)
	default:
		return fmt.Sprintf("%s:%s", scopeType, rule)
	}
}

func defaultTitle(in FiringInput) string {
	if in.DeviceID != nil {
		return fmt.Sprintf("设备 %d %s", *in.DeviceID, in.Rule)
	}
	return in.Rule
}

func encodeLabels(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func encodeFiringSnapshot(in FiringInput) string {
	snap := map[string]any{
		"scope_type": in.ScopeType,
		"rule":       in.Rule,
		"severity":   in.Severity,
	}
	if in.DeviceID != nil {
		snap["device_id"] = *in.DeviceID
	}
	if in.Value != nil {
		snap["value"] = *in.Value
	}
	if in.Threshold != nil {
		snap["threshold"] = *in.Threshold
	}
	if len(in.Labels) > 0 {
		snap["labels"] = in.Labels
	}
	out, err := json.Marshal(snap)
	if err != nil {
		return "{}"
	}
	return string(out)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func parseSilenceUntil(now time.Time, raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("value required")
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return now.Add(d), nil
	}
	if ts, err := time.Parse(time.RFC3339, raw); err == nil {
		return ts.UTC(), nil
	}
	if unixSec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(unixSec, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("must be duration, RFC3339, or unix seconds")
}
