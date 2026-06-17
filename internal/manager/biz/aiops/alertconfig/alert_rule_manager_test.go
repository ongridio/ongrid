package alertconfig

import (
	"context"
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

func validationIssueContains(v aiopstools.ConfigValidationResult, code string) bool {
	for _, issue := range v.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
