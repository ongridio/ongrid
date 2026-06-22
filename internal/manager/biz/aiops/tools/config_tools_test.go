package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

type fakeConfigManager struct {
	draftAlertCalls int
	applyAlertCalls int
	lastDraft       AlertRuleConfigArgs
	lastApply       AlertRuleApplyArgs
}

func (f *fakeConfigManager) DraftAlertRuleConfig(_ context.Context, _ ConfigCaller, in AlertRuleConfigArgs) (*ConfigDraft, error) {
	f.draftAlertCalls++
	f.lastDraft = in
	return &ConfigDraft{
		Kind:      ConfigResultKindDraft,
		Domain:    ConfigDomainAlertRule,
		Action:    "create",
		Summary:   "draft",
		ApplyTool: ToolNameApplyConfigChange,
	}, nil
}

func (f *fakeConfigManager) ApplyAlertRuleConfig(_ context.Context, _ ConfigCaller, in AlertRuleApplyArgs) (*ConfigApplyResult, error) {
	f.applyAlertCalls++
	f.lastApply = in
	return &ConfigApplyResult{
		Kind:       ConfigResultKindApply,
		Domain:     ConfigDomainAlertRule,
		Action:     "create",
		Status:     "applied",
		ResourceID: 7,
	}, nil
}

func TestConfigDraftToolCallsAlertRuleManager(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewDraftConfigChangeTool(fake, nil)
	got, err := tool.InvokableRun(context.Background(), `{"domain":"alert_rule","action":"create","lookback_seconds":3600,"rule":{"rule_key":"cpu_high","name":"CPU High"}}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.draftAlertCalls != 1 {
		t.Fatalf("draft calls = %d, want 1", fake.draftAlertCalls)
	}
	if fake.lastDraft.Action != "create" || fake.lastDraft.LookbackSeconds != 3600 || fake.lastDraft.Rule.RuleKey != "cpu_high" {
		t.Fatalf("draft args = %+v, want create/cpu_high", fake.lastDraft)
	}
	if !strings.Contains(got, `"kind":"config_draft"`) {
		t.Fatalf("unexpected result: %s", got)
	}
}

func TestConfigDraftToolUsesInvokeUserTextWhenRequestTextMissing(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewDraftConfigChangeTool(fake, nil)
	_, err := tool.InvokableRun(context.Background(), `{"domain":"alert_rule","action":"create","rule":{"rule_key":"log_rule","name":"Log Rule"}}`,
		basetool.WithUserText("系统 journald 日志 level=6 ERROR 超过 3 次"))
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.lastDraft.RequestText != "系统 journald 日志 level=6 ERROR 超过 3 次" {
		t.Fatalf("RequestText = %q, want invoke user text", fake.lastDraft.RequestText)
	}
}

func TestConfigDraftToolRejectsEmptyRule(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewDraftConfigChangeTool(fake, nil)
	_, err := tool.InvokableRun(context.Background(), `{"domain":"alert_rule","action":"create","request_text":"创建 CPU 告警","rule":{}}`)
	if err == nil {
		t.Fatalf("expected empty rule error")
	}
	if fake.draftAlertCalls != 0 {
		t.Fatalf("draft called for empty rule")
	}
}

func TestConfigDraftToolInfoDescribesDraftValidationContract(t *testing.T) {
	tool := NewDraftConfigChangeTool(&fakeConfigManager{}, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, want := range []string{"config_validation_failed", "config_draft", "draft_hash"} {
		if !strings.Contains(info.WhenToUse, want) {
			t.Fatalf("WhenToUse = %q, want %q", info.WhenToUse, want)
		}
	}
	params := string(info.Parameters)
	for _, want := range []string{"metric_raw accepts expr/promql/query", "source_explicit=true", "final PromQL/LogQL result keeps a device_id label"} {
		if !strings.Contains(params, want) {
			t.Fatalf("Parameters should describe alert draft contract, missing %q: %s", want, params)
		}
	}
	for _, overSpecified := range []string{`conn_type=\"current\"`, `clamp_min`, `ongrid_source=\"db:...\"`} {
		if strings.Contains(params, overSpecified) {
			t.Fatalf("Parameters should not hard-code database PromQL policy %q: %s", overSpecified, params)
		}
	}
}

func TestConfigDraftToolRejectsUnsupportedDomain(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewDraftConfigChangeTool(fake, nil)
	_, err := tool.InvokableRun(context.Background(), `{"domain":"notification_channel","action":"create"}`)
	if err == nil {
		t.Fatalf("expected unsupported domain error")
	}
	if fake.draftAlertCalls != 0 {
		t.Fatalf("draft called for unsupported domain")
	}
}

