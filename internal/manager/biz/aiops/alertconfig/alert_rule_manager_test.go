package alertconfig

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
)

type fakeAlertRulePort struct {
	preview *PreviewResult
}

func (f fakeAlertRulePort) PreviewRule(context.Context, aiopstools.ConfigCaller, RuleInput, int) (*PreviewResult, error) {
	return f.preview, nil
}

func (f fakeAlertRulePort) CreateRule(context.Context, aiopstools.ConfigCaller, RuleInput) (*Rule, error) {
	return &Rule{ID: 1, Kind: "metric_raw", Name: "test"}, nil
}

type mutableFakeAlertRulePort struct {
	preview     *PreviewResult
	createCalls int
}

func (f *mutableFakeAlertRulePort) PreviewRule(context.Context, aiopstools.ConfigCaller, RuleInput, int) (*PreviewResult, error) {
	return f.preview, nil
}

func (f *mutableFakeAlertRulePort) CreateRule(context.Context, aiopstools.ConfigCaller, RuleInput) (*Rule, error) {
	f.createCalls++
	return &Rule{ID: 1, Kind: "metric_raw", Name: "test"}, nil
}

func TestCompactAlertPreviewLimitsLongSeriesAndSamples(t *testing.T) {
	now := time.Now()
	in := &PreviewResult{}
	for i := 0; i < 200; i++ {
		in.Series = append(in.Series, PreviewSeriesPoint{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Value:     float64(i),
		})
		in.Samples = append(in.Samples, PreviewSample{
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

func TestDraftAlertRuleConfigReturnsValidationFailedForBlockingPreview(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{
		preview: &PreviewResult{SkippedReason: "service 为空"},
	})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
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
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindValidationFailed {
		t.Fatalf("Kind = %q, want validation failed", got.Kind)
	}
	if got.DraftHash != "" || len(got.Payload) != 0 || got.ApplyTool != "" {
		t.Fatalf("validation failed result must not be confirmable: hash=%q payload=%s apply=%q", got.DraftHash, string(got.Payload), got.ApplyTool)
	}
	if got.Validation == nil || got.Validation.Status != "failed" {
		t.Fatalf("Validation = %#v, want failed", got.Validation)
	}
}

func TestDraftAlertRuleConfigReturnsValidationFailedForSuspiciousMetricMagnitude(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{
		preview: &PreviewResult{
			FireCount: 1,
			Samples: []PreviewSample{{
				Value:   133783200,
				Summary: "redis memory usage",
			}},
		},
	})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "redis_memory_usage_high",
			Kind:     "metric_raw",
			Name:     "Redis memory usage high",
			Severity: "warning",
			Spec: map[string]interface{}{
				"expr": "(100 * redis_memory_used_bytes / clamp_min(redis_config_maxmemory, 1)) > 85",
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindValidationFailed {
		t.Fatalf("Kind = %q, want validation failed", got.Kind)
	}
	if got.Validation == nil || !validationIssueContains(*got.Validation, "metric_raw_suspicious_magnitude") {
		t.Fatalf("Validation = %#v, want suspicious magnitude issue", got.Validation)
	}
}

func TestDraftAlertRuleConfigReturnsValidationFailedForMetricRawWithoutPredicate(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{
		preview: &PreviewResult{FireCount: 1441},
	})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "mysql_slow_queries",
			Kind:     "metric_raw",
			Name:     "MySQL slow queries",
			Severity: "warning",
			Spec: map[string]interface{}{
				"expr": `rate(mysql_global_status_slow_queries{job!="test"}[5m])`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindValidationFailed {
		t.Fatalf("Kind = %q, want validation failed", got.Kind)
	}
	if got.DraftHash != "" || len(got.Payload) != 0 || got.ApplyTool != "" {
		t.Fatalf("validation failed result must not be confirmable: hash=%q payload=%s apply=%q", got.DraftHash, string(got.Payload), got.ApplyTool)
	}
	if got.Validation == nil || !validationIssueContains(*got.Validation, "metric_raw_predicate_missing") {
		t.Fatalf("Validation = %#v, want predicate missing issue", got.Validation)
	}
}

func TestDraftAlertRuleConfigReturnsValidationFailedForHostPreviewWithoutDeviceID(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{
		preview: &PreviewResult{
			FireCount: 1,
			Samples: []PreviewSample{{
				Labels: map[string]string{"job": "node"},
				Value:  1,
			}},
		},
	})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:   "host_rule_without_device",
			Kind:      "metric_raw",
			Name:      "Host rule without device",
			ScopeType: "host",
			Severity:  "warning",
			Spec: map[string]interface{}{
				"expr": `up > 0`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindValidationFailed {
		t.Fatalf("Kind = %q, want validation failed", got.Kind)
	}
	if got.Validation == nil || !validationIssueContains(*got.Validation, "host_preview_missing_device_id") {
		t.Fatalf("Validation = %#v, want host device_id issue", got.Validation)
	}
}

func TestDraftAlertRuleConfigReturnsValidationFailedForIgnoredLogQuery(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{preview: &PreviewResult{}})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "log_query_ignored",
			Kind:     "log_match",
			Name:     "Log query ignored",
			Severity: "warning",
			Spec: map[string]interface{}{
				"query": `sum by (unit) (count_over_time({ongrid_source="journald"} |~ "(?i)error" [5m])) >= 1`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindValidationFailed {
		t.Fatalf("Kind = %q, want validation failed", got.Kind)
	}
	if got.Validation == nil || !validationIssueContains(*got.Validation, "log_query_not_normalized") {
		t.Fatalf("Validation = %#v, want log query issue", got.Validation)
	}
}

func TestApplyAlertRuleConfigRevalidatesBeforeCreate(t *testing.T) {
	port := &mutableFakeAlertRulePort{preview: &PreviewResult{}}
	manager := NewAlertRuleManager(port)
	draft, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{UserID: 1}, aiopstools.AlertRuleConfigArgs{
		Action: "create",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:   "host_rule_without_device",
			Kind:      "metric_raw",
			Name:      "Host rule without device",
			ScopeType: "host",
			Severity:  "warning",
			Spec: map[string]interface{}{
				"expr": `up > 0`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	var payload struct {
		DraftID string                          `json:"draft_id"`
		Rule    aiopstools.AlertRuleConfigInput `json:"rule"`
	}
	if err := json.Unmarshal(draft.Payload, &payload); err != nil {
		t.Fatalf("decode draft payload: %v", err)
	}

	port.preview = &PreviewResult{
		FireCount: 1,
		Samples: []PreviewSample{{
			Labels: map[string]string{"job": "node"},
			Value:  1,
		}},
	}
	_, err = manager.ApplyAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{UserID: 1}, aiopstools.AlertRuleApplyArgs{
		Action:    "create",
		Rule:      payload.Rule,
		DraftID:   payload.DraftID,
		DraftHash: draft.DraftHash,
		Confirmed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "host_preview_missing_device_id") {
		t.Fatalf("ApplyAlertRuleConfig() error = %v, want host device_id validation", err)
	}
	if port.createCalls != 0 {
		t.Fatalf("CreateRule calls = %d, want 0", port.createCalls)
	}
}

func TestDraftAlertRuleConfigAllowsWarningValidationWithDraftHash(t *testing.T) {
	now := time.Now()
	manager := NewAlertRuleManager(fakeAlertRulePort{preview: &PreviewResult{
		Series: []PreviewSeriesPoint{{Timestamp: now, Value: 20}},
	}})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action:      "create",
		RequestText: "如果 CPU 持续偏高就告警",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "cpu_high",
			Kind:     "metric_raw",
			Name:     "CPU sustained high",
			Severity: "warning",
			Spec: map[string]interface{}{
				"expr": `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 80`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindDraft || got.DraftHash == "" {
		t.Fatalf("got kind=%q hash=%q, want confirmable draft", got.Kind, got.DraftHash)
	}
	if got.Validation == nil || got.Validation.Status != "warning" {
		t.Fatalf("Validation = %#v, want warning", got.Validation)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "持续") {
		t.Fatalf("Warnings = %#v, want sustained warning", got.Warnings)
	}
}

func TestDraftAlertRuleConfigIncludesScopeConfirmation(t *testing.T) {
	manager := NewAlertRuleManager(fakeAlertRulePort{preview: &PreviewResult{
		FireCount: 1,
		Samples: []PreviewSample{{
			Labels: map[string]string{"device_id": "2"},
			Value:  91,
		}},
	}})

	got, err := manager.DraftAlertRuleConfig(context.Background(), aiopstools.ConfigCaller{}, aiopstools.AlertRuleConfigArgs{
		Action:      "create",
		RequestText: "创建 PostgreSQL 连接使用率过高告警",
		Rule: aiopstools.AlertRuleConfigInput{
			RuleKey:  "pg_connection_usage_high",
			Kind:     "metric_raw",
			Name:     "PostgreSQL connection usage high",
			Severity: "warning",
			Spec: map[string]interface{}{
				"expr": `100 * sum by (device_id, ongrid_source) (pg_stat_database_numbackends) / max by (device_id, ongrid_source) (pg_settings_max_connections) > 85`,
			},
		},
	})
	if err != nil {
		t.Fatalf("DraftAlertRuleConfig() error = %v", err)
	}
	if got.Kind != aiopstools.ConfigResultKindDraft {
		t.Fatalf("Kind = %q, want draft", got.Kind)
	}
	if got.Scope == nil || got.Scope.Type != "host" || got.Scope.Label != "主机级" {
		t.Fatalf("Scope = %#v, want host scope summary", got.Scope)
	}
	if !strings.Contains(got.ConfirmationPrompt, "当前告警范围：主机级") || !strings.Contains(got.ConfirmationPrompt, "改成全局") {
		t.Fatalf("ConfirmationPrompt = %q, want host scope confirmation text", got.ConfirmationPrompt)
	}
}

func TestValidateAlertRuleDraftWarnsForGlobalHostScopedRule(t *testing.T) {
	validation := validateAlertRuleDraft(aiopstools.AlertRuleConfigInput{
		RuleKey:   "cpu_high",
		Kind:      "metric_raw",
		Name:      "CPU high",
		ScopeType: "global",
		Severity:  "warning",
		Spec: map[string]interface{}{
			"expr": `100 * (1 - avg by (device_id) (rate(node_cpu_seconds_total{mode="idle"}[5m]))) > 80`,
		},
	}, "创建 CPU 使用率过高告警", &PreviewResult{
		FireCount: 1,
		Samples: []PreviewSample{{
			Labels: map[string]string{"device_id": "2"},
			Value:  91,
		}},
	})

	if validation.Status != "warning" || !validationIssueContains(validation, "host_scope_recommended") {
		t.Fatalf("Validation = %#v, want host_scope_recommended warning", validation)
	}
}

func TestValidateAlertRuleDraftWarnsForGlobalDatabaseRuleWithDeviceID(t *testing.T) {
	validation := validateAlertRuleDraft(aiopstools.AlertRuleConfigInput{
		RuleKey:   "pg_connection_usage_high",
		Kind:      "metric_raw",
		Name:      "PostgreSQL connection usage high",
		ScopeType: "global",
		Severity:  "warning",
		Spec: map[string]interface{}{
			"expr": `100 * sum by (device_id, ongrid_source) (pg_stat_database_numbackends) / max by (device_id, ongrid_source) (pg_settings_max_connections) > 85`,
		},
	}, "创建 PostgreSQL 连接使用率过高告警", &PreviewResult{
		FireCount: 1,
		Samples: []PreviewSample{{
			Labels: map[string]string{"device_id": "2", "ongrid_source": "db:postgres"},
			Value:  91,
		}},
	})

	if validation.Status != "warning" || !validationIssueContains(validation, "host_scope_recommended") {
		t.Fatalf("Validation = %#v, want host_scope_recommended warning", validation)
	}
}

func TestHasMetricRawComparisonPredicateIgnoresLabelMatchers(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want bool
	}{
		{
			name: "bare_metric_with_not_equal_matcher",
			expr: `rate(mysql_global_status_slow_queries{job!="test"}[5m])`,
			want: false,
		},
		{
			name: "simple_comparison",
			expr: `rate(mysql_global_status_slow_queries{job!="test"}[5m]) > 0.5`,
			want: true,
		},
		{
			name: "compound_comparison",
			expr: `up{job="node"} == 0 or process_resident_memory_bytes > 1024`,
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasMetricRawComparisonPredicate(tc.expr); got != tc.want {
				t.Fatalf("hasMetricRawComparisonPredicate(%q) = %v, want %v", tc.expr, got, tc.want)
			}
		})
	}
}

func validationIssueContains(v aiopstools.ConfigValidationResult, code string) bool {
	for _, issue := range v.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
