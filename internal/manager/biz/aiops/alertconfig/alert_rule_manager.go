package alertconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	configDraftPreviewSeriesLimit = 60
	configDraftPreviewSampleLimit = 20
	alertRuleDraftTTL             = 30 * time.Minute
)

type AlertRulePort interface {
	PreviewRule(ctx context.Context, caller aiopstools.ConfigCaller, in RuleInput, lookbackSeconds int) (*PreviewResult, error)
	CreateRule(ctx context.Context, caller aiopstools.ConfigCaller, in RuleInput) (*Rule, error)
}

type RuleCondition struct {
	Metric     string
	Operator   string
	Threshold  float64
	Window     string
	For        string
	Aggregator string
}

type RuleInput struct {
	RuleKey             string
	Kind                string
	Name                string
	ScopeType           string
	JoinMode            string
	Severity            string
	Enabled             bool
	Conditions          []RuleCondition
	Spec                map[string]interface{}
	Labels              map[string]string
	RunbookURL          string
	NotifyChannelIDs    []uint64
	NotifyWindowSeconds int
	NotifyMinFires      int
}

type Rule struct {
	ID   uint64
	Kind string
	Name string
}

type PreviewSample struct {
	Timestamp time.Time         `json:"ts"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64           `json:"value"`
	Summary   string            `json:"summary"`
}

type PreviewSeriesPoint struct {
	Timestamp time.Time `json:"ts"`
	Value     float64   `json:"value"`
}

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

// AlertRuleManager owns the natural-language alert-rule draft/apply flow.
// Persistence and preview execution stay behind AlertRulePort so this package
// does not depend on service-layer DTOs.
type AlertRuleManager struct {
	alert      AlertRulePort
	drafts     alertRuleDraftStore
	newDraftID func() (string, error)
}

// NewAlertRuleManager wires natural-language alert-rule draft/apply tools to
// a narrow alert-rule port.
func NewAlertRuleManager(alertSvc AlertRulePort) *AlertRuleManager {
	return &AlertRuleManager{
		alert:      alertSvc,
		drafts:     newMemoryAlertRuleDraftStore(time.Now),
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
	ruleInput := toAlertRuleInput(compiled.Rule)
	res, err := a.alert.PreviewRule(ctx, caller, ruleInput, in.LookbackSeconds)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule: %w", err)
	}
	validation := validateAlertRuleDraft(compiled.Rule, in.RequestText, res)
	warnings := validationWarnings(validation)
	previewRes := compactAlertPreview(res)
	preview, err := configRaw(previewRes)
	if err != nil {
		return nil, err
	}
	if validationHasErrors(validation) {
		return &aiopstools.ConfigDraft{
			Kind:       aiopstools.ConfigResultKindValidationFailed,
			Domain:     aiopstools.ConfigDomainAlertRule,
			Action:     compiled.Action,
			Summary:    "alert rule draft validation failed: " + compiled.Summary,
			Preview:    preview,
			Validation: &validation,
			Warnings:   warnings,
		}, nil
	}
	newDraftID := a.newDraftID
	if newDraftID == nil {
		newDraftID = newAlertRuleDraftID
	}
	if a.drafts == nil {
		a.drafts = newMemoryAlertRuleDraftStore(time.Now)
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
		ExpiresAt: a.drafts.expiresAt(alertRuleDraftTTL),
	})
	return &aiopstools.ConfigDraft{
		Kind:               aiopstools.ConfigResultKindDraft,
		Domain:             aiopstools.ConfigDomainAlertRule,
		Action:             compiled.Action,
		Summary:            compiled.Summary,
		Payload:            payload,
		Preview:            preview,
		Validation:         &validation,
		Warnings:           warnings,
		Scope:              alertRuleScopeSummary(compiled.Rule),
		ConfirmationPrompt: alertRuleConfirmationPrompt(compiled.Rule),
		Rollback:           "可在 Alerts 规则列表中禁用或继续编辑该规则。",
		ApplyTool:          aiopstools.ToolNameApplyConfigChange,
		DraftHash:          draftHash,
	}, nil
}

func alertRuleScopeSummary(rule aiopstools.AlertRuleConfigInput) *aiopstools.ConfigScopeSummary {
	switch rule.ScopeType {
	case "host":
		return &aiopstools.ConfigScopeSummary{
			Type:       "host",
			Label:      "主机级",
			Reason:     "命中后会关联到具体设备，适合主机资源、系统日志以及在设备上采集的数据库实例指标。",
			ChangeHint: "如果要改成全局汇总，可以回复“改成全局”。",
		}
	case "monitoring_pipeline":
		return &aiopstools.ConfigScopeSummary{
			Type:       "monitoring_pipeline",
			Label:      "平台自身",
			Reason:     "用于监控平台采集、存储或告警链路自身的健康状态。",
			ChangeHint: "如果要改成业务告警范围，可以回复要修改的范围。",
		}
	default:
		return &aiopstools.ConfigScopeSummary{
			Type:       "global",
			Label:      "全局",
			Reason:     "命中后不会绑定到单台设备，适合服务级、SLO、Trace 或明确按整体汇总的规则。",
			ChangeHint: "如果要改成按主机触发，可以回复“改成主机”。",
		}
	}
}

func alertRuleConfirmationPrompt(rule aiopstools.AlertRuleConfigInput) string {
	scope := alertRuleScopeSummary(rule)
	if scope == nil || scope.Label == "" {
		return "请确认是否应用这条告警规则草案。"
	}
	return "当前告警范围：" + scope.Label + "。" + scope.Reason + scope.ChangeHint + "确认无误后可点击确认应用或回复“ok”。"
}

func compactAlertPreview(in *PreviewResult) *PreviewResult {
	if in == nil {
		return nil
	}
	out := *in
	out.Series = samplePreviewSeries(in.Series, configDraftPreviewSeriesLimit)
	out.Samples = samplePreviewSamples(in.Samples, configDraftPreviewSampleLimit)
	return &out
}

func samplePreviewSeries(in []PreviewSeriesPoint, limit int) []PreviewSeriesPoint {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]PreviewSeriesPoint(nil), in...)
	}
	if limit == 1 {
		return []PreviewSeriesPoint{in[len(in)-1]}
	}
	out := make([]PreviewSeriesPoint, 0, limit)
	last := len(in) - 1
	for i := 0; i < limit; i++ {
		out = append(out, in[i*last/(limit-1)])
	}
	return out
}

func samplePreviewSamples(in []PreviewSample, limit int) []PreviewSample {
	if limit <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= limit {
		return append([]PreviewSample(nil), in...)
	}
	if limit == 1 {
		return []PreviewSample{in[len(in)-1]}
	}
	out := make([]PreviewSample, 0, limit)
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
	if a.drafts == nil {
		a.drafts = newMemoryAlertRuleDraftStore(time.Now)
	}
	lease, err := a.drafts.beginApply(caller, action, in.Rule, in.DraftID, in.DraftHash)
	if err != nil {
		return nil, err
	}
	applied := false
	defer func() {
		if !applied {
			lease.rollback()
		}
	}()
	ruleInput := toAlertRuleInput(in.Rule)
	res, err := a.alert.PreviewRule(ctx, caller, ruleInput, 0)
	if err != nil {
		return nil, fmt.Errorf("preview alert rule before create: %w", err)
	}
	if res != nil && res.SkippedReason != "" {
		reason := res.SkippedReason
		if alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
			return nil, fmt.Errorf("%w: preview skipped before create: %s", errs.ErrInvalid, reason)
		}
	}
	normalizedRule := alertdraft.NormalizeRuleConfigInput(in.Rule)
	validation := validateAlertRuleDraft(normalizedRule, in.ConfirmationText, res)
	if validationHasErrors(validation) {
		return nil, fmt.Errorf("%w: alert rule draft validation failed before create: %s", errs.ErrInvalid, validationErrorMessage(validation))
	}
	rule, err := a.alert.CreateRule(ctx, caller, ruleInput)
	if err != nil {
		return nil, fmt.Errorf("create alert rule: %w", err)
	}
	lease.commit()
	applied = true
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

func toAlertRuleInput(in aiopstools.AlertRuleConfigInput) RuleInput {
	in = alertdraft.NormalizeRuleConfigInput(in)
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	conds := make([]RuleCondition, 0, len(in.Conditions))
	for _, c := range in.Conditions {
		conds = append(conds, RuleCondition{
			Metric:     c.Metric,
			Operator:   c.Operator,
			Threshold:  c.Threshold,
			Window:     c.Window,
			For:        c.For,
			Aggregator: c.Aggregator,
		})
	}
	return RuleInput{
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

type alertRuleDraftStore interface {
	put(rec alertRuleDraftRecord)
	beginApply(caller aiopstools.ConfigCaller, action string, rule aiopstools.AlertRuleConfigInput, draftID, draftHash string) (alertRuleDraftApplyLease, error)
	expiresAt(ttl time.Duration) time.Time
}

type alertRuleDraftApplyLease interface {
	commit()
	rollback()
}
