package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestQueryK8sSnapshotToolInfo(t *testing.T) {
	tool := NewQueryK8sSnapshotTool(&fakeK8sSnapshotReader{}, slog.Default())
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryK8sSnapshot {
		t.Fatalf("Name=%q want %q", info.Name, ToolNameQueryK8sSnapshot)
	}
	if info.Class != "read" {
		t.Fatalf("Class=%q want read", info.Class)
	}
	if !strings.Contains(info.Description, "DB snapshot") {
		t.Fatalf("description should identify DB snapshot source: %s", info.Description)
	}
}

func TestQueryK8sSnapshotAggregatesPodCountsFromDBSnapshot(t *testing.T) {
	reader := newFakeK8sSnapshotReader()
	tool := NewQueryK8sSnapshotTool(reader, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"resource":"pods","phase":"Pending","limit":1}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	var got struct {
		Resource  string `json:"resource"`
		Source    string `json:"source"`
		Total     int64  `json:"total"`
		Truncated bool   `json:"truncated"`
		Pods      []struct {
			ClusterID uint64 `json:"cluster_id"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Phase     string `json:"phase"`
		} `json:"pods"`
		PerCluster []struct {
			ClusterID uint64 `json:"cluster_id"`
			Pods      int64  `json:"pods"`
			Total     int64  `json:"total"`
		} `json:"per_cluster"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Resource != "pods" || got.Source != "manager_db_snapshot" {
		t.Fatalf("unexpected source/resource: %+v", got)
	}
	if got.Total != 1 {
		t.Fatalf("total=%d want 1", got.Total)
	}
	if len(got.Pods) != 1 || got.Pods[0].Name != "api-pending" || got.Pods[0].Phase != "Pending" {
		t.Fatalf("unexpected pods: %+v", got.Pods)
	}
	if got.Truncated {
		t.Fatalf("truncated=true, want false")
	}
	if len(got.PerCluster) != 2 || got.PerCluster[0].Pods != 1 || got.PerCluster[1].Pods != 0 {
		t.Fatalf("unexpected per_cluster counts: %+v", got.PerCluster)
	}
}

func TestQueryK8sSnapshotFiltersPodsByReason(t *testing.T) {
	reader := newFakeK8sSnapshotReader()
	now := time.Now()
	reader.pods = append(reader.pods, &k8smodel.Pod{
		ClusterID:    1,
		Namespace:    "default",
		Name:         "api-crash",
		UID:          "p-crash",
		Phase:        "Running",
		OwnerKind:    "Deployment",
		OwnerName:    "api",
		RestartCount: 7,
		Reason:       "CrashLoopBackOff",
		LastSeenAt:   &now,
	})
	tool := NewQueryK8sSnapshotTool(reader, slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{"resource":"pods","reason":"CrashLoopBackOff","limit":10}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got struct {
		Total int64 `json:"total"`
		Pods  []struct {
			Name   string `json:"name"`
			Reason string `json:"reason"`
		} `json:"pods"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Total != 1 || len(got.Pods) != 1 || got.Pods[0].Name != "api-crash" || got.Pods[0].Reason != "CrashLoopBackOff" {
		t.Fatalf("unexpected reason-filtered pods: %+v", got)
	}
}

func TestQueryK8sSnapshotSummaryReturnsTotals(t *testing.T) {
	tool := NewQueryK8sSnapshotTool(newFakeK8sSnapshotReader(), slog.Default())

	out, err := tool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}

	var got struct {
		Resource string `json:"resource"`
		Source   string `json:"source"`
		Total    int64  `json:"total"`
		Totals   struct {
			Nodes     int64 `json:"nodes"`
			Workloads int64 `json:"workloads"`
			Pods      int64 `json:"pods"`
			Events    int64 `json:"events"`
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if got.Resource != "summary" || got.Source != "manager_db_snapshot" {
		t.Fatalf("unexpected source/resource: %+v", got)
	}
	if got.Total != 2 {
		t.Fatalf("cluster total=%d want 2", got.Total)
	}
	if got.Totals.Nodes != 1 || got.Totals.Workloads != 2 || got.Totals.Pods != 3 || got.Totals.Events != 1 {
		t.Fatalf("unexpected totals: %+v", got.Totals)
	}
}

func TestQueryK8sSnapshotRegisteredInClosureAndBaseToolPaths(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default())
	reg.SetK8sSnapshotReader(newFakeK8sSnapshotReader())

	if !containsName(schemaNames(reg.Schemas()), ToolNameQueryK8sSnapshot) {
		t.Fatalf("closure registry missing %q", ToolNameQueryK8sSnapshot)
	}
	bag := reg.BuildBaseTools()
	names := toolInfoNames(t, bag.AllTools())
	if !containsName(names, ToolNameQueryK8sSnapshot) {
		t.Fatalf("base tool registry missing %q: %v", ToolNameQueryK8sSnapshot, names)
	}
	if toolTier(NewQueryK8sSnapshotTool(newFakeK8sSnapshotReader(), slog.Default())) != "core" {
		t.Fatalf("%s should be a core tool", ToolNameQueryK8sSnapshot)
	}
}

var _ K8sSnapshotReader = (*fakeK8sSnapshotReader)(nil)
var _ basetool.BaseTool = (*QueryK8sSnapshotTool)(nil)

type fakeK8sSnapshotReader struct {
	clusters  []*k8smodel.Cluster
	nodes     []*k8smodel.Node
	workloads []*k8smodel.Workload
	pods      []*k8smodel.Pod
	events    []*k8smodel.Event
}

func newFakeK8sSnapshotReader() *fakeK8sSnapshotReader {
	now := time.Now().UTC()
	controllerEdgeID := uint64(77)
	return &fakeK8sSnapshotReader{
		clusters: []*k8smodel.Cluster{
			{ID: 1, Name: "prod", Mode: k8smodel.ModeFullNode, Status: k8smodel.ClusterStatusOnline, ControllerEdgeID: &controllerEdgeID, LastSeenAt: &now},
			{ID: 2, Name: "serverless", Mode: k8smodel.ModeServerless, Status: k8smodel.ClusterStatusOnline, LastSeenAt: &now},
		},
		nodes: []*k8smodel.Node{
			{ClusterID: 1, NodeName: "node-a", NodeUID: "node-a-uid", LastSeenAt: &now},
		},
		workloads: []*k8smodel.Workload{
			{ClusterID: 1, Namespace: "default", Kind: "Deployment", Name: "api", DesiredReplicas: 2, ReadyReplicas: 1, LastSeenAt: &now},
			{ClusterID: 2, Namespace: "tenant-a", Kind: "Deployment", Name: "worker", DesiredReplicas: 1, ReadyReplicas: 1, LastSeenAt: &now},
		},
		pods: []*k8smodel.Pod{
			{ClusterID: 1, Namespace: "default", Name: "api-running", UID: "p1", Phase: "Running", OwnerKind: "Deployment", OwnerName: "api", LastSeenAt: &now},
			{ClusterID: 1, Namespace: "default", Name: "api-pending", UID: "p2", Phase: "Pending", OwnerKind: "Deployment", OwnerName: "api", Reason: "Unschedulable", LastSeenAt: &now},
			{ClusterID: 2, Namespace: "tenant-a", Name: "worker-running", UID: "p3", Phase: "Running", OwnerKind: "Deployment", OwnerName: "worker", LastSeenAt: &now},
		},
		events: []*k8smodel.Event{
			{ClusterID: 1, Namespace: "default", Name: "api-pending.1", UID: "e1", Type: "Warning", Reason: "FailedScheduling", InvolvedKind: "Pod", InvolvedName: "api-pending", Message: "no nodes available", LastSeenAt: &now},
		},
	}
}

func (f *fakeK8sSnapshotReader) ListClusters(_ context.Context, filter k8sbiz.ListClustersFilter) ([]*k8smodel.Cluster, error) {
	out := make([]*k8smodel.Cluster, 0, len(f.clusters))
	for _, c := range f.clusters {
		if filter.Name != "" && !strings.Contains(c.Name, filter.Name) {
			continue
		}
		if filter.Status != "" && c.Status != filter.Status {
			continue
		}
		if filter.Mode != "" && c.Mode != filter.Mode {
			continue
		}
		cp := *c
		out = append(out, &cp)
	}
	return limitClusters(out, filter.Limit, filter.Offset), nil
}

func (f *fakeK8sSnapshotReader) GetCluster(_ context.Context, id uint64) (*k8smodel.Cluster, error) {
	for _, c := range f.clusters {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, errs.ErrNotFound
}

func (f *fakeK8sSnapshotReader) ListNodes(_ context.Context, clusterID uint64) ([]*k8smodel.Node, error) {
	out := make([]*k8smodel.Node, 0)
	for _, n := range f.nodes {
		if n.ClusterID == clusterID {
			cp := *n
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeK8sSnapshotReader) CountNodes(ctx context.Context, clusterID uint64) (int64, error) {
	items, err := f.ListNodes(ctx, clusterID)
	return int64(len(items)), err
}

func (f *fakeK8sSnapshotReader) ListWorkloads(_ context.Context, filter k8sbiz.ListWorkloadsFilter) ([]*k8smodel.Workload, error) {
	out := make([]*k8smodel.Workload, 0)
	for _, w := range f.workloads {
		if !matchesWorkloadFilter(w, filter) {
			continue
		}
		cp := *w
		out = append(out, &cp)
	}
	return limitWorkloads(out, filter.Limit, filter.Offset), nil
}

func (f *fakeK8sSnapshotReader) CountWorkloads(_ context.Context, filter k8sbiz.ListWorkloadsFilter) (int64, error) {
	var total int64
	for _, w := range f.workloads {
		if matchesWorkloadFilter(w, filter) {
			total++
		}
	}
	return total, nil
}

func (f *fakeK8sSnapshotReader) ListPods(_ context.Context, filter k8sbiz.ListPodsFilter) ([]*k8smodel.Pod, error) {
	out := make([]*k8smodel.Pod, 0)
	for _, p := range f.pods {
		if !matchesPodFilter(p, filter) {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return limitPods(out, filter.Limit, filter.Offset), nil
}

func (f *fakeK8sSnapshotReader) CountPods(_ context.Context, filter k8sbiz.ListPodsFilter) (int64, error) {
	var total int64
	for _, p := range f.pods {
		if matchesPodFilter(p, filter) {
			total++
		}
	}
	return total, nil
}

func (f *fakeK8sSnapshotReader) ListEvents(_ context.Context, filter k8sbiz.ListEventsFilter) ([]*k8smodel.Event, error) {
	out := make([]*k8smodel.Event, 0)
	for _, e := range f.events {
		if !matchesEventFilter(e, filter) {
			continue
		}
		cp := *e
		out = append(out, &cp)
	}
	return limitEvents(out, filter.Limit, filter.Offset), nil
}

func (f *fakeK8sSnapshotReader) CountEvents(_ context.Context, filter k8sbiz.ListEventsFilter) (int64, error) {
	var total int64
	for _, e := range f.events {
		if matchesEventFilter(e, filter) {
			total++
		}
	}
	return total, nil
}

func matchesWorkloadFilter(w *k8smodel.Workload, f k8sbiz.ListWorkloadsFilter) bool {
	return w.ClusterID == f.ClusterID &&
		(f.Namespace == "" || w.Namespace == f.Namespace) &&
		(f.Kind == "" || w.Kind == f.Kind)
}

func matchesPodFilter(p *k8smodel.Pod, f k8sbiz.ListPodsFilter) bool {
	return p.ClusterID == f.ClusterID &&
		(f.Namespace == "" || p.Namespace == f.Namespace) &&
		(f.NodeName == "" || p.NodeName == f.NodeName) &&
		(f.Phase == "" || p.Phase == f.Phase) &&
		(f.Reason == "" || p.Reason == f.Reason)
}

func matchesEventFilter(e *k8smodel.Event, f k8sbiz.ListEventsFilter) bool {
	return e.ClusterID == f.ClusterID &&
		(f.Namespace == "" || e.Namespace == f.Namespace) &&
		(f.Type == "" || e.Type == f.Type) &&
		(f.Reason == "" || e.Reason == f.Reason) &&
		(f.InvolvedKind == "" || e.InvolvedKind == f.InvolvedKind) &&
		(f.InvolvedName == "" || e.InvolvedName == f.InvolvedName)
}

func limitClusters(in []*k8smodel.Cluster, limit, offset int) []*k8smodel.Cluster {
	if offset > len(in) {
		return nil
	}
	in = in[offset:]
	if limit > 0 && limit < len(in) {
		return in[:limit]
	}
	return in
}

func limitWorkloads(in []*k8smodel.Workload, limit, offset int) []*k8smodel.Workload {
	if offset > len(in) {
		return nil
	}
	in = in[offset:]
	if limit > 0 && limit < len(in) {
		return in[:limit]
	}
	return in
}

func limitPods(in []*k8smodel.Pod, limit, offset int) []*k8smodel.Pod {
	if offset > len(in) {
		return nil
	}
	in = in[offset:]
	if limit > 0 && limit < len(in) {
		return in[:limit]
	}
	return in
}

func limitEvents(in []*k8smodel.Event, limit, offset int) []*k8smodel.Event {
	if offset > len(in) {
		return nil
	}
	in = in[offset:]
	if limit > 0 && limit < len(in) {
		return in[:limit]
	}
	return in
}
