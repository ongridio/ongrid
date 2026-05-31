//go:build e2e

// Catalog: E1 — metric_raw 告警评估器单条触发。流程：
//   - 起 manager 把 ONGRID_ALERT_EVAL_INTERVAL 压到 2s（同时也是 rules 缓存
//     刷新间隔 — main.go 把两者绑在同一个 cfg.Alert.EvaluatorInterval 上）
//   - admin 登录 → POST /api/v1/alert-rules 创建一条 kind=metric_raw,
//     scope=global 的规则，expr 唯一好认 "fake_e2e_metric > 50"
//   - FakeProm.SetInstant 把这条 expr 的查询结果填成一个非空 vector
//     条目（PromQL 的过滤语义本身就是"返回 → 命中"）
//   - 等 5s（一次 rules 刷新 + 至少一次 evaluator tick）
//   - GET /api/v1/alerts/incidents → 必须出现一条以我们 rule_key 为 rule 的
//     incident
package e2e

import (
	"testing"
	"time"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestAlert_MetricRawEvaluatorFires_E1(t *testing.T) {
	env := testenv.Start(t,
		testenv.WithEnv("ONGRID_ALERT_EVAL_INTERVAL", "2s"),
		testenv.WithEnv("ONGRID_ALERT_ENABLED", "true"),
	)
	pair := env.LoginAdmin()

	const (
		ruleKey = "e2e_metric_raw_test"
		expr    = "fake_e2e_metric > 50"
	)

	createStatus, body, err := env.DoJSON("POST", "/api/v1/alert-rules", map[string]any{
		"rule_key":   ruleKey,
		"kind":       "metric_raw",
		"name":       "E2E metric_raw rule",
		"scope_type": "global",
		"join_mode":  "all",
		"severity":   "warning",
		"enabled":    true,
		"spec":       map[string]any{"expr": expr},
	}, pair.AccessToken)
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	if createStatus != 200 && createStatus != 201 {
		t.Fatalf("create rule: status=%d body=%v", createStatus, body)
	}

	// Tell FakeProm: when the evaluator runs our exact expression, return
	// one "firing" series. Empty Labels = global series (no per-host
	// breakdown), perfect for a scope=global rule which validateFiring
	// does not require device_id for.
	env.FakeProm().SetInstant(expr, []testenv.InstantEntry{
		{Labels: map[string]string{}, Value: 95.0},
	})

	// Wait long enough for: manager finishes startup (~10–15s on cold
	// runs), one rules cache refresh + one eval tick (cache interval =
	// EvalInterval = 2s, main.go 801). 45s window with 1s poll gives
	// generous slack for the box's CPU contention.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		status, list, err := env.DoJSON("GET", "/api/v1/alerts/incidents", nil, pair.AccessToken)
		if err != nil {
			t.Fatalf("list incidents: %v", err)
		}
		if status != 200 {
			t.Fatalf("list incidents: status=%d body=%v", status, list)
		}
		if itemsHaveRule(list, ruleKey) {
			return // 通过
		}
		time.Sleep(1 * time.Second)
	}

	// Diagnostic: dump what we did see so the failure message is useful.
	_, list, _ := env.DoJSON("GET", "/api/v1/alerts/incidents", nil, pair.AccessToken)
	t.Fatalf("no incident with rule_key=%q within 45s; list=%v", ruleKey, list)
}

// itemsHaveRule scans the incidents list response for one whose rule_key
// matches. The DTO field is rule_key (not rule) — caught by the first
// E1 run where the assertion missed despite the incident actually firing.
func itemsHaveRule(list map[string]any, ruleKey string) bool {
	items, _ := list["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		if r, _ := m["rule_key"].(string); r == ruleKey {
			return true
		}
	}
	return false
}