func TestConfigDraftToolRejectsNonCreateAction(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewDraftConfigChangeTool(fake, nil)
	_, err := tool.InvokableRun(context.Background(), `{"domain":"alert_rule","action":"update"}`)
	if err == nil {
		t.Fatalf("expected unsupported action error")
	}
	if fake.draftAlertCalls != 0 {
		t.Fatalf("draft called for unsupported action")
	}
}

func TestConfigApplyToolRequiresConfirmation(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	_, err := tool.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","confirmed":false}`)
	if err == nil {
		t.Fatalf("expected confirmation error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called without confirmation")
	}
}

func TestConfigApplyToolRequiresAdmin(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 2, Role: "viewer"})
	_, err := tool.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","confirmed":true}`)
	if err == nil {
		t.Fatalf("expected forbidden error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called for non-admin")
	}
}

func TestConfigApplyToolAdminCallsManager(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	got, err := tool.InvokableRun(ctx, applyArgsJSON(t, "create", AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"}, ""))
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.applyAlertCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", fake.applyAlertCalls)
	}
	if !strings.Contains(got, `"status":"applied"`) {
		t.Fatalf("unexpected result: %s", got)
	}
}

func TestConfigApplyToolUsesPayloadDefaults(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	args := applyArgsJSON(t, "create", AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"}, "")
	if _, err := tool.InvokableRun(ctx, args); err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.applyAlertCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", fake.applyAlertCalls)
	}
	if fake.lastApply.Action != "create" || fake.lastApply.Rule.RuleKey != "cpu_high" {
		t.Fatalf("apply args = %+v, want payload defaults", fake.lastApply)
	}
}

func TestConfigApplyToolPassesDraftIDAndHashToManager(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	rule := AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"}
	payload, hash, err := AlertRuleConfigDraftPayloadForID("create", rule, "draft-123")
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftPayloadForID() error = %v", err)
	}
	args := fmt.Sprintf(`{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":%q,"payload":%s}`, hash, string(payload))

	if _, err := tool.InvokableRun(ctx, args); err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.lastApply.DraftID != "draft-123" || fake.lastApply.DraftHash != hash {
		t.Fatalf("apply draft identity = (%q,%q), want (%q,%q)", fake.lastApply.DraftID, fake.lastApply.DraftHash, "draft-123", hash)
	}
}

func TestConfigApplyToolUsesPayloadAsSourceOfTruth(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	payload, hash, err := AlertRuleConfigDraftPayload("create", AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"})
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftPayload() error = %v", err)
	}
	args := fmt.Sprintf(`{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":%q,"payload":%s,"rule":{"rule_key":"mem_high","name":"Memory High"}}`, hash, string(payload))

	if _, err := tool.InvokableRun(ctx, args); err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	if fake.lastApply.Rule.RuleKey != "cpu_high" {
		t.Fatalf("apply rule = %+v, want payload rule cpu_high", fake.lastApply.Rule)
	}
}

func TestConfigApplyToolRejectsMismatchedDraftHash(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	_, err := tool.InvokableRun(ctx, applyArgsJSON(t, "create", AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"}, "sha256:bad"))
	if err == nil {
		t.Fatalf("expected draft_hash mismatch error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called for mismatched draft_hash")
	}
}

func TestConfigApplyToolRequiresDraftPayload(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	_, err := tool.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","confirmed":true,"draft_hash":"sha256:bad"}`)
	if err == nil {
		t.Fatalf("expected missing payload error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called without payload")
	}
}

func TestConfigApplyToolRejectsNonCreateAction(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	_, err := tool.InvokableRun(ctx, applyArgsJSON(t, "disable", AlertRuleConfigInput{RuleKey: "cpu_high", Name: "CPU High"}, ""))
	if err == nil {
		t.Fatalf("expected unsupported action error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called for unsupported action")
	}
}

func applyArgsJSON(t *testing.T, action string, rule AlertRuleConfigInput, hashOverride string) string {
	t.Helper()
	payload, hash, err := AlertRuleConfigDraftPayload(action, rule)
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftPayload() error = %v", err)
	}
	if hashOverride != "" {
		hash = hashOverride
	}
	return fmt.Sprintf(`{"domain":"alert_rule","action":%q,"confirmed":true,"draft_hash":%q,"payload":%s}`, action, hash, string(payload))
}
