package aiopsconfig

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	alertdraft "github.com/ongridio/ongrid/internal/manager/biz/aiops/alertdraft"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managersvcalert "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type fakeAlertRuleService struct {
	preview    *managersvcalert.PreviewResult
	previewErr error
	createErr  error

	createCalls int
	lastCreate  managersvcalert.RuleInput
}

func (f *fakeAlertRuleService) PreviewRule(_ context.Context, _ managersvcalert.Caller, _ managersvcalert.RuleInput, _ int) (*managersvcalert.PreviewResult, error) {
	return f.preview, f.previewErr
}

func (f *fakeAlertRuleService) CreateRule(_ context.Context, _ managersvcalert.Caller, in managersvcalert.RuleInput) (*managersvcalert.Rule, error) {
	f.createCalls++
	f.lastCreate = in
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &managersvcalert.Rule{
		ID:       uint64(f.createCalls),
		RuleKey:  in.RuleKey,
		Kind:     in.Kind,
		Name:     in.Name,
		Severity: in.Severity,
		Enabled:  in.Enabled,
	}, nil
}

func TestNormalizeAlertRuleConfigInputCanonicalizesHostMetricAliases(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Conditions: []aiopstools.AlertRuleCondition{
			{Metric: "cpu_usage_percent", Operator: ">", Threshold: 30},
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.ScopeType)
	}
	if got.Severity != "warning" {
		t.Fatalf("Severity = %q, want warning", got.Severity)
	}
	if got.RuleKey != "cpu_high" {
		t.Fatalf("RuleKey = %q, want cpu_high", got.RuleKey)
	}
	if got.Name != "CPU > 30%" {
		t.Fatalf("Name = %q, want CPU > 30%%", got.Name)
	}
	if got.RunbookURL == "" {
		t.Fatalf("RunbookURL should be defaulted")
	}
	if len(got.Conditions) != 1 {
		t.Fatalf("conditions = %d, want 1", len(got.Conditions))
	}
	cond := got.Conditions[0]
	if cond.Metric != "cpu_pct" {
		t.Fatalf("condition metric = %q, want cpu_pct", cond.Metric)
	}
	if cond.Window != "5m" {
		t.Fatalf("condition window = %q, want 5m", cond.Window)
	}
	if cond.Aggregator != "avg" {
		t.Fatalf("condition aggregator = %q, want avg", cond.Aggregator)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesSimpleMetricRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": "cpu_usage_percent > 30 and disk_used_pct > 50 and mem_pct > 50",
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.JoinMode != "all" {
		t.Fatalf("JoinMode = %q, want all", got.JoinMode)
	}
	if got.ScopeType != "host" {
		t.Fatalf("ScopeType = %q, want host", got.ScopeType)
	}
	if got.Spec != nil {
		t.Fatalf("Spec = %#v, want nil after rewrite", got.Spec)
	}
	if len(got.Conditions) != 3 {
		t.Fatalf("conditions = %d, want 3", len(got.Conditions))
	}
	wantMetrics := []string{"cpu_pct", "disk_used_pct", "mem_pct"}
	for i, want := range wantMetrics {
		if got.Conditions[i].Metric != want {
			t.Fatalf("condition[%d].Metric = %q, want %q", i, got.Conditions[i].Metric, want)
		}
		if got.Conditions[i].Window != "5m" {
			t.Fatalf("condition[%d].Window = %q, want 5m", i, got.Conditions[i].Window)
		}
	}
}

func TestNormalizeAlertRuleConfigInputKeepsRealMetricRawPromQL(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `sum by (device_id) (rate(node_cpu_seconds_total{mode!="idle"}[5m])) > 0.8`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if len(got.Conditions) != 0 {
		t.Fatalf("conditions = %d, want 0", len(got.Conditions))
	}
	if got.Spec["expr"] != in.Spec["expr"] {
		t.Fatalf("Spec expr changed: %#v", got.Spec)
	}
}

func TestNormalizeAlertRuleConfigInputMergesSelectorIntoRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr":            `(mysql_global_status_threads_connected / mysql_global_variables_max_connections) * 100 > 80`,
			"selector":        `ongrid_source="db:mysql-test"`,
			"source_explicit": true,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`mysql_global_status_threads_connected{ongrid_source="db:mysql-test"}`,
		`mysql_global_variables_max_connections{ongrid_source="db:mysql-test"}`,
		`100 *`,
		`> 80`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
}

func TestNormalizeAlertRuleConfigInputMovesTopLevelForIntoRawSpec(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		For:  "10m",
		Spec: map[string]interface{}{
			"expr": `( max by (device_id, ongrid_source) (mysql_global_status_threads_connected) / max by (device_id, ongrid_source) (mysql_global_variables_max_connections) ) * 100 > 75`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.For != "" {
		t.Fatalf("top-level For = %q, want cleared after normalization", got.For)
	}
	if got.Spec["for"] != "10m" {
		t.Fatalf("spec.for = %#v, want 10m", got.Spec["for"])
	}
}

func TestNormalizeAlertRuleConfigInputMovesTopLevelDurationsIntoThresholdConditions(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind:   "metric_threshold",
		Window: "5m",
		For:    "15m",
		Conditions: []aiopstools.AlertRuleCondition{
			{Metric: "cpu_pct", Operator: ">", Threshold: 80},
			{Metric: "mem_pct", Operator: ">", Threshold: 90, For: "20m"},
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Window != "" || got.For != "" {
		t.Fatalf("top-level durations should be cleared, got window=%q for=%q", got.Window, got.For)
	}
	if got.Conditions[0].Window != "5m" || got.Conditions[0].For != "15m" {
		t.Fatalf("condition[0] = %#v, want window 5m and for 15m", got.Conditions[0])
	}
	if got.Conditions[1].For != "20m" {
		t.Fatalf("condition[1].For = %q, want existing 20m preserved", got.Conditions[1].For)
	}
}

func TestNormalizeAlertRuleConfigInputDropsIncompleteNotifyPolicy(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind:           "metric_raw",
		NotifyMinFires: 1,
		Spec: map[string]interface{}{
			"expr": "up == 0",
		},
	})
	if got.NotifyMinFires != 0 || got.NotifyWindowSeconds != 0 {
		t.Fatalf("notify policy = min_fires:%d window:%d, want both cleared", got.NotifyMinFires, got.NotifyWindowSeconds)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesMySQLConnectionUsageBySource(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `max by (device_id, ongrid_source) (mysql_global_status_threads_connected{ongrid_source="db:mysql-test"} / mysql_global_variables_max_connections{ongrid_source="db:mysql-test"}) * 100 > 75`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`max by (device_id, ongrid_source) (mysql_global_status_threads_connected)`,
		`max by (device_id, ongrid_source) (mysql_global_variables_max_connections)`,
		`clamp_min`,
		`> 75`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want %q", expr, want)
		}
	}
	if strings.Contains(expr, `ongrid_source="db:mysql-test"`) {
		t.Fatalf("expr = %q, should drop implicit sample database source selector", expr)
	}
}

