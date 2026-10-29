package externalmcp

import (
	"encoding/json"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
	"github.com/stretchr/testify/require"
)

func TestRuleContractsMatchSupportedBusinessKinds(t *testing.T) {
	catalog, err := biz.NewCatalog([]biz.Tool{(Resources{Alerts: &alertsvc.Service{}}).ruleTool()})
	require.NoError(t, err)
	for _, tc := range []struct {
		kind string
		spec map[string]any
	}{
		{"metric_raw", map[string]any{"expr": "up == 0"}},
		{"metric_burn_rate", map[string]any{"sli": "sum(rate(ok_total[$window])) / sum(rate(all_total[$window]))", "slo": 99.9, "burns": []any{map[string]any{"window": "5m", "multiplier": 14.4}}}},
		{"log_search", map[string]any{"keywords": map[string]any{"include": []string{"error"}}, "window": "5m", "operator": ">", "threshold": 1}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			input := map[string]any{"action": "create", "idempotency_key": "rule-test-01", "rule": map[string]any{"rule_key": "fixture", "name": "Test rule", "kind": tc.kind, "scope_type": "global", "severity": "warning", "enabled": true, "spec": tc.spec}}
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			_, err = catalog["manage_alert_rule"].Parse(raw)
			require.NoError(t, err)
			tc.spec["unexpected"] = true
			raw, err = json.Marshal(input)
			require.NoError(t, err)
			_, err = catalog["manage_alert_rule"].Parse(raw)
			require.Error(t, err)
		})
	}
}

func TestProjectionRemovesCredentialsAndPreservesRevision(t *testing.T) {
	result := sanitize(map[string]any{"revision": "abc", "attributes": map[string]any{"secret": "private"}, "env": []any{"TOKEN=private"}, "message": "Authorization: Bearer fixture-secret", "preflight_token": "bound-once"}).(map[string]any)
	require.NotContains(t, result, "attributes")
	require.NotContains(t, result, "env")
	require.Equal(t, "abc", result["revision"])
	require.Equal(t, "bound-once", result["preflight_token"])
	require.NotContains(t, result["message"], "fixture-secret")
	for _, text := range []string{`{"password":"fixture-secret"}`, `api_key='fixture-secret'`, `Authorization: Bearer fixture-secret`} {
		require.NotContains(t, sanitize(text), "fixture-secret")
	}
}
