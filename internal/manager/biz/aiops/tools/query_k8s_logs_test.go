package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestQueryK8sLogsToolCallsControllerEdge(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.KubernetesPodLogsResponse{
			ClusterID:    1,
			Namespace:    "default",
			Pod:          "api-1",
			Container:    "api",
			SinceSeconds: 60,
			TailLines:    10,
			LimitBytes:   4096,
			Timestamps:   true,
			FetchedAt:    42,
			Bytes:        11,
			LineCount:    1,
			Logs:         "hello world",
		}),
	}
	tool := NewQueryK8sLogsTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"cluster_id":1,"namespace":"default","pod":"api-1","container":"api","since_seconds":60,"tail_lines":10,"limit_bytes":4096}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 77 {
		t.Fatalf("caller edge_id=%d want 77", fc.lastID)
	}
	if fc.lastName != tunnel.MethodQueryK8sLogs {
		t.Fatalf("caller method=%q want %q", fc.lastName, tunnel.MethodQueryK8sLogs)
	}
	var sent tunnel.KubernetesPodLogsRequest
	if err := json.Unmarshal(fc.lastBody, &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if sent.ClusterID != 1 || sent.Namespace != "default" || sent.Pod != "api-1" || sent.Container != "api" {
		t.Fatalf("unexpected sent request: %+v", sent)
	}
	if sent.SinceSeconds != 60 || sent.TailLines != 10 || sent.LimitBytes != 4096 || !sent.Timestamps {
		t.Fatalf("unexpected limits/defaults: %+v", sent)
	}

	var got struct {
		Source           string `json:"source"`
		ControllerEdgeID uint64 `json:"controller_edge_id"`
		Result           struct {
			Logs      string `json:"logs"`
			LineCount int    `json:"line_count"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Source != "kubernetes_pods_log" || got.ControllerEdgeID != 77 || got.Result.Logs != "hello world" || got.Result.LineCount != 1 {
		t.Fatalf("unexpected output: %+v", got)
	}
}

func TestQueryK8sLogsReturnsUnavailableForRBACForbidden(t *testing.T) {
	fc := &fakeCaller{
		respErr: errors.New("query_k8s_logs: get pod logs default/api-1: kubernetes api forbidden"),
	}
	tool := NewQueryK8sLogsTool(fc, newFakeK8sSnapshotReader(), slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"cluster_id":1,"namespace":"default","pod":"api-1","previous":true}`)
	if err != nil {
		t.Fatalf("InvokableRun should return structured unavailable result: %v", err)
	}

	var got struct {
		Source           string `json:"source"`
		Status           string `json:"status"`
		ErrorKind        string `json:"error_kind"`
		Error            string `json:"error"`
		Advice           string `json:"advice"`
		ControllerEdgeID uint64 `json:"controller_edge_id"`
		Result           struct {
			ClusterID uint64 `json:"cluster_id"`
			Namespace string `json:"namespace"`
			Pod       string `json:"pod"`
			Previous  bool   `json:"previous"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Source != "kubernetes_pods_log" || got.Status != "unavailable" || got.ErrorKind != "rbac_forbidden" {
		t.Fatalf("unexpected unavailable envelope: %+v", got)
	}
	if got.ControllerEdgeID != 77 || got.Result.ClusterID != 1 || got.Result.Namespace != "default" || got.Result.Pod != "api-1" || !got.Result.Previous {
		t.Fatalf("unexpected identity in unavailable result: %+v", got)
	}
	if got.Error == "" || got.Advice == "" {
		t.Fatalf("error/advice should be present: %+v", got)
	}
}

func TestNormalizeQueryK8sLogsArgsBounds(t *testing.T) {
	req, err := normalizeQueryK8sLogsArgs(QueryK8sLogsArgs{
		ClusterID:    1,
		Namespace:    " default ",
		Pod:          " api-1 ",
		SinceSeconds: 999999,
		TailLines:    9999,
		LimitBytes:   999999,
	})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if req.Namespace != "default" || req.Pod != "api-1" {
		t.Fatalf("unexpected identity: %+v", req)
	}
	if req.SinceSeconds != maxPodLogSinceSeconds || req.TailLines != maxPodLogTailLines || req.LimitBytes != maxPodLogLimitBytes {
		t.Fatalf("limits not clamped: %+v", req)
	}
	if !req.Timestamps {
		t.Fatalf("timestamps should default true")
	}
	disabled := false
	req, err = normalizeQueryK8sLogsArgs(QueryK8sLogsArgs{
		ClusterID:  1,
		Namespace:  "default",
		Pod:        "api-1",
		Timestamps: &disabled,
	})
	if err != nil {
		t.Fatalf("normalize disabled timestamps: %v", err)
	}
	if req.Timestamps {
		t.Fatalf("timestamps override ignored: %+v", req)
	}
}

func TestQueryK8sLogsRegisteredInClosureAndBaseToolPaths(t *testing.T) {
	reg := NewRegistry(&fakeCaller{}, nil, nil, nil, nil, nil, nil, slog.Default())
	reg.SetK8sSnapshotReader(newFakeK8sSnapshotReader())

	if !containsName(schemaNames(reg.Schemas()), ToolNameQueryK8sLogs) {
		t.Fatalf("closure registry missing %q", ToolNameQueryK8sLogs)
	}
	names := toolInfoNames(t, reg.BuildBaseTools().AllTools())
	if !containsName(names, ToolNameQueryK8sLogs) {
		t.Fatalf("base tool registry missing %q: %v", ToolNameQueryK8sLogs, names)
	}
	if toolTier(NewQueryK8sLogsTool(&fakeCaller{}, newFakeK8sSnapshotReader(), slog.Default())) != "core" {
		t.Fatalf("%s should be a core tool", ToolNameQueryK8sLogs)
	}
}

var _ basetool.BaseTool = (*QueryK8sLogsTool)(nil)