func TestNormalizeAlertRuleConfigInputMergesSelectorWithoutTouchingPromQLSyntax(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr":            `sum by (device_id) (rate(node_network_receive_bytes_total[5m])) > 1024`,
			"selector":        `device_id="2"`,
			"source_explicit": true,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	want := `sum by (device_id) (rate(node_network_receive_bytes_total{device_id="2"}[5m])) > 1024`
	if expr != want {
		t.Fatalf("expr = %q, want %q", expr, want)
	}
}

func TestNormalizeAlertRuleConfigInputMergesExistingSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr":     `sum(rate(ongrid_http_requests_total{code=~"5.."}[5m])) / sum(rate(ongrid_http_requests_total[5m])) > 0.05`,
			"selector": `job="ongrid-manager"`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`ongrid_http_requests_total{code=~"5..",job="ongrid-manager"}`,
		`ongrid_http_requests_total{job="ongrid-manager"}`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
}

func TestNormalizeAlertRuleConfigInputReplacesConflictingExistingSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr":     `sum(rate(ongrid_http_requests_total{job="old",code=~"5.."}[5m])) > 0`,
			"selector": `job="ongrid-manager"`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	want := `ongrid_http_requests_total{code=~"5..",job="ongrid-manager"}`
	if !strings.Contains(expr, want) {
		t.Fatalf("expr = %q, want to contain %q", expr, want)
	}
	if strings.Contains(expr, `job="old"`) {
		t.Fatalf("expr = %q, should replace conflicting selector", expr)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesFriendlyHostMetricSelectorPromQL(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `disk_used_pct{mountpoint="/"} > 88`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`node_filesystem_avail_bytes{mountpoint="/"}`,
		`node_filesystem_size_bytes{mountpoint="/"}`,
		`> 88`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
	if strings.Contains(expr, "disk_used_pct") {
		t.Fatalf("expr = %q, should not contain friendly metric disk_used_pct", expr)
	}
}

func TestNormalizeAlertRuleConfigInputHostMetricSpecBecomesThreshold(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":    "cpu",
			"operator":  ">",
			"threshold": 90,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_threshold" {
		t.Fatalf("Kind = %q, want metric_threshold", got.Kind)
	}
	if got.Spec != nil {
		t.Fatalf("Spec = %#v, want nil", got.Spec)
	}
	if len(got.Conditions) != 1 || got.Conditions[0].Metric != "cpu_pct" {
		t.Fatalf("Conditions = %#v, want cpu_pct threshold condition", got.Conditions)
	}
}

func TestNormalizeAlertRuleConfigInputExpandsDatabaseCatalogMetric(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"catalog_metric":  "redis_memory_usage_pct",
			"operator":        ">",
			"threshold":       80,
			"source_explicit": true,
			"labels": map[string]interface{}{
				"device_id": "7",
			},
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		"redis_memory_used_bytes{device_id=\"7\"}",
		"redis_memory_max_bytes{device_id=\"7\"}",
		"> 80",
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if got.ScopeType != "global" {
		t.Fatalf("ScopeType = %q, want global", got.ScopeType)
	}
}

func TestNormalizeAlertRuleConfigInputExpandsDatabaseCatalogMetricWithMatcherArray(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"catalog_metric":  "mysql_connection_usage_pct",
			"operator":        ">",
			"threshold":       80,
			"source_explicit": true,
			"selector": []interface{}{
				`device_id="5"`,
				`ongrid_source="db:mysql-1"`,
			},
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`mysql_global_status_threads_connected{device_id="5",ongrid_source="db:mysql-1"}`,
		`mysql_global_variables_max_connections{device_id="5",ongrid_source="db:mysql-1"}`,
		"> 80",
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want to contain %q", expr, want)
		}
	}
}

func TestNormalizeAlertRuleConfigInputExpandsDatabaseCatalogMetricWithSelectorMap(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"catalog_metric":  "mysql_threads_running",
			"operator":        ">",
			"threshold":       20,
			"source_explicit": true,
			"matchers": map[string]interface{}{
				"ongrid_source": "db:mysql-1",
				"device_id":     "5",
			},
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	want := `mysql_global_status_threads_running{device_id="5",ongrid_source="db:mysql-1"}`
	if !strings.Contains(expr, want) {
		t.Fatalf("expr = %q, want to contain %q", expr, want)
	}
}

func TestDraftAlertRuleConfigIncludesMatchingDraftHash(t *testing.T) {
	adapter := NewAlertRuleManager(managersvcalert.NewStub())
	draft, err := adapter.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			Kind: "trace_latency",
			Spec: map[string]interface{}{
				"service":      "checkout",
				"threshold_ms": 750,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if draft.DraftHash == "" {
		t.Fatalf("DraftHash should be populated")
	}
	var payload struct {
		DraftID string                          `json:"draft_id"`
		Action  string                          `json:"action"`
		Rule    aiopstools.AlertRuleConfigInput `json:"rule"`
	}
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	if payload.DraftID == "" {
		t.Fatalf("payload draft_id should be populated")
	}
	want, err := aiopstools.AlertRuleConfigDraftHashForID(payload.Action, payload.Rule, payload.DraftID)
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftHash() error = %v", err)
	}
	if draft.DraftHash != want {
		t.Fatalf("DraftHash = %q, want %q", draft.DraftHash, want)
	}
}

func TestCompactAlertPreviewLimitsLongSeriesAndSamples(t *testing.T) {
	now := time.Now()
	in := &managersvcalert.PreviewResult{}
	for i := 0; i < 200; i++ {
		in.Series = append(in.Series, managersvcalert.PreviewSeriesPoint{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Value:     float64(i),
		})
		in.Samples = append(in.Samples, managersvcalert.PreviewSample{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Value:     float64(i),
			Summary:   "sample",
		})
	}

	got := compactAlertPreview(in)
	if got == nil {
		t.Fatal("compactAlertPreview() = nil")
	}
	if len(got.Series) != configDraftPreviewSeriesLimit {
		t.Fatalf("Series len = %d, want %d", len(got.Series), configDraftPreviewSeriesLimit)
	}
	if len(got.Samples) != configDraftPreviewSampleLimit {
		t.Fatalf("Samples len = %d, want %d", len(got.Samples), configDraftPreviewSampleLimit)
	}
	if got.Series[0].Value != 0 || got.Series[len(got.Series)-1].Value != 199 {
		t.Fatalf("Series should preserve first and last point, got first=%v last=%v", got.Series[0].Value, got.Series[len(got.Series)-1].Value)
	}
	if got.Samples[0].Value != 0 || got.Samples[len(got.Samples)-1].Value != 199 {
		t.Fatalf("Samples should preserve first and last point, got first=%v last=%v", got.Samples[0].Value, got.Samples[len(got.Samples)-1].Value)
	}
	if len(in.Series) != 200 || len(in.Samples) != 200 {
		t.Fatalf("compactAlertPreview mutated input lengths: series=%d samples=%d", len(in.Series), len(in.Samples))
	}
}

