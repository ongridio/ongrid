package aiopsconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	configDraftPreviewSeriesLimit = 60
	configDraftPreviewSampleLimit = 20
	alertRuleDraftTTL             = 30 * time.Minute
)

type alertRuleService interface {
	PreviewRule(ctx context.Context, caller managersvcalert.Caller, in managersvcalert.RuleInput, lookbackSeconds int) (*managersvcalert.PreviewResult, error)
	CreateRule(ctx context.Context, caller managersvcalert.Caller, in managersvcalert.RuleInput) (*managersvcalert.Rule, error)
}

// AlertRuleManager adapts conversational aiops config tools to the alert
// service. It keeps cmd/ongrid limited to wiring while the draft/apply flow
// remains close to the manager service layer.
type AlertRuleManager struct {
	alert      alertRuleService
	drafts     *alertRuleDraftStore
	newDraftID func() (string, error)
}

// NewAlertRuleManager wires natural-language alert-rule draft/apply tools to
// the existing alert service.
func NewAlertRuleManager(alertSvc alertRuleService) *AlertRuleManager {
	return &AlertRuleManager{
		alert:      alertSvc,
		drafts:     newAlertRuleDraftStore(time.Now),
		newDraftID: newAlertRuleDraftID,
	}
}

func (a *AlertRuleManager) DraftAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleConfigArgs) (*aiopstools.ConfigDraft, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	compiled, err := alertdraft.CompileDraft(alertdraft.CompileInput{
		Action:      in.Action,
		Rule:        in.Rule,
		RequestText: in.RequestText,
	})
	if err != nil {
		return nil, err
	}
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(compiled.Rule)
	res, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, in.LookbackSeconds)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule: %w", err)
	}
	warnings := []string(nil)
	if res != nil && res.SkippedReason != "" {
		reason := res.SkippedReason
		if alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
			return nil, fmt.Errorf("%w: preview skipped before draft: %s", errs.ErrInvalid, reason)
		}
		warnings = append(warnings, reason)
	}
	previewRes := compactAlertPreview(res)
	preview, err := configRaw(previewRes)
	if err != nil {
		return nil, err
	}
	newDraftID := a.newDraftID
	if newDraftID == nil {
		newDraftID = newAlertRuleDraftID
	}
	if a.drafts == nil {
		a.drafts = newAlertRuleDraftStore(time.Now)
	}
	draftID, err := newDraftID()
	if err != nil {
		return nil, err
	}
	payload, draftHash, err := aiopstools.AlertRuleConfigDraftPayloadForID(compiled.Action, compiled.Rule, draftID)
	if err != nil {
		return nil, err
	}
	a.drafts.put(alertRuleDraftRecord{
		ID:        draftID,
		UserID:    caller.UserID,
		Action:    compiled.Action,
		Hash:      draftHash,
		ExpiresAt: a.drafts.now().Add(alertRuleDraftTTL),
	})
	return &aiopstools.ConfigDraft{
		Kind:      aiopstools.ConfigResultKindDraft,
		Domain:    aiopstools.ConfigDomainAlertRule,
		Action:    compiled.Action,
		Summary:   compiled.Summary,
		Payload:   payload,
		Preview:   preview,
		Warnings:  warnings,
		Rollback:  "可在 Alerts 规则列表中禁用或继续编辑该规则。",
		ApplyTool: aiopstools.ToolNameApplyConfigChange,
		DraftHash: draftHash,
	}, nil
}

func compactAlertPreview(in *managersvcalert.PreviewResult) *managersvcalert.PreviewResult {
	if in == nil {
		return nil
	}
	out := *in
	out.Series = samplePreviewSeries(in.Series, configDraftPreviewSeriesLimit)
	out.Samples = samplePreviewSamples(in.Samples, configDraftPreviewSampleLimit)
	return &out
}

func samplePreviewSeries(in []managersvcalert.PreviewSeriesPoint, limit int) []managersvcalert.PreviewSeriesPoint {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]managersvcalert.PreviewSeriesPoint(nil), in...)
	}
	if limit == 1 {
		return []managersvcalert.PreviewSeriesPoint{in[len(in)-1]}
	}
	out := make([]managersvcalert.PreviewSeriesPoint, 0, limit)
	last := len(in) - 1
	for i := 0; i < limit; i++ {
		out = append(out, in[i*last/(limit-1)])
	}
	return out
}

func samplePreviewSamples(in []managersvcalert.PreviewSample, limit int) []managersvcalert.PreviewSample {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]managersvcalert.PreviewSample(nil), in...)
	}
	if limit == 1 {
		return []managersvcalert.PreviewSample{in[len(in)-1]}
	}
	out := make([]managersvcalert.PreviewSample, 0, limit)
	last := len(in) - 1
	for i := 0; i < limit; i++ {
		out = append(out, in[i*last/(limit-1)])
	}
	return out
}

