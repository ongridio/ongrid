package externalmcp

import (
	"encoding/json"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/stretchr/testify/require"
)

func TestResourceDiscoveryRequiredFieldsAreArrays(t *testing.T) {
	identity := integrationauth.Identity{Scopes: integrationauth.Scopes()}
	for _, tool := range (Resources{}).Tools() {
		t.Run(tool.Name, func(t *testing.T) {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(tool.InputSchema(identity), &schema))
			require.IsType(t, []any{}, schema["required"], "MCP clients reject required: null")
		})
	}
}

func TestResourceRiskOnlyExemptsApprovedCreationAndMetadata(t *testing.T) {
	catalog, err := biz.NewCatalog((Resources{}).Tools())
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		args biz.Arguments
		low  bool
	}{
		{"update_device", biz.Arguments{"device_id": float64(1), "name": "new"}, true},
		{"update_device", biz.Arguments{"device_id": float64(1), "description": "new"}, true},
		{"update_device", biz.Arguments{"device_id": float64(1), "name": "new", "environment": "prod"}, false},
		{"delete_device", biz.Arguments{"device_id": float64(1)}, false},
		{"manage_monitor_panel", biz.Arguments{"action": "create"}, true},
		{"manage_monitor_panel", biz.Arguments{"action": "update", "panel_id": float64(1), "title": "new", "idempotency_key": "fixture-key", "expected_revision": "revision"}, true},
		{"manage_monitor_panel", biz.Arguments{"action": "update", "title": "new", "promql": "other"}, false},
		{"manage_monitor_panel", biz.Arguments{"action": "delete"}, false},
		{"manage_alert_rule", biz.Arguments{"action": "create"}, true},
		{"manage_alert_rule", biz.Arguments{"action": "update"}, false},
		{"manage_alert_rule", biz.Arguments{"action": "set_enabled", "enabled": false}, false},
		{"manage_alert_rule", biz.Arguments{"action": "delete"}, false},
	} {
		policy, err := catalog[tc.name].Policy(tc.args)
		require.NoError(t, err)
		require.Equal(t, tc.low, policy.LowRisk, "%s %v", tc.name, tc.args)
	}
}