func applyArgsFromDraft(t *testing.T, draft *aiopstools.ConfigDraft) aiopstools.AlertRuleApplyArgs {
	t.Helper()
	if draft == nil {
		t.Fatal("draft is nil")
	}
	var payload struct {
		DraftID string                          `json:"draft_id"`
		Action  string                          `json:"action"`
		Rule    aiopstools.AlertRuleConfigInput `json:"rule"`
	}
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("unmarshal draft payload: %v", err)
	}
	if payload.DraftID == "" || draft.DraftHash == "" {
		t.Fatalf("draft missing id/hash: id=%q hash=%q", payload.DraftID, draft.DraftHash)
	}
	return aiopstools.AlertRuleApplyArgs{
		Action:    payload.Action,
		Rule:      payload.Rule,
		DraftID:   payload.DraftID,
		DraftHash: draft.DraftHash,
		Confirmed: true,
	}
}

func TestApplyAlertRuleConfigRejectsUnissuedDraft(t *testing.T) {
	fake := &fakeAlertRuleService{}
	adapter := NewAlertRuleManager(fake)
	rule := aiopstools.AlertRuleConfigInput{
		RuleKey:  "trace_latency_checkout",
		Kind:     "trace_latency",
		Name:     "Trace latency checkout",
		Severity: "warning",
		Spec: map[string]interface{}{
			"service":      "checkout",
			"threshold_ms": 750,
		},
	}
	draftID := "forged-draft"
	draftHash, err := aiopstools.AlertRuleConfigDraftHashForID("create", rule, draftID)
	if err != nil {
		t.Fatalf("AlertRuleConfigDraftHashForID() error = %v", err)
	}

	_, err = adapter.ApplyAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{UserID: 7, Role: "admin"}, aiopstools.AlertRuleApplyArgs{
		Action:    "create",
		Rule:      rule,
		DraftID:   draftID,
		DraftHash: draftHash,
	})
	if err == nil {
		t.Fatalf("expected unissued draft error")
	}
	if !strings.Contains(err.Error(), "not issued") {
		t.Fatalf("error = %v, want unissued draft rejection", err)
	}
	if fake.createCalls != 0 {
		t.Fatalf("create calls = %d, want 0", fake.createCalls)
	}
}

func TestApplyAlertRuleConfigConsumesDraftOnce(t *testing.T) {
	fake := &fakeAlertRuleService{}
	adapter := NewAlertRuleManager(fake)
	caller := aiopstools.ConfigCaller{UserID: 7, Role: "admin"}
	draft, err := adapter.DraftAlertRuleConfig(context.Background(), caller, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "trace_latency_checkout",
			Kind:     "trace_latency",
			Name:     "Trace latency checkout",
			Severity: "warning",
			Spec: map[string]interface{}{
				"service":      "checkout",
				"threshold_ms": 750,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	apply := applyArgsFromDraft(t, draft)

	if _, err := adapter.ApplyAlertRuleConfig(context.Background(), caller, apply); err != nil {
		t.Fatalf("first ApplyAlertRuleConfig() error = %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("create calls after first apply = %d, want 1", fake.createCalls)
	}
	if _, err := adapter.ApplyAlertRuleConfig(context.Background(), caller, apply); err == nil {
		t.Fatalf("second ApplyAlertRuleConfig() should reject consumed draft")
	}
	if fake.createCalls != 1 {
		t.Fatalf("create calls after replay = %d, want 1", fake.createCalls)
	}
}

func TestDraftAlertRuleConfigRejectsStructuralSkippedPreview(t *testing.T) {
	adapter := NewAlertRuleManager(managersvcalert.NewStub())
	_, err := adapter.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "trace_latency_missing_service",
			Kind:     "trace_latency",
			Name:     "Trace latency missing service",
			Severity: "warning",
			Spec: map[string]interface{}{
				"threshold_ms": 750,
			},
		},
	})
	if err == nil {
		t.Fatalf("expected skipped preview error")
	}
	if !strings.Contains(err.Error(), "preview skipped before draft") {
		t.Fatalf("error = %v, want skipped preview before draft", err)
	}
}

func TestAlertPreviewSkipBlockingCoversMissingTraceService(t *testing.T) {
	reason := `当前 traces_spanmetrics_latency_bucket 未发现 service_name="checkout"`
	if !alertdraft.ShouldBlockCreateOnPreviewSkip(reason) {
		t.Fatalf("missing trace service skipped reason should block alert creation")
	}
}

