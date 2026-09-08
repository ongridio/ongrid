package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestKubeEventsToolCallsControllerEdge(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.KubernetesListEventsResponse{
			ClusterID: 1,
			Namespace: "default",
			Events: []tunnel.KubernetesEventSnapshot{{
				Namespace:    "default",
				Name:         "api-1.17c1a2",
				Type:         "Warning",
				Reason:       "BackOff",
				InvolvedKind: "Pod",
				InvolvedName: "api-1",
			}},
			Total:     1,
			FetchedAt: 42,
		}),
	}
	tool := NewKubeEventsTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"cluster_id":1,"namespace":"default","type":"Warning","reason":"BackOff","involved_kind":"Pod","involved_name":"api-1","limit":200}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 77 {
		t.Fatalf("caller edge_id=%d want 77", fc.lastID)
	}
	if fc.lastName != tunnel.MethodListK8sEvents {
		t.Fatalf("caller method=%q want %q", fc.lastName, tunnel.MethodListK8sEvents)
	}
	var sent tunnel.KubernetesListEventsRequest
	if err := json.Unmarshal(fc.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.ClusterID != 1 || sent.Namespace != "default" || sent.Type != "Warning" || sent.Reason != "BackOff" {
		t.Fatalf("unexpected sent request: %+v", sent)
	}
	if sent.InvolvedKind != "Pod" || sent.InvolvedName != "api-1" || sent.Limit != 200 {
		t.Fatalf("unexpected sent filters: %+v", sent)
	}

	var got struct {
		Source           string `json:"source"`
		ControllerEdgeID uint64 `json:"controller_edge_id"`
		Result           struct {
			Total  int `json:"total"`
			Events []struct {
				Reason string `json:"reason"`
			} `json:"events"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Source != "kubernetes_api" || got.ControllerEdgeID != 77 || got.Result.Total != 1 {
		t.Fatalf("unexpected output: %+v", got)
	}
	if len(got.Result.Events) != 1 || got.Result.Events[0].Reason != "BackOff" {
		t.Fatalf("unexpected events: %+v", got.Result.Events)
	}
}

func TestKubeEventsToolClampsLimit(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.KubernetesListEventsResponse{ClusterID: 1}),
	}
	tool := NewKubeEventsTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	if _, err := tool.InvokableRun(context.Background(), `{"cluster_id":1,"limit":9999}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var sent tunnel.KubernetesListEventsRequest
	if err := json.Unmarshal(fc.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.Limit != 200 {
		t.Fatalf("limit should clamp to 200, got %d", sent.Limit)
	}
}

func TestKubeEventsToolRequiresClusterID(t *testing.T) {
	tool := NewKubeEventsTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())
	if _, err := tool.InvokableRun(context.Background(), `{"namespace":"default"}`); err == nil {
		t.Fatal("InvokableRun should reject missing cluster_id")
	}
}

func TestKubeEventsRegisteredInClosureAndBaseToolPaths(t *testing.T) {
	reg := NewRegistry(&fakeCaller{}, nil, nil, nil, nil, nil, nil, slog.Default())
	reg.SetK8sSnapshotReader(newFakeK8sSnapshotReader())

	if !containsName(schemaNames(reg.Schemas()), ToolNameKubeEvents) {
		t.Fatalf("closure registry missing %q", ToolNameKubeEvents)
	}
	names := toolInfoNames(t, reg.BuildBaseTools().AllTools())
	if !containsName(names, ToolNameKubeEvents) {
		t.Fatalf("base tool registry missing %q: %v", ToolNameKubeEvents, names)
	}
	if toolTier(NewKubeEventsTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())) != "core" {
		t.Fatalf("%s should be a core tool", ToolNameKubeEvents)
	}
}

var _ basetool.BaseTool = (*KubeEventsTool)(nil)
