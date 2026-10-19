package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestGetHostSummaryTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameGetHostSummary {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	dp, _ := props["device_ids"].(map[string]any)
	if dp == nil || dp["type"] != "array" {
		t.Errorf("device_ids must be array: %+v", dp)
	}
}

func TestGetHostSummaryTool_BatchHappy(t *testing.T) {
	now := time.Now()
	e1 := &edgemodel.Edge{ID: 5, Name: "host-5", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	e2 := &edgemodel.Edge{ID: 6, Name: "host-6", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1, e2), nil, nil, slog.Default())
	tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[5,6]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env HostSummaryBatchResponse
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 0 {
		t.Errorf("counts = %d/%d, want 2/0", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 2 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	if env.Results[0].DeviceID != 5 || env.Results[1].DeviceID != 6 {
		t.Errorf("order corrupted: %+v", env.Results)
	}
	for i, r := range env.Results {
		if r.Summary == nil {
			t.Errorf("entry %d Summary nil", i)
			continue
		}
		edgeBlk, ok := r.Summary["host"].(map[string]any)
		if !ok {
			t.Errorf("entry %d edge block missing", i)
			continue
		}
		if edgeBlk["name"] == "" {
			t.Errorf("entry %d edge.name empty", i)
		}
	}
}

func TestGetHostSummaryTool_BatchPartialSuccess(t *testing.T) {
	now := time.Now()
	e1 := &edgemodel.Edge{ID: 5, Name: "host-5", Status: edgemodel.StatusOffline, LastSeenAt: &now}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1), nil, nil, slog.Default())
	tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)

	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[5,99]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var env HostSummaryBatchResponse
	_ = json.Unmarshal([]byte(out), &env)
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if env.Results[1].Error == "" {
		t.Errorf("entry 1 should carry not-found error")
	}
}

func TestGetHostSummaryTool_BadArgs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
	// Missing / empty device_ids now means "summarize all edges" (no error).
	// With an empty fake fleet this returns a valid empty envelope.
	if out, err := tool.InvokableRun(context.Background(), `{}`); err != nil {
		t.Errorf("missing device_ids should be all-edges mode, got err: %v", err)
	} else if !strings.Contains(out, `"results"`) {
		t.Errorf("expected envelope for all-edges mode, got: %s", out)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[]}`); err != nil {
		t.Errorf("empty device_ids should be all-edges mode, got err: %v", err)
	}
}

func TestGetHostSummaryTool_NilEdges(t *testing.T) {
	tool := NewGetHostSummaryTool(nil, nil, nil, nil, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"device_ids":[1]}`); err == nil {
		t.Errorf("expected early error when edges nil")
	}
}

func TestGetHostSummaryTool_TooManyIDs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)
	ids := make([]uint64, batchMaxIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	args, _ := json.Marshal(map[string]any{"device_ids": ids})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-ids error: %v", err)
	}
}

func TestHostSummaryKeepsHostIncidentIdentityAcrossInstallations(t *testing.T) {
	hostID := uint64(41)
	edges := edgebiz.NewUsecase(newFakeEdgeRepo(&edgemodel.Edge{ID: 23, DeviceID: &hostID, Name: "persistent-host", Status: edgemodel.StatusOffline}), nil, nil, nil)
	devices := testHostInventory(t, edges)
	alerts := &fakeAlertUC{}
	tool := NewGetHostSummaryTool(nil, edges, devices, alerts, nil)
	out, err := tool.InvokableRun(context.Background(), `{"device_ids":[41]}`)
	if err != nil {
		t.Fatal(err)
	}
	var result HostSummaryBatchResponse
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("summary=%s", out)
	}
	if alerts.lastIncidentFilter.DeviceID == nil || *alerts.lastIncidentFilter.DeviceID != 41 {
		t.Fatalf("incident filter=%+v", alerts.lastIncidentFilter)
	}
	if result.Results[0].Summary["execution_available"] != false {
		t.Fatalf("offline host executable: %s", out)
	}
	current := result.Results[0].Summary["current_edge"].(map[string]any)
	if current["edge_id"] != float64(23) {
		t.Fatalf("wrong runtime: %s", out)
	}
	// Losing the runtime dependency must not remove the Host or its incident history.
	tool = NewGetHostSummaryTool(nil, nil, devices, alerts, nil)
	out, err = tool.InvokableRun(context.Background(), `{"device_ids":[41]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result.SuccessCount != 1 || result.Results[0].Summary["current_edge"] != nil {
		t.Fatalf("history without edge: %s", out)
	}
}

// 在线/离线都不能把未查询的插件状态解释成不支持；版本只能归属于 Edge。
func TestGetHostSummaryTool_VersionAndPluginEvidence(t *testing.T) {
	for _, status := range []string{edgemodel.StatusOnline, edgemodel.StatusOffline} {
		t.Run(string(status), func(t *testing.T) {
			hostID := uint64(91)
			edge := &edgemodel.Edge{ID: 7, DeviceID: &hostID, Name: "host", Status: status, AgentVersion: "v0.18.2"}
			uc := edgebiz.NewUsecase(newFakeEdgeRepo(edge), nil, nil, slog.Default())
			tool := NewGetHostSummaryTool(nil, uc, testHostInventory(t, uc), nil, nil)
			out, err := tool.InvokableRun(context.Background(), `{"device_ids":[91]}`)
			if err != nil {
				t.Fatal(err)
			}
			var response HostSummaryBatchResponse
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			if response.SuccessCount != 1 {
				t.Fatalf("unexpected result: %s", out)
			}
			summary := response.Results[0].Summary
			current := summary["current_edge"].(map[string]any)
			if current["edge_version"] != "v0.18.2" || current["edge_id"] != float64(7) {
				t.Fatalf("wrong Edge metadata: %v", current)
			}
			if _, ok := current["agent_version"]; ok {
				t.Fatal("ambiguous version field leaked to model")
			}
			if _, ok := summary["agent_runtime_version"]; ok {
				t.Fatal("Host summary must not invent chat runtime")
			}
			if summary["plugin_status"] != "not_queried" || summary["plugin_status_reason"] == "" {
				t.Fatalf("unknown plugin status misrepresented: %v", summary)
			}
		})
	}
}