func TestApplyAlertRuleConfigAllowsEnvironmentOnlySkippedPreview(t *testing.T) {
	adapter := NewAlertRuleManager(managersvcalert.NewStub())
	caller := aiopstools.ConfigCaller{Role: "admin"}
	draft, err := adapter.DraftAlertRuleConfig(context.Background(), caller, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "trace_latency_checkout",
			Kind:     "trace_latency",
			Name:     "Trace latency checkout",
			Severity: "warning",
			Spec: map[string]interface{}{
				"service":      "checkout",
				"threshold_ms": 750,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	_, err = adapter.ApplyAlertRuleConfig(context.Background(), caller, applyArgsFromDraft(t, draft))
	if !errors.Is(err, errs.ErrNotWiredYet) {
		t.Fatalf("error = %v, want create path to reach service stub", err)
	}
	if strings.Contains(err.Error(), "preview skipped before create") {
		t.Fatalf("error = %v, should not block on environment-only preview skip", err)
	}
}

func TestNormalizeAlertRuleConfigInputBuildsRawPredicateForCollectedMetricName(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Spec: map[string]interface{}{
			"metric":    "custom_app_queue_depth",
			"operator":  ">=",
			"threshold": "100",
			"selector":  `job="worker"`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	expr, _ := got.Spec["expr"].(string)
	if expr != `(custom_app_queue_depth{job="worker"}) >= 100` {
		t.Fatalf("expr = %q, want raw metric predicate", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitSourceIdentityFromCollectedMetricSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":    "custom_app_queue_depth",
			"operator":  ">=",
			"threshold": 100,
			"selector":  `queue="payments",device_id="5",ongrid_source="custom:queue",job="queue-exporter",instance="127.0.0.1:9100"`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if expr != `(custom_app_queue_depth{queue="payments"}) >= 100` {
		t.Fatalf("expr = %q, want only business label selector", expr)
	}
	if got.Spec["selector"] != `queue="payments"` {
		t.Fatalf("selector = %#v, want only business label selector", got.Spec["selector"])
	}
}

func TestNormalizeAlertRuleConfigInputPreservesExplicitCollectedMetricSourceSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":          "custom_app_queue_depth",
			"operator":        ">=",
			"threshold":       100,
			"selector":        `queue="payments",device_id="5",ongrid_source="custom:queue",job="queue-exporter",instance="127.0.0.1:9100"`,
			"source_explicit": true,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, want := range []string{
		`queue="payments"`,
		`device_id="5"`,
		`ongrid_source="custom:queue"`,
		`job="queue-exporter"`,
		`instance="127.0.0.1:9100"`,
	} {
		if !strings.Contains(expr, want) {
			t.Fatalf("expr = %q, want explicit selector part %q", expr, want)
		}
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitSourceIdentityFromCollectedMetricRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `custom_app_queue_depth{queue="payments",device_id="5",ongrid_source="custom:queue",job="queue-exporter",instance="127.0.0.1:9100"} >= 100`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if expr != `custom_app_queue_depth{queue="payments"} >= 100` {
		t.Fatalf("expr = %q, want inline source identity labels stripped", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitSourceIdentityFromArbitraryMetricRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `rate(prometheus_http_requests_total{handler="/api/v1/query",job="prometheus",instance="localhost:9090"}[5m]) > 10`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if expr != `rate(prometheus_http_requests_total{handler="/api/v1/query"}[5m]) > 10` {
		t.Fatalf("expr = %q, want source identity labels stripped from arbitrary metric", expr)
	}
}

func TestNormalizeAlertRuleConfigInputPreservesExplicitSourceIdentityInRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"source_explicit": true,
			"expr":            `rate(prometheus_http_requests_total{handler="/api/v1/query",job="prometheus",instance="localhost:9090"}[5m]) > 10`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if expr != in.Spec["expr"] {
		t.Fatalf("expr = %q, want explicit source identity preserved", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDoesNotInventExprForInvalidMetricName(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"metric":    "not a valid metric()",
			"operator":  ">",
			"threshold": 1,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	if got.Kind != "metric_raw" {
		t.Fatalf("Kind = %q, want metric_raw", got.Kind)
	}
	if expr, ok := got.Spec["expr"].(string); ok && expr != "" {
		t.Fatalf("expr = %q, want no invented PromQL for invalid metric name", expr)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesNaturalLanguageScope(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "any", in: "any", want: "global"},
		{name: "all", in: "all", want: "global"},
		{name: "database", in: "database", want: "global"},
		{name: "device", in: "device", want: "host"},
		{name: "pipeline", in: "pipeline", want: "monitoring_pipeline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
				Kind:      "metric_raw",
				ScopeType: tt.in,
				Spec: map[string]interface{}{
					"expr": "up == 0",
				},
			})
			if got.ScopeType != tt.want {
				t.Fatalf("ScopeType = %q, want %q", got.ScopeType, tt.want)
			}
		})
	}
}

func TestNormalizeAlertRuleConfigInputRewritesMongoConnectionUsageActiveToCurrent(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `(max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="active",device_id="2",ongrid_source="db:mongo-test"}) / (max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="active",device_id="2",ongrid_source="db:mongo-test"}) + max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="available",device_id="2",ongrid_source="db:mongo-test"}))) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, `conn_type="active"`) {
		t.Fatalf("expr = %q, want active connection matcher rewritten", expr)
	}
	if count := strings.Count(expr, `conn_type="current"`); count != 2 {
		t.Fatalf("expr = %q, current matcher count = %d, want 2", expr, count)
	}
	if !strings.Contains(expr, `conn_type="available"`) {
		t.Fatalf("expr = %q, want available matcher preserved", expr)
	}
	if !strings.Contains(expr, `max by (device_id, ongrid_source)`) {
		t.Fatalf("expr = %q, want canonical max-by-source aggregation", expr)
	}
	if strings.Contains(expr, `ongrid_source="db:mongo-test"`) || strings.Contains(expr, `device_id="2"`) {
		t.Fatalf("expr = %q, should drop leaked sample database identity selectors", expr)
	}
}

func TestNormalizeAlertRuleConfigInputPreservesExplicitDatabaseSourceSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"source_explicit": true,
			"selector":        `ongrid_source="db:mongo-test"`,
			"expr":            `(mongodb_ss_connections{conn_type="active"} / (mongodb_ss_connections{conn_type="active"} + mongodb_ss_connections{conn_type="available"})) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if !strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, want explicit database source selector preserved", expr)
	}
	if strings.Contains(expr, `conn_type="active"`) {
		t.Fatalf("expr = %q, want active connection matcher rewritten", expr)
	}
	if strings.Count(expr, `ongrid_source="db:mongo-test"`) != 3 {
		t.Fatalf("expr = %q, want explicit source on numerator, denominator current, and available selectors", expr)
	}
}

func TestNormalizeAlertRuleConfigInputForRequestDropsModelClaimedExplicitDatabaseSource(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"source_explicit": true,
			"selector":        `ongrid_source="db:mongo-test"`,
			"expr":            `(mongodb_ss_connections{conn_type="current"} / (mongodb_ss_connections{conn_type="current"} + mongodb_ss_connections{conn_type="available"})) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInputForRequest(in, "创建 MongoDB 连接使用率超过 80% 且持续 10 分钟的告警")
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, should drop model-claimed source when user did not specify it", expr)
	}
	if _, exists := got.Spec["source_explicit"]; exists {
		t.Fatalf("source_explicit should be removed when request did not specify source: %#v", got.Spec["source_explicit"])
	}
	if _, exists := got.Spec["selector"]; exists {
		t.Fatalf("selector = %#v, should remove implicit database source selector", got.Spec["selector"])
	}
	if count := strings.Count(expr, `max by (device_id, ongrid_source)`); count != 3 {
		t.Fatalf("expr = %q, want per-source MongoDB connection usage expression", expr)
	}
}

func TestNormalizeAlertRuleConfigInputForRequestPreservesUserSpecifiedDatabaseSource(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"source_explicit": true,
			"selector":        `ongrid_source="db:mongo-test"`,
			"expr":            `(mongodb_ss_connections{conn_type="current"} / (mongodb_ss_connections{conn_type="current"} + mongodb_ss_connections{conn_type="available"})) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInputForRequest(in, "针对 source db:mongo-test 创建 MongoDB 连接使用率超过 80% 且持续 10 分钟的告警")
	expr, _ := got.Spec["expr"].(string)
	if !strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, want user-specified database source preserved", expr)
	}
	if got.Spec["source_explicit"] != true {
		t.Fatalf("source_explicit = %#v, want preserved", got.Spec["source_explicit"])
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitDatabaseSourceSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"selector": `ongrid_source="db:mongo-test"`,
			"expr":     `(mongodb_ss_connections{conn_type="active"} / (mongodb_ss_connections{conn_type="active"} + mongodb_ss_connections{conn_type="available"})) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, should drop implicit sample database source selector", expr)
	}
	if _, exists := got.Spec["selector"]; exists {
		t.Fatalf("selector = %#v, should remove implicit sample database source selector", got.Spec["selector"])
	}
	if !strings.Contains(expr, `max by (device_id, ongrid_source)`) {
		t.Fatalf("expr = %q, want per-source grouping after dropping selector", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitMongoSourceFromConnectionUsageExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `(max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="active", ongrid_source="db:mongo-test", service="mongo-test"}) / (max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="active", ongrid_source="db:mongo-test", service="mongo-test"}) + max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="available", ongrid_source="db:mongo-test", service="mongo-test"}))) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, leaked := range []string{`ongrid_source="db:mongo-test"`, `service="mongo-test"`} {
		if strings.Contains(expr, leaked) {
			t.Fatalf("expr = %q, should drop implicit sample MongoDB matcher %s", expr, leaked)
		}
	}
	if strings.Contains(expr, `conn_type="active"`) {
		t.Fatalf("expr = %q, want active matcher rewritten to current", expr)
	}
	if count := strings.Count(expr, `max by (device_id, ongrid_source)`); count != 3 {
		t.Fatalf("expr = %q, max-by-source count = %d, want 3", expr, count)
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitDatabaseServiceSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"db_metric": "mongodb_connection_usage_pct",
			"selector":  `service="mongo-test",ongrid_source="db:mongo-test"`,
			"operator":  ">",
			"threshold": 80,
			"for":       "10m",
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, leaked := range []string{`service="mongo-test"`, `ongrid_source="db:mongo-test"`} {
		if strings.Contains(expr, leaked) {
			t.Fatalf("expr = %q, should drop implicit database identity matcher %s", expr, leaked)
		}
	}
	if _, exists := got.Spec["selector"]; exists {
		t.Fatalf("selector = %#v, should remove implicit database identity selector", got.Spec["selector"])
	}
}

