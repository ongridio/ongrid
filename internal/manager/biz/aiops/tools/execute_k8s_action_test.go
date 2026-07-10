package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestExecuteK8sActionToolInfoIsWrite(t *testing.T) {
	tool := NewExecuteK8sActionTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameExecuteK8sAction {
		t.Fatalf("Name=%q want %q", info.Name, ToolNameExecuteK8sAction)
	}
	if info.Class != "write" {
		t.Fatalf("Class=%q want write", info.Class)
	}
	if !strings.Contains(info.WhenToUse, "MUTATING") {
		t.Fatalf("WhenToUse should identify mutating behavior: %s", info.WhenToUse)
	}
}

func TestExecuteK8sActionToolRequiresAdmin(t *testing.T) {
	tool := NewExecuteK8sActionTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())
	args := `{"cluster_id":1,"action":"scale","kind":"Deployment","namespace":"default","name":"api","replicas":3}`

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing identity", ctx: context.Background()},
		{name: "ordinary user", ctx: tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 2, Role: "user"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tool.InvokableRun(tc.ctx, args); !errors.Is(err, errs.ErrForbidden) {
				t.Fatalf("InvokableRun() error = %v, want forbidden", err)
			}
		})
	}
}

func TestExecuteK8sActionToolCallsControllerEdge(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.KubernetesActionResponse{
			ClusterID:  1,
			Action:     "scale",
			Kind:       "Deployment",
			APIVersion: "apps/v1",
			Namespace:  "default",
			Name:       "api",
			Applied:    true,
			Preflight: tunnel.KubernetesActionPreflight{
				Kind:            "Deployment",
				APIVersion:      "apps/v1",
				Namespace:       "default",
				Name:            "api",
				UID:             "deploy-uid",
				ResourceVersion: "10",
				Exists:          true,
			},
			ResultResourceVersion: "11",
		}),
	}
	tool := NewExecuteK8sActionTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	ctx := tenantctx.With(context.Background(), tenantctx.Tenant{UserID: 1, Role: "admin"})
	out, err := tool.InvokableRun(ctx, `{"cluster_id":1,"action":"scale","kind":"Deployment","namespace":"default","name":"api","replicas":3,"expected_resource_version":"10","reason":"roll new capacity"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 77 {
		t.Fatalf("caller edge_id=%d want 77", fc.lastID)
	}
	if fc.lastName != tunnel.MethodExecuteK8sAction {
		t.Fatalf("caller method=%q want %q", fc.lastName, tunnel.MethodExecuteK8sAction)
	}
	var sent tunnel.KubernetesActionRequest
	if err := json.Unmarshal(fc.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.ClusterID != 1 || sent.Action != "scale" || sent.Kind != "Deployment" || sent.Namespace != "default" || sent.Name != "api" {
		t.Fatalf("unexpected sent request: %+v", sent)
	}
	if sent.Replicas == nil || *sent.Replicas != 3 || sent.ExpectedResourceVersion != "10" || sent.Reason != "roll new capacity" {
		t.Fatalf("unexpected scale args: %+v", sent)
	}

	var got struct {
		Source           string `json:"source"`
		ControllerEdgeID uint64 `json:"controller_edge_id"`
		Result           struct {
			Action                string `json:"action"`
			Applied               bool   `json:"applied"`
			ResultResourceVersion string `json:"result_resource_version"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Source != "kubernetes_api" || got.ControllerEdgeID != 77 || got.Result.Action != "scale" || !got.Result.Applied || got.Result.ResultResourceVersion != "11" {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestNormalizeExecuteK8sActionNodeMaintenanceDefaults(t *testing.T) {
	drain, err := normalizeExecuteK8sActionArgs(ExecuteK8sActionArgs{
		ClusterID: 1,
		Action:    "drain",
		Name:      "node-a",
	})
	if err != nil {
		t.Fatalf("normalize drain: %v", err)
	}
	if drain.Action != "drain" || drain.Kind != "Node" || drain.Name != "node-a" {
		t.Fatalf("unexpected drain request: %+v", drain)
	}
	if drain.DrainTimeoutSeconds != defaultK8sDrainTimeoutSeconds || drain.DrainRetrySeconds != defaultK8sDrainRetrySeconds {
		t.Fatalf("unexpected drain defaults: %+v", drain)
	}
	if drain.IgnoreDaemonSets == nil || !*drain.IgnoreDaemonSets {
		t.Fatalf("ignore_daemonsets should default true: %+v", drain)
	}

	evict, err := normalizeExecuteK8sActionArgs(ExecuteK8sActionArgs{
		ClusterID: 1,
		Action:    "evict",
		Namespace: "default",
		Name:      "api-1",
	})
	if err != nil {
		t.Fatalf("normalize evict: %v", err)
	}
	if evict.Action != "evict_pod" || evict.Kind != "Pod" || evict.Namespace != "default" || evict.Name != "api-1" {
		t.Fatalf("unexpected evict request: %+v", evict)
	}
}

func TestNormalizeExecuteK8sActionDrainOptions(t *testing.T) {
	grace := 30
	ignoreDaemonSets := false
	req, err := normalizeExecuteK8sActionArgs(ExecuteK8sActionArgs{
		ClusterID:           1,
		Action:              "drain",
		Name:                "node-a",
		GracePeriodSeconds:  &grace,
		DrainTimeoutSeconds: 300,
		DrainRetrySeconds:   5,
		IgnoreDaemonSets:    &ignoreDaemonSets,
		DeleteEmptyDirData:  true,
		Force:               true,
		DisableEviction:     true,
	})
	if err != nil {
		t.Fatalf("normalize drain options: %v", err)
	}
	if req.GracePeriodSeconds == nil || *req.GracePeriodSeconds != 30 || req.DrainTimeoutSeconds != 300 || req.DrainRetrySeconds != 5 {
		t.Fatalf("unexpected numeric drain options: %+v", req)
	}
	if req.IgnoreDaemonSets == nil || *req.IgnoreDaemonSets || !req.DeleteEmptyDirData || !req.Force || !req.DisableEviction {
		t.Fatalf("unexpected boolean drain options: %+v", req)
	}
	if executeK8sActionTimeout(req) != 315*time.Second {
		t.Fatalf("execute timeout=%s want 315s", executeK8sActionTimeout(req))
	}
}

func TestNormalizeExecuteK8sActionRejectsDrainOptionBounds(t *testing.T) {
	badGrace := maxK8sGracePeriodSeconds + 1
	cases := []struct {
		name string
		args ExecuteK8sActionArgs
		want string
	}{
		{
			name: "grace",
			args: ExecuteK8sActionArgs{ClusterID: 1, Action: "drain", Name: "node-a", GracePeriodSeconds: &badGrace},
			want: "grace_period_seconds",
		},
		{
			name: "timeout",
			args: ExecuteK8sActionArgs{ClusterID: 1, Action: "drain", Name: "node-a", DrainTimeoutSeconds: maxK8sDrainTimeoutSeconds + 1},
			want: "drain_timeout_seconds",
		},
		{
			name: "retry",
			args: ExecuteK8sActionArgs{ClusterID: 1, Action: "drain", Name: "node-a", DrainRetrySeconds: maxK8sDrainRetrySeconds + 1},
			want: "drain_retry_seconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeExecuteK8sActionArgs(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestExecuteK8sActionRegisteredOnlyAsBaseTool(t *testing.T) {
	reg := NewRegistry(&fakeCaller{}, nil, nil, nil, nil, nil, nil, slog.Default())
	reg.SetK8sSnapshotReader(newFakeK8sSnapshotReader())

	if containsName(schemaNames(reg.Schemas()), ToolNameExecuteK8sAction) {
		t.Fatalf("legacy closure registry should not expose mutating %q", ToolNameExecuteK8sAction)
	}
	names := toolInfoNames(t, reg.BuildBaseTools().AllTools())
	if !containsName(names, ToolNameExecuteK8sAction) {
		t.Fatalf("base tool registry missing %q: %v", ToolNameExecuteK8sAction, names)
	}
	if toolTier(NewExecuteK8sActionTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())) != "specialty" {
		t.Fatalf("%s should be a specialty tool", ToolNameExecuteK8sAction)
	}
}
