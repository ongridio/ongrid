package alert

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestCreateRuleLeavesUserRuleSourceCustom(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)
	createdBy := uint64(42)

	row, err := uc.CreateRule(context.Background(), RuleInput{
		RuleKey:   "custom_disk_pressure",
		Kind:      model.RuleKindMetricRaw,
		Name:      "Custom Disk Pressure",
		ScopeType: model.RuleScopeHost,
		JoinMode:  model.RuleJoinModeAll,
		Severity:  "warning",
		Enabled:   true,
		Spec:      map[string]any{"expr": `disk_used_pct{mountpoint="/"} > 88`},
	}, &createdBy)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if row.SourceType == model.RuleSourceBuiltin {
		t.Fatalf("SourceType = %q, want custom source", row.SourceType)
	}
	if row.SourceType != "" {
		t.Fatalf("SourceType = %q, want empty custom source", row.SourceType)
	}

	got, err := uc.GetRule(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("GetRule: %v", err)
	}
	if got.SourceType != "" {
		t.Fatalf("persisted SourceType = %q, want empty custom source", got.SourceType)
	}
}

func TestCreateRulePreservesMetricForecastSelector(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)

	row, err := uc.CreateRule(context.Background(), RuleInput{
		RuleKey:   "forecast_root_disk_free",
		Kind:      model.RuleKindMetricForecast,
		Name:      "Forecast Root Disk Free",
		ScopeType: model.RuleScopeHost,
		JoinMode:  model.RuleJoinModeAll,
		Severity:  "warning",
		Enabled:   true,
		Spec: map[string]any{
			"metric":          "disk_avail_bytes",
			"selector":        `{mountpoint="/",fstype!="tmpfs"}`,
			"fit_window":      "7d",
			"predict_seconds": 21600,
			"operator":        "<",
			"threshold":       float64(10 * 1024 * 1024 * 1024),
		},
	}, nil)
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	var got struct {
		Selector string `json:"selector"`
	}
	if err := json.Unmarshal([]byte(row.ConditionsJSON), &got); err != nil {
		t.Fatalf("decode conditions_json: %v", err)
	}
	if got.Selector != `{mountpoint="/",fstype!="tmpfs"}` {
		t.Fatalf("selector = %q, want root filesystem selector", got.Selector)
	}
}

func TestDeleteRuleRejectsBuiltinRule(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nil)
	repo.rules["cpu_high"] = &model.Rule{
		ID:         1,
		RuleKey:    "cpu_high",
		Kind:       model.RuleKindMetricRaw,
		Name:       "CPU High",
		SourceType: model.RuleSourceBuiltin,
		ScopeType:  model.RuleScopeHost,
		Severity:   "warning",
		Enabled:    true,
	}

	err := uc.DeleteRule(context.Background(), 1)
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("DeleteRule err = %v, want ErrForbidden", err)
	}
	if _, err := repo.GetRuleByID(context.Background(), 1); err != nil {
		t.Fatalf("builtin rule should remain after rejected delete: %v", err)
	}
}