func TestNormalizeAlertRuleConfigInputPreservesExplicitDatabaseServiceSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"source_explicit": true,
			"selector":        `service="mongo-test"`,
			"expr":            `(mongodb_ss_connections{conn_type="active"} / (mongodb_ss_connections{conn_type="active"} + mongodb_ss_connections{conn_type="available"})) * 100 > 80`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if !strings.Contains(expr, `service="mongo-test"`) {
		t.Fatalf("expr = %q, want explicit database service selector preserved", expr)
	}
	if strings.Contains(expr, `conn_type="active"`) {
		t.Fatalf("expr = %q, want active matcher rewritten to current", expr)
	}
}

func TestNormalizeAlertRuleConfigInputDropsImplicitDatabaseSourceFromCatalogSelector(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"db_metric":   "mongodb_connection_usage_pct",
			"selector":    `ongrid_source="db:mongo-test"`,
			"operator":    ">",
			"threshold":   80,
			"window":      "5m",
			"for":         "10m",
			"source_hint": "sample label from catalog",
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, should drop implicit sample database source selector", expr)
	}
	if _, exists := got.Spec["selector"]; exists {
		t.Fatalf("selector = %#v, should remove implicit sample database source selector", got.Spec["selector"])
	}
	if count := strings.Count(expr, `by (device_id, ongrid_source)`); count < 2 {
		t.Fatalf("expr = %q, want per-source grouping for catalog database metric", expr)
	}
}

func TestNormalizeAlertRuleConfigInputKeepsNonIdentitySelectorWhenSourceLeaks(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"db_metric":   "postgresql_active_connections",
			"selector":    `datname="postgres",ongrid_source="db:pg-test",instance="127.0.0.1:9187"`,
			"operator":    ">",
			"threshold":   100,
			"window":      "5m",
			"source_note": "catalog sample",
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, `ongrid_source="db:pg-test"`) || strings.Contains(expr, `instance="127.0.0.1:9187"`) {
		t.Fatalf("expr = %q, should drop leaked identity selectors", expr)
	}
	if !strings.Contains(expr, `datname="postgres"`) {
		t.Fatalf("expr = %q, want non-identity selector preserved", expr)
	}
	if got.Spec["selector"] != `datname="postgres"` {
		t.Fatalf("selector = %#v, want only non-identity selector left", got.Spec["selector"])
	}
}

