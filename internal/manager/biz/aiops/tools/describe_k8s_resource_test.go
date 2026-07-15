package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestDescribeK8sResourceToolCallsControllerEdge(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.KubernetesDescribeResourceResponse{
			ClusterID:       1,
			Kind:            "Pod",
			APIVersion:      "v1",
			Namespace:       "default",
			Name:            "api-1",
			UID:             "pod-uid",
			ResourceVersion: "123",
			FetchedAt:       42,
			Object:          json.RawMessage(`{"kind":"Pod","metadata":{"name":"api-1"}}`),
			Events: []tunnel.KubernetesEventSnapshot{{
				Namespace:    "default",
				Name:         "api-1.1",
				Type:         "Normal",
				Reason:       "Started",
				InvolvedKind: "Pod",
				InvolvedName: "api-1",
			}},
		}),
	}
	tool := NewDescribeK8sResourceTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"cluster_id":1,"kind":"Pod","namespace":"default","name":"api-1"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 77 {
		t.Fatalf("caller edge_id=%d want 77", fc.lastID)
	}
	if fc.lastName != tunnel.MethodDescribeK8sResource {
		t.Fatalf("caller method=%q want %q", fc.lastName, tunnel.MethodDescribeK8sResource)
	}
	var sent tunnel.KubernetesDescribeResourceRequest
	if err := json.Unmarshal(fc.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.ClusterID != 1 || sent.Kind != "Pod" || sent.Namespace != "default" || sent.Name != "api-1" {
		t.Fatalf("unexpected sent request: %+v", sent)
	}
	if !sent.IncludeEvents || sent.EventsLimit != 20 {
		t.Fatalf("events defaults not applied: %+v", sent)
	}

	var got struct {
		Source           string `json:"source"`
		ControllerEdgeID uint64 `json:"controller_edge_id"`
		Result           struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Events []struct {
				Reason string `json:"reason"`
			} `json:"events"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Source != "kubernetes_api" || got.ControllerEdgeID != 77 || got.Result.Kind != "Pod" || got.Result.Name != "api-1" {
		t.Fatalf("unexpected output: %+v", got)
	}
	if len(got.Result.Events) != 1 || got.Result.Events[0].Reason != "Started" {
		t.Fatalf("unexpected output events: %+v", got.Result.Events)
	}
}

func TestDescribeK8sResourceRegisteredInClosureAndBaseToolPaths(t *testing.T) {
	reg := NewRegistry(&fakeCaller{}, nil, nil, nil, nil, nil, nil, slog.Default())
	reg.SetK8sSnapshotReader(newFakeK8sSnapshotReader())

	if !containsName(schemaNames(reg.Schemas()), ToolNameDescribeK8sResource) {
		t.Fatalf("closure registry missing %q", ToolNameDescribeK8sResource)
	}
	names := toolInfoNames(t, reg.BuildBaseTools().AllTools())
	if !containsName(names, ToolNameDescribeK8sResource) {
		t.Fatalf("base tool registry missing %q: %v", ToolNameDescribeK8sResource, names)
	}
	if toolTier(NewDescribeK8sResourceTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())) != "core" {
		t.Fatalf("%s should be a core tool", ToolNameDescribeK8sResource)
	}
}

var _ basetool.BaseTool = (*DescribeK8sResourceTool)(nil)
