package tools

import (
	"context"
	"strings"
	"testing"

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
	got, err := tool.InvokableRun(ctx, `{"domain":"alert_rule","action":"create","confirmed":true}`)
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
	args := `{"domain":"alert_rule","confirmed":true,"payload":{"action":"create","rule":{"rule_key":"cpu_high","name":"CPU High"}}}`
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

func TestConfigApplyToolRejectsNonCreateAction(t *testing.T) {
	fake := &fakeConfigManager{}
	tool := NewApplyConfigChangeTool(fake, nil)
	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	_, err := tool.InvokableRun(ctx, `{"domain":"alert_rule","action":"disable","confirmed":true}`)
	if err == nil {
		t.Fatalf("expected unsupported action error")
	}
	if fake.applyAlertCalls != 0 {
		t.Fatalf("apply called for unsupported action")
	}
}