func TestNormalizeAlertRuleConfigInputDropsLeakedDatabaseSourceFromRawExpr(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `redis_connected_clients{device_id="7",ongrid_source="db:redis-test",job="redis",instance="127.0.0.1:9121"} > 50`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	for _, leaked := range []string{`device_id="7"`, `ongrid_source="db:redis-test"`, `job="redis"`, `instance="127.0.0.1:9121"`} {
		if strings.Contains(expr, leaked) {
			t.Fatalf("expr = %q, should drop leaked sample matcher %s", expr, leaked)
		}
	}
	if expr != `redis_connected_clients > 50` {
		t.Fatalf("expr = %q, want database metric left unscoped", expr)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesMongoConnectionUsageSumToMax(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `(sum by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="current"}) / (sum by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="current"}) + sum by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="available"}))) * 100 > 85`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.Contains(expr, "sum by") {
		t.Fatalf("expr = %q, want gauge connection usage rewritten away from sum by", expr)
	}
	if count := strings.Count(expr, `max by (device_id, ongrid_source)`); count != 3 {
		t.Fatalf("expr = %q, max-by-source count = %d, want 3", expr, count)
	}
	if strings.Contains(expr, `ongrid_source="db:mongo-test"`) {
		t.Fatalf("expr = %q, should not invent a source selector", expr)
	}
	if !strings.HasSuffix(expr, `> 85`) {
		t.Fatalf("expr = %q, want threshold preserved", expr)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesMongoConnectionUsageRatioThreshold(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"expr": `max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="current"}) / (max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="current"}) + max by (device_id, ongrid_source) (mongodb_ss_connections{conn_type="available"})) > 0.8`,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if !strings.Contains(expr, `100 *`) {
		t.Fatalf("expr = %q, want percent expression", expr)
	}
	if strings.HasSuffix(expr, `> 0.8`) || !strings.HasSuffix(expr, `> 80`) {
		t.Fatalf("expr = %q, want ratio threshold normalized to 80 percent", expr)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesPercentCatalogMetricRatioThreshold(t *testing.T) {
	in := aiopstools.AlertRuleConfigInput{
		Kind: "metric_raw",
		Spec: map[string]interface{}{
			"db_metric": "mongodb_connection_usage_pct",
			"operator":  ">",
			"threshold": 0.8,
		},
	}

	got := alertdraft.NormalizeRuleConfigInput(in)
	expr, _ := got.Spec["expr"].(string)
	if strings.HasSuffix(expr, `> 0.8`) || !strings.Contains(expr, `> 80`) {
		t.Fatalf("expr = %q, want percent catalog threshold normalized to 80", expr)
	}
}

func TestDatabaseAlertMetricExprCoversSupportedCatalogMetrics(t *testing.T) {
	catalogMetrics := []string{
		"mysql_up",
		"mysql_connection_usage_pct",
		"mysql_threads_running",
		"mysql_qps",
		"mysql_slow_queries_15m",
		"mysql_aborted_connects_15m",
		"mysql_innodb_buffer_pool_hit_pct",
		"mysql_row_lock_waits_15m",
		"mysql_open_files_usage_pct",
		"mysql_temp_disk_tables_15m",
		"postgresql_up",
		"postgresql_connection_usage_pct",
		"postgresql_active_connections",
		"postgresql_deadlocks_15m",
		"postgresql_cache_hit_ratio_pct",
		"postgresql_temp_bytes_15m",
		"postgresql_replication_lag_seconds",
		"postgresql_long_transaction_seconds",
		"postgresql_locks_count",
		"postgresql_database_size_bytes",
		"postgresql_tps",
		"redis_up",
		"redis_memory_usage_pct",
		"redis_client_usage_pct",
		"redis_connected_clients",
		"redis_ops_per_second",
		"redis_keyspace_hit_ratio_pct",
		"redis_evicted_keys_15m",
		"redis_rejected_connections_15m",
		"redis_blocked_clients",
		"redis_slowlog_length",
		"redis_latency_usec",
		"redis_key_count",
		"mongodb_up",
		"mongodb_connections_current",
		"mongodb_connections_active",
		"mongodb_connection_usage_pct",
		"mongodb_connections_available",
		"mongodb_global_lock_queue",
		"mongodb_operations_per_second",
		"mongodb_asserts_15m",
		"mongodb_page_faults_15m",
		"mongodb_resident_memory_bytes",
		"mongodb_wiredtiger_cache_usage_pct",
		"mongodb_wiredtiger_dirty_cache_pct",
		"mongodb_operation_latency_ms",
		"mongodb_collection_scans_15m",
		"mongodb_sort_spills_15m",
	}

	for _, metric := range catalogMetrics {
		t.Run(metric, func(t *testing.T) {
			expr, ok := alertdraft.DatabaseMetricExpr(metric, `device_id="db-1"`)
			if !ok {
				t.Fatalf("alertdraft.DatabaseMetricExpr(%q) ok = false, want true", metric)
			}
			if !strings.Contains(expr, `device_id="db-1"`) {
				t.Fatalf("expr = %q, want selector propagated", expr)
			}
		})
	}
}

func TestDatabaseAlertMetricExprGroupsEveryUnscopedCatalogMetricBySource(t *testing.T) {
	catalogMetrics := []string{
		"mysql_up",
		"mysql_connection_usage_pct",
		"mysql_threads_running",
		"mysql_qps",
		"mysql_slow_queries_15m",
		"mysql_aborted_connects_15m",
		"mysql_innodb_buffer_pool_hit_pct",
		"mysql_row_lock_waits_15m",
		"mysql_open_files_usage_pct",
		"mysql_temp_disk_tables_15m",
		"postgresql_up",
		"postgresql_connection_usage_pct",
		"postgresql_active_connections",
		"postgresql_deadlocks_15m",
		"postgresql_cache_hit_ratio_pct",
		"postgresql_temp_bytes_15m",
		"postgresql_replication_lag_seconds",
		"postgresql_long_transaction_seconds",
		"postgresql_locks_count",
		"postgresql_database_size_bytes",
		"postgresql_tps",
		"redis_up",
		"redis_memory_usage_pct",
		"redis_client_usage_pct",
		"redis_connected_clients",
		"redis_ops_per_second",
		"redis_keyspace_hit_ratio_pct",
		"redis_evicted_keys_15m",
		"redis_rejected_connections_15m",
		"redis_blocked_clients",
		"redis_slowlog_length",
		"redis_latency_usec",
		"redis_key_count",
		"mongodb_up",
		"mongodb_connections_current",
		"mongodb_connections_active",
		"mongodb_connection_usage_pct",
		"mongodb_connections_available",
		"mongodb_global_lock_queue",
		"mongodb_operations_per_second",
		"mongodb_asserts_15m",
		"mongodb_page_faults_15m",
		"mongodb_resident_memory_bytes",
		"mongodb_wiredtiger_cache_usage_pct",
		"mongodb_wiredtiger_dirty_cache_pct",
		"mongodb_operation_latency_ms",
		"mongodb_collection_scans_15m",
		"mongodb_sort_spills_15m",
	}

	for _, metric := range catalogMetrics {
		t.Run(metric, func(t *testing.T) {
			expr, ok := alertdraft.DatabaseMetricExpr(metric, "")
			if !ok {
				t.Fatalf("alertdraft.DatabaseMetricExpr(%q) ok = false, want true", metric)
			}
			if !strings.Contains(expr, "by (device_id, ongrid_source)") {
				t.Fatalf("expr = %q, want per-source grouping for unscoped database alert", expr)
			}
		})
	}
}

func TestDatabaseAlertMetricExprCoversAnalyzerFallbackMetrics(t *testing.T) {
	tests := []struct {
		name string
		want []string
	}{
		{name: "redis_ops_per_second", want: []string{"redis_commands_processed_total", "redis_commands_total"}},
		{name: "redis_key_count", want: []string{"redis_db_keys", "redis_keys_count"}},
		{name: "postgresql_connection_usage_pct", want: []string{"pg_stat_database_numbackends", "pg_stat_activity_count", "pg_settings_max_connections", "by (device_id, ongrid_source)"}},
		{name: "mongodb_connections_current", want: []string{"mongodb_ss_connections", "conn_type=\"current\"", "mongodb_connections", "state=\"current\""}},
		{name: "mongodb_connection_usage_pct", want: []string{"conn_type=\"current\"", "conn_type=\"available\"", "by (device_id, ongrid_source)"}},
		{name: "mongodb_global_lock_queue", want: []string{"mongodb_ss_globalLock_currentQueue", "count_type=\"total\"", "by (device_id, ongrid_source)"}},
		{name: "mongodb_operations_per_second", want: []string{"mongodb_ss_opcounters", "mongodb_op_counters_total"}},
		{name: "mongodb_asserts_15m", want: []string{"mongodb_ss_asserts", "mongodb_asserts_total"}},
		{name: "mongodb_page_faults_15m", want: []string{"mongodb_ss_extra_info_page_faults", "mongodb_ss_extra_info_page_faults_total"}},
		{name: "mongodb_resident_memory_bytes", want: []string{"mongodb_ss_mem_resident", "mongodb_mongod_mem_resident_megabytes"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expr, ok := alertdraft.DatabaseMetricExpr(tt.name, `device_id="db-1"`)
			if !ok {
				t.Fatalf("alertdraft.DatabaseMetricExpr(%q) ok = false, want true", tt.name)
			}
			for _, want := range tt.want {
				if !strings.Contains(expr, want) {
					t.Fatalf("expr = %q, want to contain %q", expr, want)
				}
			}
		})
	}
}

func TestNormalizeAlertRuleConfigInputDefaultsAllSupportedKinds(t *testing.T) {
	tests := []struct {
		name   string
		in     aiopstools.AlertRuleConfigInput
		assert func(t *testing.T, got aiopstools.AlertRuleConfigInput)
	}{
		{
			name: "metric_anomaly",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "anomaly",
				Spec: map[string]interface{}{"metric": "memory"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "metric_anomaly" || got.Spec["metric"] != "mem_pct" || got.Spec["method"] != "zscore" || got.Spec["baseline_window"] != "1h" {
					t.Fatalf("got = %#v, want canonical metric_anomaly defaults", got)
				}
			},
		},
		{
			name: "metric_forecast",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "forecast",
				Spec: map[string]interface{}{"metric": "disk_available"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "metric_forecast" || got.Spec["metric"] != "disk_avail_bytes" || got.Spec["fit_window"] != "1h" || got.Spec["operator"] != "<=" {
					t.Fatalf("got = %#v, want metric_forecast defaults", got)
				}
				if got.Spec["predict_seconds"] != float64(21600) {
					t.Fatalf("predict_seconds = %#v, want 21600", got.Spec["predict_seconds"])
				}
			},
		},
		{
			name: "metric_burn_rate",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "burn_rate",
				Spec: map[string]interface{}{"sli": `sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))`},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				burns, ok := got.Spec["burns"].([]interface{})
				if got.Kind != "metric_burn_rate" || got.Spec["slo"] != float64(99.9) || !ok || len(burns) != 2 {
					t.Fatalf("got = %#v, want metric_burn_rate defaults", got)
				}
			},
		},
		{
			name: "log_match",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "log",
				Spec: map[string]interface{}{"pattern": "(?i)error|panic"},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "log_match" || got.ScopeType != "global" || got.Spec["stream_selector"] != alertdraft.DefaultJournaldLogSelector || got.Spec["line_filter"] != "(?i)error|panic" || got.Spec["operator"] != ">=" || got.Spec["threshold"] != float64(1) {
					t.Fatalf("got = %#v, want log_match defaults", got)
				}
			},
		},
		{
			name: "log_volume",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "log_volume",
				Spec: map[string]interface{}{"operator": ">", "threshold": 3},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.ScopeType != "global" || got.Spec["ratio_op"] != ">" || got.Spec["ratio_threshold"] != float64(3) {
					t.Fatalf("got = %#v, want log_volume ratio aliases", got)
				}
			},
		},
		{
			name: "trace_latency",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "latency",
				Spec: map[string]interface{}{"service": "checkout", "threshold": 750},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "trace_latency" || got.Spec["threshold_ms"] != float64(750) || got.Spec["quantile"] != "p95" {
					t.Fatalf("got = %#v, want trace_latency defaults", got)
				}
			},
		},
		{
			name: "trace_error_rate",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "error_rate",
				Spec: map[string]interface{}{"service": "checkout", "threshold": 2.5},
			},
			assert: func(t *testing.T, got aiopstools.AlertRuleConfigInput) {
				if got.Kind != "trace_error_rate" || got.Spec["threshold_pct"] != 2.5 || got.Spec["operator"] != ">=" {
					t.Fatalf("got = %#v, want trace_error_rate defaults", got)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := alertdraft.NormalizeRuleConfigInput(tt.in)
			tt.assert(t, got)
			if got.RuleKey == "" {
				t.Fatalf("RuleKey should be defaulted")
			}
			if got.Name == "" {
				t.Fatalf("Name should be defaulted")
			}
			if got.RunbookURL == "" {
				t.Fatalf("RunbookURL should be defaulted")
			}
		})
	}
}

func TestNormalizeAlertRuleConfigInputCanonicalizesClosedSetPromQLAliases(t *testing.T) {
	tests := []struct {
		name       string
		in         aiopstools.AlertRuleConfigInput
		wantMetric string
	}{
		{
			name: "anomaly cpu promql",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "metric_anomaly",
				Spec: map[string]interface{}{
					"metric": `100 - (avg by (device_id)(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)`,
				},
			},
			wantMetric: "cpu_pct",
		},
		{
			name: "forecast filesystem avail",
			in: aiopstools.AlertRuleConfigInput{
				Kind: "metric_forecast",
				Spec: map[string]interface{}{
					"metric": "node_filesystem_avail_bytes",
				},
			},
			wantMetric: "disk_avail_bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := alertdraft.NormalizeRuleConfigInput(tt.in)
			if got.Spec["metric"] != tt.wantMetric {
				t.Fatalf("metric = %#v, want %q", got.Spec["metric"], tt.wantMetric)
			}
		})
	}
}