func (a *AlertRuleManager) ApplyAlertRuleConfig(ctx context.Context, caller aiopstools.ConfigCaller, in aiopstools.AlertRuleApplyArgs) (*aiopstools.ConfigApplyResult, error) {
	if a.alert == nil {
		return nil, errs.ErrNotWiredYet
	}
	action, err := alertdraft.NormalizeConfigAction(in.Action)
	if err != nil {
		return nil, err
	}
	if err := a.drafts.consume(caller, action, in.Rule, in.DraftID, in.DraftHash); err != nil {
		return nil, err
	}
	alertCaller := toAlertServiceCaller(caller)
	ruleInput := toAlertRuleInput(in.Rule)
	res, err := a.alert.PreviewRule(ctx, alertCaller, ruleInput, 0)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule before create: %w", err)
	}
	if res != nil && res.SkippedReason != "" {
		reason := res.SkippedReason
		if alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
			return nil, fmt.Errorf("%w: preview skipped before create: %s", errs.ErrInvalid, reason)
		}
	}
	rule, err := a.alert.CreateRule(ctx, alertCaller, ruleInput)
	if err != nil {
		return nil, fmt.Errorf("create alert rule: %w", err)
	}
	return &aiopstools.ConfigApplyResult{
		Kind:       aiopstools.ConfigResultKindApply,
		Domain:     aiopstools.ConfigDomainAlertRule,
		Action:     action,
		Status:     "applied",
		ResourceID: rule.ID,
		Resource: &aiopstools.ConfigTarget{
			ID:   rule.ID,
			Name: rule.Name,
			Type: rule.Kind,
		},
		Message:  fmt.Sprintf("alert rule %q created", rule.Name),
		Rollback: "可在 Alerts 规则列表中禁用或继续编辑该规则。",
	}, nil
}

func toAlertRuleInput(in aiopstools.AlertRuleConfigInput) managersvcalert.RuleInput {
	in = alertdraft.NormalizeRuleConfigInput(in)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	conds := make([]managersvcalert.RuleCondition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, managersvcalert.RuleCondition{
			Metric:     c.Metric,
			Operator:   c.Operator,
			Threshold:  c.Threshold,
			Window:     c.Window,
			For:        c.For,
			Aggregator: c.Aggregator,
		})
	}
	return managersvcalert.RuleInput{
		RuleKey:             in.RuleKey,
		Kind:                in.Kind,
		Name:                in.Name,
		ScopeType:           in.ScopeType,
		JoinMode:            in.JoinMode,
		Severity:            in.Severity,
		Enabled:             enabled,
		Conditions:          conds,
		Spec:                in.Spec,
		Labels:              in.Labels,
		RunbookURL:          in.RunbookURL,
		NotifyChannelIDs:    in.NotifyChannelIDs,
		NotifyWindowSeconds: in.NotifyWindowSeconds,
		NotifyMinFires:      in.NotifyMinFires,
	}
}

func toAlertServiceCaller(c aiopstools.ConfigCaller) managersvcalert.Caller {
	return managersvcalert.Caller{UserID: c.UserID, Role: c.Role}
}

func configRaw(v interface{}) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal config payload: %w", err)
	}
	return b, nil
}

func newAlertRuleDraftID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate alert rule draft id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

type alertRuleDraftRecord struct {
	ID        string
	UserID    uint64
	Action    string
	Hash      string
	ExpiresAt time.Time
}

type alertRuleDraftStore struct {
	mu      sync.Mutex
	records map[string]alertRuleDraftRecord
	nowFn   func() time.Time
}

func newAlertRuleDraftStore(nowFn func() time.Time) *alertRuleDraftStore {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &alertRuleDraftStore{
		records: make(map[string]alertRuleDraftRecord),
		nowFn:   nowFn,
	}
}

func (s *alertRuleDraftStore) now() time.Time {
	if s == nil || s.nowFn == nil {
		return time.Now()
	}
	return s.nowFn()
}

func (s *alertRuleDraftStore) put(rec alertRuleDraftRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, existing := range s.records {
		if !existing.ExpiresAt.IsZero() && !existing.ExpiresAt.After(now) {
			delete(s.records, id)
		}
	}
	s.records[rec.ID] = rec
}

func (s *alertRuleDraftStore) consume(caller aiopstools.ConfigCaller, action string, rule aiopstools.AlertRuleConfigInput, draftID, draftHash string) error {
	if s == nil {
		return fmt.Errorf("%w: alert rule draft store is not configured", errs.ErrInvalid)
	}
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return fmt.Errorf("%w: draft_id from config_draft payload is required before applying", errs.ErrInvalid)
	}
	draftHash = strings.TrimSpace(draftHash)
	if draftHash == "" {
		return fmt.Errorf("%w: draft_hash from config_draft is required before applying", errs.ErrInvalid)
	}
	expectedHash, err := aiopstools.AlertRuleConfigDraftHashForID(action, rule, draftID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(draftHash, expectedHash) {
		return fmt.Errorf("%w: draft_hash does not match config_draft payload", errs.ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[draftID]
	if !ok {
		return fmt.Errorf("%w: config_draft was not issued by this server or was already applied", errs.ErrInvalid)
	}
	now := s.now()
	if !rec.ExpiresAt.IsZero() && !rec.ExpiresAt.After(now) {
		delete(s.records, draftID)
		return fmt.Errorf("%w: config_draft expired", errs.ErrInvalid)
	}
	if rec.UserID != caller.UserID {
		return fmt.Errorf("%w: config_draft belongs to a different user", errs.ErrForbidden)
	}
	if rec.Action != action || !strings.EqualFold(rec.Hash, draftHash) {
		return fmt.Errorf("%w: config_draft does not match the issued payload", errs.ErrInvalid)
	}
	delete(s.records, draftID)
	return nil
}