func TestNormalizeAlertRuleConfigInputRewritesFilesystemAvailablePercentForecastExpr(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "metric_forecast",
		Spec: map[string]interface{}{
			"expr":            `(node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}) * 100`,
			"fit_window":      "7d",
			"predict_seconds": 86400,
			"operator":        "lt",
			"threshold":       15,
		},
	})
	if got.Spec["metric"] != "disk_used_pct" {
		t.Fatalf("metric = %#v, want disk_used_pct", got.Spec["metric"])
	}
	if got.Spec["operator"] != ">" {
		t.Fatalf("operator = %#v, want >", got.Spec["operator"])
	}
	if got.Spec["threshold"] != float64(85) {
		t.Fatalf("threshold = %#v, want 85", got.Spec["threshold"])
	}
	if got.Spec["selector"] != `mountpoint="/"` {
		t.Fatalf("selector = %#v, want root mountpoint", got.Spec["selector"])
	}
	if _, exists := got.Spec["expr"]; exists {
		t.Fatalf("expr should be removed after closed-set forecast rewrite: %#v", got.Spec)
	}
}

func TestNormalizeAlertRuleConfigInputForRequestRewritesFilesystemAvailablePercentForecastMetric(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInputForRequest(aiopstools.AlertRuleConfigInput{
		Kind: "metric_forecast",
		Spec: map[string]interface{}{
			"metric":          "node_filesystem_avail_bytes",
			"fit_window":      "7d",
			"predict_seconds": 86400,
			"operator":        "lt",
			"threshold":       15,
		},
	}, "根分区 / 的磁盘可用空间预测 24 小时内会低于 15%")
	if got.Spec["metric"] != "disk_used_pct" {
		t.Fatalf("metric = %#v, want disk_used_pct", got.Spec["metric"])
	}
	if got.Spec["operator"] != ">" {
		t.Fatalf("operator = %#v, want >", got.Spec["operator"])
	}
	if got.Spec["threshold"] != float64(85) {
		t.Fatalf("threshold = %#v, want 85", got.Spec["threshold"])
	}
	if got.Spec["selector"] != `mountpoint="/"` {
		t.Fatalf("selector = %#v, want root mountpoint", got.Spec["selector"])
	}
}

func TestNormalizeAlertRuleConfigInputRewritesGuessedJournaldJobSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{job=~".*journal.*"}`,
			"line_filter":     "ERROR|panic|OOM",
		},
	})
	if got.Spec["stream_selector"] != alertdraft.DefaultJournaldLogSelector {
		t.Fatalf("stream_selector = %#v, want %s", got.Spec["stream_selector"], alertdraft.DefaultJournaldLogSelector)
	}
}

func TestNormalizeAlertRuleConfigInputCoercesLogMonitoringPipelineScopeToGlobal(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind:      "log_match",
		ScopeType: "monitoring_pipeline",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source=~"journald(:.*)?"}`,
			"line_filter":     "ERROR|panic",
		},
	})
	if got.ScopeType != "global" {
		t.Fatalf("ScopeType = %q, want global", got.ScopeType)
	}
}

func TestNormalizeAlertRuleConfigInputRewritesGuessedJournaldJobSelectorAndKeepsKnownLabels(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{job=~".*journal.*",level="6",unit="ongrid.service",app="guessed"}`,
			"line_filter":     "error",
		},
	})
	want := `{ongrid_source=~"journald(:.*)?",level="6",unit="ongrid.service"}`
	if got.Spec["stream_selector"] != want {
		t.Fatalf("stream_selector = %#v, want %s", got.Spec["stream_selector"], want)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesLogOperatorAndLineFilter(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source=~"journald(:.*)?"}`,
			"line_filter":     `|~ "(?i)(ERROR|panic)"`,
			"operator":        "gte",
			"threshold":       3,
		},
	})
	if got.Spec["operator"] != ">=" {
		t.Fatalf("operator = %#v, want >=", got.Spec["operator"])
	}
	if got.Spec["line_filter"] != `(?i)(ERROR|panic)` {
		t.Fatalf("line_filter = %#v, want pure regex", got.Spec["line_filter"])
	}
}

func TestNormalizeAlertRuleConfigInputMovesLogLabelFilterChainIntoSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source="journald"}`,
			"line_filter":     `|~ "(?i)level=6" |~ "(?i)(error|panic)"`,
			"operator":        ">=",
			"threshold":       3,
		},
	})
	if got.Spec["stream_selector"] != `{ongrid_source="journald",level="6"}` {
		t.Fatalf("stream_selector = %#v, want level matcher moved into selector", got.Spec["stream_selector"])
	}
	if got.Spec["line_filter"] != `(?i)(error|panic)` {
		t.Fatalf("line_filter = %#v, want only content regex", got.Spec["line_filter"])
	}
}

func TestNormalizeAlertRuleConfigInputForRequestMovesExplicitLogLabelIntoSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInputForRequest(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source="journald"}`,
			"line_filter":     `|~ "(?i)(ERROR|panic)"`,
			"operator":        "gte",
			"threshold":       3,
		},
	}, "系统 journald 日志里 level=6 的 ERROR 或 panic 在 5 分钟内出现 >= 3 次就告警")
	if got.Spec["stream_selector"] != `{ongrid_source="journald",level="6"}` {
		t.Fatalf("stream_selector = %#v, want level matcher from user request", got.Spec["stream_selector"])
	}
	if got.Spec["line_filter"] != `(?i)(ERROR|panic)` {
		t.Fatalf("line_filter = %#v, want pure content regex", got.Spec["line_filter"])
	}
	if got.Spec["operator"] != ">=" {
		t.Fatalf("operator = %#v, want >=", got.Spec["operator"])
	}
}

func TestNormalizeAlertRuleConfigInputMapsJournaldPriorityAndDropsUnknownLogLabels(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInputForRequest(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source="journald",PRIORITY="6",app="guessed"}`,
			"line_filter":     `ERROR|panic`,
			"operator":        ">=",
			"threshold":       3,
		},
	}, "系统 journald 日志里 level=6 的 ERROR 或 panic")
	if got.Spec["stream_selector"] != `{ongrid_source="journald",level="6"}` {
		t.Fatalf("stream_selector = %#v, want priority mapped and unknown label dropped", got.Spec["stream_selector"])
	}
}

func TestNormalizeAlertRuleConfigInputMovesLogLabelPrefixRegexIntoSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInputForRequest(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source="journald"}`,
			"line_filter":     `level=6.*(ERROR|panic)`,
			"operator":        ">=",
			"threshold":       3,
		},
	}, "")
	if got.Spec["stream_selector"] != `{ongrid_source="journald",level="6"}` {
		t.Fatalf("stream_selector = %#v, want level matcher moved into selector", got.Spec["stream_selector"])
	}
	if got.Spec["line_filter"] != `(ERROR|panic)` {
		t.Fatalf("line_filter = %#v, want only content regex", got.Spec["line_filter"])
	}
}

func TestNormalizeAlertRuleConfigInputMovesLogLabelAlternationRegexIntoSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInputForRequest(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{ongrid_source=~"journald(:.*)?"}`,
			"line_filter":     `(?i)(level|priority)[=:]6.*(error|panic)`,
			"operator":        "gte",
			"threshold":       3,
		},
	}, "")
	if got.Spec["stream_selector"] != `{ongrid_source=~"journald(:.*)?",level="6"}` {
		t.Fatalf("stream_selector = %#v, want level matcher moved into selector", got.Spec["stream_selector"])
	}
	if got.Spec["line_filter"] != `(error|panic)` {
		t.Fatalf("line_filter = %#v, want only content regex", got.Spec["line_filter"])
	}
}

func TestNormalizeAlertRuleConfigInputRewritesGuessedJournaldJobSelectorForLogVolume(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_volume",
		Spec: map[string]interface{}{
			"stream_selector": `{job=~".*journal.*",level!="7"}`,
			"ratio_threshold": 3,
		},
	})
	want := `{ongrid_source=~"journald(:.*)?",level!="7"}`
	if got.Spec["stream_selector"] != want {
		t.Fatalf("stream_selector = %#v, want %s", got.Spec["stream_selector"], want)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesLogVolumeLineFilter(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_volume",
		Spec: map[string]interface{}{
			"stream_selector": `{job=~".*journal.*"}`,
			"line_filter":     `|~ "(?i)level=6" |~ "(?i)(error|failed)"`,
			"ratio_op":        "gte",
			"ratio_threshold": 3,
			"window":          "5m",
		},
	})
	if got.Spec["stream_selector"] != `{ongrid_source=~"journald(:.*)?",level="6"}` {
		t.Fatalf("stream_selector = %#v, want level moved into selector", got.Spec["stream_selector"])
	}
	if got.Spec["line_filter"] != `(?i)(error|failed)` {
		t.Fatalf("line_filter = %#v, want pure regex", got.Spec["line_filter"])
	}
	if got.Spec["ratio_op"] != ">=" {
		t.Fatalf("ratio_op = %#v, want >=", got.Spec["ratio_op"])
	}
}

func TestNormalizeAlertRuleConfigInputPreservesExplicitLogSelector(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "log_match",
		Spec: map[string]interface{}{
			"stream_selector": `{unit="nginx.service"}`,
			"line_filter":     "error",
		},
	})
	if got.Spec["stream_selector"] != `{unit="nginx.service"}` {
		t.Fatalf("stream_selector = %#v, want explicit selector preserved", got.Spec["stream_selector"])
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesBurnRateFixedRangeToWindow(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "metric_burn_rate",
		Spec: map[string]interface{}{
			"sli": `sum(rate(http_requests_total{code!~"5.."}[5m])) / sum(rate(http_requests_total[5m]))`,
		},
	})
	sli, _ := got.Spec["sli"].(string)
	if strings.Contains(sli, "[5m]") || strings.Count(sli, "[$window]") != 2 {
		t.Fatalf("sli = %q, want fixed ranges normalized to $window", sli)
	}
}

func TestNormalizeAlertRuleConfigInputNormalizesBurnRateRatioSLOToPercent(t *testing.T) {
	got := alertdraft.NormalizeRuleConfigInput(aiopstools.AlertRuleConfigInput{
		Kind: "metric_burn_rate",
		Spec: map[string]interface{}{
			"sli": `sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))`,
			"slo": 0.999,
		},
	})
	if got.Spec["slo"] != float64(99.9) {
		t.Fatalf("slo = %#v, want 99.9 percent", got.Spec["slo"])
	}
}

func TestDraftAlertRuleConfigRejectsBurnRateWithoutWindowedSLI(t *testing.T) {
	adapter := NewAlertRuleManager(managersvcalert.NewStub())
	_, err := adapter.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "burn_rate_no_window",
			Kind:     "metric_burn_rate",
			Name:     "Burn rate no window",
			Severity: "critical",
			Spec: map[string]interface{}{
				"sli": "http_success_ratio",
				"slo": 99.9,
				"burns": []interface{}{
					map[string]interface{}{"window": "1h", "multiplier": 14.4},
				},
			},
		},
	})
	if err == nil {
		t.Fatalf("expected missing $window SLI to be rejected")
	}
	if !strings.Contains(err.Error(), "$window") {
		t.Fatalf("error = %v, want $window guidance", err)
	}
}
