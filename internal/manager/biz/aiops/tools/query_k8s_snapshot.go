package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	k8sbiz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	k8smodel "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

const ToolNameQueryK8sSnapshot = "query_k8s_snapshot"

const QueryK8sSnapshotDescription = "Query the manager's Kubernetes DB snapshot for cluster, node, workload, pod, and event inventory. " +
	"Use this for counts, lists, status summaries, namespace fault triage, and K8s object correlation; it does not call the live Kubernetes API."

var QueryK8sSnapshotSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "resource": {
      "type": "string",
      "enum": ["summary", "clusters", "nodes", "workloads", "pods", "events"],
      "description": "Snapshot resource to query. Use summary for per-cluster counts. Default summary."
    },
    "cluster_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Optional cluster id. Omit to aggregate across registered clusters."
    },
    "cluster_name": {
      "type": "string",
      "description": "Optional substring filter for cluster name when cluster_id is omitted."
    },
    "cluster_status": {
      "type": "string",
      "enum": ["online", "offline", "degraded"],
      "description": "Optional cluster status filter."
    },
    "cluster_mode": {
      "type": "string",
      "enum": ["full-node", "serverless"],
      "description": "Optional cluster mode filter."
    },
    "namespace": {
      "type": "string",
      "description": "Optional namespace filter for workloads, pods, and events."
    },
    "kind": {
      "type": "string",
      "description": "Optional workload kind filter, for example Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob."
    },
    "node_name": {
      "type": "string",
      "description": "Optional node name filter for pods."
    },
    "phase": {
      "type": "string",
      "description": "Optional pod phase filter, for example Running, Pending, Failed, Succeeded."
    },
    "event_type": {
      "type": "string",
      "enum": ["Normal", "Warning"],
      "description": "Optional event type filter."
    },
    "reason": {
      "type": "string",
      "description": "Optional reason filter. For pods, use values such as CrashLoopBackOff or OOMKilled. For events, use values such as BackOff or FailedScheduling."
    },
    "involved_kind": {
      "type": "string",
      "description": "Optional involved object kind for events, for example Pod or Deployment."
    },
    "involved_name": {
      "type": "string",
      "description": "Optional involved object name for events."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 200,
      "description": "Maximum sample rows to return. Total counts are exact DB counts. Default 50."
    },
    "offset": {
      "type": "integer",
      "minimum": 0,
      "description": "Offset for sample rows. Most useful when cluster_id is set."
    }
  }
}`)

const queryK8sSnapshotWhenToUse = "When the user asks Kubernetes inventory questions such as current pod count, abnormal pods, " +
	"deployment readiness, cluster/node/workload lists, namespace fault analysis, or related Events. For namespace fault triage, " +
	"collect workloads, pods, and warning Events once, then use describe_k8s_resource only for ambiguous Pods and query_k8s_logs only for containers that actually start or restart. " +
	"This reads the manager DB snapshot by design. NOT for live describe/logs/exec/restart/scale/delete; use the live K8s/controller tools when those exist."

const queryK8sSnapshotCallTimeout = 10 * time.Second

type K8sSnapshotReader interface {
	ListClusters(ctx context.Context, f k8sbiz.ListClustersFilter) ([]*k8smodel.Cluster, error)
	GetCluster(ctx context.Context, id uint64) (*k8smodel.Cluster, error)
	ListNodes(ctx context.Context, clusterID uint64) ([]*k8smodel.Node, error)
	CountNodes(ctx context.Context, clusterID uint64) (int64, error)
	ListWorkloads(ctx context.Context, f k8sbiz.ListWorkloadsFilter) ([]*k8smodel.Workload, error)
	CountWorkloads(ctx context.Context, f k8sbiz.ListWorkloadsFilter) (int64, error)
	ListPods(ctx context.Context, f k8sbiz.ListPodsFilter) ([]*k8smodel.Pod, error)
	CountPods(ctx context.Context, f k8sbiz.ListPodsFilter) (int64, error)
	ListEvents(ctx context.Context, f k8sbiz.ListEventsFilter) ([]*k8smodel.Event, error)
	CountEvents(ctx context.Context, f k8sbiz.ListEventsFilter) (int64, error)
}

type QueryK8sSnapshotTool struct {
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewQueryK8sSnapshotTool(reader K8sSnapshotReader, log *slog.Logger) *QueryK8sSnapshotTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryK8sSnapshotTool{reader: reader, log: log}
}

func (t *QueryK8sSnapshotTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryK8sSnapshot,
		Description: QueryK8sSnapshotDescription,
		WhenToUse:   queryK8sSnapshotWhenToUse,
		Parameters:  QueryK8sSnapshotSchema,
		Class:       "read",
	}, nil
}

type QueryK8sSnapshotArgs struct {
	Resource      string `json:"resource,omitempty"`
	ClusterID     uint64 `json:"cluster_id,omitempty"`
	ClusterName   string `json:"cluster_name,omitempty"`
	ClusterStatus string `json:"cluster_status,omitempty"`
	ClusterMode   string `json:"cluster_mode,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Kind          string `json:"kind,omitempty"`
	NodeName      string `json:"node_name,omitempty"`
	Phase         string `json:"phase,omitempty"`
	EventType     string `json:"event_type,omitempty"`
	Reason        string `json:"reason,omitempty"`
	InvolvedKind  string `json:"involved_kind,omitempty"`
	InvolvedName  string `json:"involved_name,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

type k8sSnapshotResponse struct {
	Resource string         `json:"resource"`
	Source   string         `json:"source"`
	Filters  map[string]any `json:"filters,omitempty"`
	Total    int64          `json:"total"`
	Limit    int            `json:"limit,omitempty"`
	Offset   int            `json:"offset,omitempty"`

	Clusters   []k8sClusterSnapshotRow   `json:"clusters,omitempty"`
	Nodes      []k8sNodeSnapshotRow      `json:"nodes,omitempty"`
	Workloads  []k8sWorkloadSnapshotRow  `json:"workloads,omitempty"`
	Pods       []k8sPodSnapshotRow       `json:"pods,omitempty"`
	Events     []k8sEventSnapshotRow     `json:"events,omitempty"`
	Totals     *k8sClusterCountSnapshot  `json:"totals,omitempty"`
	PerCluster []k8sClusterCountSnapshot `json:"per_cluster,omitempty"`
	Truncated  bool                      `json:"truncated,omitempty"`
}

type k8sClusterCountSnapshot struct {
	ClusterID uint64 `json:"cluster_id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	Status    string `json:"status"`
	Nodes     int64  `json:"nodes,omitempty"`
	Workloads int64  `json:"workloads,omitempty"`
	Pods      int64  `json:"pods,omitempty"`
	Events    int64  `json:"events,omitempty"`
	Total     int64  `json:"total,omitempty"`
}

type k8sClusterSnapshotRow struct {
	ClusterID        uint64     `json:"cluster_id"`
	Name             string     `json:"name"`
	UID              *string    `json:"uid,omitempty"`
	Mode             string     `json:"mode"`
	Status           string     `json:"status"`
	ControllerEdgeID *uint64    `json:"controller_edge_id,omitempty"`
	Version          string     `json:"version,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
}

type k8sNodeSnapshotRow struct {
	ClusterID      uint64     `json:"cluster_id"`
	Name           string     `json:"name"`
	UID            string     `json:"uid"`
	ProviderID     string     `json:"provider_id,omitempty"`
	EdgeID         *uint64    `json:"edge_id,omitempty"`
	DeviceID       *uint64    `json:"device_id,omitempty"`
	KubeletVersion string     `json:"kubelet_version,omitempty"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
}

type k8sWorkloadSnapshotRow struct {
	ClusterID       uint64     `json:"cluster_id"`
	Namespace       string     `json:"namespace"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	UID             string     `json:"uid,omitempty"`
	DesiredReplicas int        `json:"desired_replicas"`
	ReadyReplicas   int        `json:"ready_replicas"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
}

type k8sPodSnapshotRow struct {
	ClusterID    uint64     `json:"cluster_id"`
	Namespace    string     `json:"namespace"`
	Name         string     `json:"name"`
	UID          string     `json:"uid,omitempty"`
	NodeName     string     `json:"node_name,omitempty"`
	Phase        string     `json:"phase,omitempty"`
	OwnerKind    string     `json:"owner_kind,omitempty"`
	OwnerName    string     `json:"owner_name,omitempty"`
	RestartCount int        `json:"restart_count"`
	Reason       string     `json:"reason,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
}

type k8sEventSnapshotRow struct {
	ClusterID         uint64     `json:"cluster_id"`
	Namespace         string     `json:"namespace"`
	Name              string     `json:"name"`
	UID               string     `json:"uid,omitempty"`
	Type              string     `json:"type,omitempty"`
	Reason            string     `json:"reason,omitempty"`
	Message           string     `json:"message,omitempty"`
	InvolvedKind      string     `json:"involved_kind,omitempty"`
	InvolvedNamespace string     `json:"involved_namespace,omitempty"`
	InvolvedName      string     `json:"involved_name,omitempty"`
	InvolvedUID       string     `json:"involved_uid,omitempty"`
	Count             int        `json:"count,omitempty"`
	LastTimestamp     *time.Time `json:"last_timestamp,omitempty"`
	EventTime         *time.Time `json:"event_time,omitempty"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
}

func (t *QueryK8sSnapshotTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameQueryK8sSnapshot)
	}
	var in QueryK8sSnapshotArgs
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
			return "", fmt.Errorf("%s: bad args: %w", ToolNameQueryK8sSnapshot, err)
		}
	}
	normalizeQueryK8sSnapshotArgs(&in)

	callCtx, cancel := context.WithTimeout(ctx, queryK8sSnapshotCallTimeout)
	defer cancel()

	out, err := t.run(callCtx, in)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameQueryK8sSnapshot, err)
	}
	return string(b), nil
}

func (t *QueryK8sSnapshotTool) run(ctx context.Context, in QueryK8sSnapshotArgs) (*k8sSnapshotResponse, error) {
	clusters, err := t.selectClusters(ctx, in)
	if err != nil {
		return nil, err
	}
	resp := &k8sSnapshotResponse{
		Resource: in.Resource,
		Source:   "manager_db_snapshot",
		Filters:  queryK8sSnapshotFilters(in),
		Limit:    in.Limit,
		Offset:   in.Offset,
	}

	switch in.Resource {
	case "summary":
		return t.querySummary(ctx, clusters, in, resp)
	case "clusters":
		rows := make([]k8sClusterSnapshotRow, 0, len(clusters))
		for _, c := range clusters {
			rows = append(rows, clusterSnapshotRow(c))
		}
		resp.Total = int64(len(rows))
		resp.Clusters = rows
		return resp, nil
	case "nodes":
		return t.queryNodes(ctx, clusters, in, resp)
	case "workloads":
		return t.queryWorkloads(ctx, clusters, in, resp)
	case "pods":
		return t.queryPods(ctx, clusters, in, resp)
	case "events":
		return t.queryEvents(ctx, clusters, in, resp)
	default:
		return nil, fmt.Errorf("%s: invalid resource %q", ToolNameQueryK8sSnapshot, in.Resource)
	}
}

func (t *QueryK8sSnapshotTool) selectClusters(ctx context.Context, in QueryK8sSnapshotArgs) ([]*k8smodel.Cluster, error) {
	if in.ClusterID > 0 {
		c, err := t.reader.GetCluster(ctx, in.ClusterID)
		if err != nil {
			return nil, fmt.Errorf("%s: get cluster %d: %w", ToolNameQueryK8sSnapshot, in.ClusterID, err)
		}
		return []*k8smodel.Cluster{c}, nil
	}
	items, err := t.reader.ListClusters(ctx, k8sbiz.ListClustersFilter{
		Name:   in.ClusterName,
		Status: in.ClusterStatus,
		Mode:   in.ClusterMode,
		Limit:  100,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: list clusters: %w", ToolNameQueryK8sSnapshot, err)
	}
	return items, nil
}

func (t *QueryK8sSnapshotTool) querySummary(ctx context.Context, clusters []*k8smodel.Cluster, in QueryK8sSnapshotArgs, resp *k8sSnapshotResponse) (*k8sSnapshotResponse, error) {
	resp.PerCluster = make([]k8sClusterCountSnapshot, 0, len(clusters))
	totals := &k8sClusterCountSnapshot{}
	for _, c := range clusters {
		row, err := t.countCluster(ctx, c, in)
		if err != nil {
			return nil, err
		}
		resp.PerCluster = append(resp.PerCluster, row)
		totals.Nodes += row.Nodes
		totals.Workloads += row.Workloads
		totals.Pods += row.Pods
		totals.Events += row.Events
	}
	resp.Total = int64(len(clusters))
	resp.Totals = totals
	return resp, nil
}

func (t *QueryK8sSnapshotTool) countCluster(ctx context.Context, c *k8smodel.Cluster, in QueryK8sSnapshotArgs) (k8sClusterCountSnapshot, error) {
	row := k8sClusterCountSnapshot{ClusterID: c.ID, Name: c.Name, Mode: c.Mode, Status: c.Status}
	nodes, err := t.reader.CountNodes(ctx, c.ID)
	if err != nil {
		return row, fmt.Errorf("%s: count nodes for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
	}
	workloads, err := t.reader.CountWorkloads(ctx, k8sbiz.ListWorkloadsFilter{
		ClusterID: c.ID,
		Namespace: in.Namespace,
		Kind:      in.Kind,
	})
	if err != nil {
		return row, fmt.Errorf("%s: count workloads for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
	}
	pods, err := t.reader.CountPods(ctx, k8sbiz.ListPodsFilter{
		ClusterID: c.ID,
		Namespace: in.Namespace,
		NodeName:  in.NodeName,
		Phase:     in.Phase,
	})
	if err != nil {
		return row, fmt.Errorf("%s: count pods for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
	}
	events, err := t.reader.CountEvents(ctx, k8sbiz.ListEventsFilter{
		ClusterID:    c.ID,
		Namespace:    in.Namespace,
		Type:         in.EventType,
		Reason:       in.Reason,
		InvolvedKind: in.InvolvedKind,
		InvolvedName: in.InvolvedName,
	})
	if err != nil {
		return row, fmt.Errorf("%s: count events for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
	}
	row.Nodes = nodes
	row.Workloads = workloads
	row.Pods = pods
	row.Events = events
	return row, nil
}

func (t *QueryK8sSnapshotTool) queryNodes(ctx context.Context, clusters []*k8smodel.Cluster, in QueryK8sSnapshotArgs, resp *k8sSnapshotResponse) (*k8sSnapshotResponse, error) {
	remainingOffset := in.Offset
	remainingLimit := in.Limit
	resp.Nodes = make([]k8sNodeSnapshotRow, 0, in.Limit)
	for _, c := range clusters {
		total, err := t.reader.CountNodes(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: count nodes for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		resp.Total += total
		resp.PerCluster = append(resp.PerCluster, k8sClusterCountSnapshot{
			ClusterID: c.ID,
			Name:      c.Name,
			Mode:      c.Mode,
			Status:    c.Status,
			Nodes:     total,
			Total:     total,
		})
		if remainingLimit <= 0 {
			continue
		}
		items, err := t.reader.ListNodes(ctx, c.ID)
		if err != nil {
			return nil, fmt.Errorf("%s: list nodes for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		if remainingOffset >= len(items) {
			remainingOffset -= len(items)
			continue
		}
		items = items[remainingOffset:]
		remainingOffset = 0
		for _, item := range items {
			if remainingLimit <= 0 {
				break
			}
			resp.Nodes = append(resp.Nodes, nodeSnapshotRow(item))
			remainingLimit--
		}
	}
	resp.Truncated = int64(len(resp.Nodes)) < resp.Total
	return resp, nil
}

func (t *QueryK8sSnapshotTool) queryWorkloads(ctx context.Context, clusters []*k8smodel.Cluster, in QueryK8sSnapshotArgs, resp *k8sSnapshotResponse) (*k8sSnapshotResponse, error) {
	remainingOffset := in.Offset
	remainingLimit := in.Limit
	resp.Workloads = make([]k8sWorkloadSnapshotRow, 0, in.Limit)
	for _, c := range clusters {
		filter := k8sbiz.ListWorkloadsFilter{ClusterID: c.ID, Namespace: in.Namespace, Kind: in.Kind}
		total, err := t.reader.CountWorkloads(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: count workloads for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		resp.Total += total
		resp.PerCluster = append(resp.PerCluster, k8sClusterCountSnapshot{
			ClusterID: c.ID,
			Name:      c.Name,
			Mode:      c.Mode,
			Status:    c.Status,
			Workloads: total,
			Total:     total,
		})
		if remainingLimit <= 0 || total == 0 {
			continue
		}
		if remainingOffset >= int(total) {
			remainingOffset -= int(total)
			continue
		}
		filter.Limit = remainingLimit
		filter.Offset = remainingOffset
		remainingOffset = 0
		items, err := t.reader.ListWorkloads(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: list workloads for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		for _, item := range items {
			resp.Workloads = append(resp.Workloads, workloadSnapshotRow(item))
			remainingLimit--
		}
	}
	resp.Truncated = int64(len(resp.Workloads)) < resp.Total
	return resp, nil
}

func (t *QueryK8sSnapshotTool) queryPods(ctx context.Context, clusters []*k8smodel.Cluster, in QueryK8sSnapshotArgs, resp *k8sSnapshotResponse) (*k8sSnapshotResponse, error) {
	remainingOffset := in.Offset
	remainingLimit := in.Limit
	resp.Pods = make([]k8sPodSnapshotRow, 0, in.Limit)
	for _, c := range clusters {
		filter := k8sbiz.ListPodsFilter{ClusterID: c.ID, Namespace: in.Namespace, NodeName: in.NodeName, Phase: in.Phase, Reason: in.Reason}
		total, err := t.reader.CountPods(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: count pods for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		resp.Total += total
		resp.PerCluster = append(resp.PerCluster, k8sClusterCountSnapshot{
			ClusterID: c.ID,
			Name:      c.Name,
			Mode:      c.Mode,
			Status:    c.Status,
			Pods:      total,
			Total:     total,
		})
		if remainingLimit <= 0 || total == 0 {
			continue
		}
		if remainingOffset >= int(total) {
			remainingOffset -= int(total)
			continue
		}
		filter.Limit = remainingLimit
		filter.Offset = remainingOffset
		remainingOffset = 0
		items, err := t.reader.ListPods(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: list pods for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		for _, item := range items {
			resp.Pods = append(resp.Pods, podSnapshotRow(item))
			remainingLimit--
		}
	}
	resp.Truncated = int64(len(resp.Pods)) < resp.Total
	return resp, nil
}

func (t *QueryK8sSnapshotTool) queryEvents(ctx context.Context, clusters []*k8smodel.Cluster, in QueryK8sSnapshotArgs, resp *k8sSnapshotResponse) (*k8sSnapshotResponse, error) {
	remainingOffset := in.Offset
	remainingLimit := in.Limit
	resp.Events = make([]k8sEventSnapshotRow, 0, in.Limit)
	for _, c := range clusters {
		filter := k8sbiz.ListEventsFilter{
			ClusterID:    c.ID,
			Namespace:    in.Namespace,
			Type:         in.EventType,
			Reason:       in.Reason,
			InvolvedKind: in.InvolvedKind,
			InvolvedName: in.InvolvedName,
		}
		total, err := t.reader.CountEvents(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: count events for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		resp.Total += total
		resp.PerCluster = append(resp.PerCluster, k8sClusterCountSnapshot{
			ClusterID: c.ID,
			Name:      c.Name,
			Mode:      c.Mode,
			Status:    c.Status,
			Events:    total,
			Total:     total,
		})
		if remainingLimit <= 0 || total == 0 {
			continue
		}
		if remainingOffset >= int(total) {
			remainingOffset -= int(total)
			continue
		}
		filter.Limit = remainingLimit
		filter.Offset = remainingOffset
		remainingOffset = 0
		items, err := t.reader.ListEvents(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("%s: list events for cluster %d: %w", ToolNameQueryK8sSnapshot, c.ID, err)
		}
		for _, item := range items {
			resp.Events = append(resp.Events, eventSnapshotRow(item))
			remainingLimit--
		}
	}
	resp.Truncated = int64(len(resp.Events)) < resp.Total
	return resp, nil
}

func normalizeQueryK8sSnapshotArgs(in *QueryK8sSnapshotArgs) {
	in.Resource = strings.ToLower(strings.TrimSpace(in.Resource))
	if in.Resource == "" {
		in.Resource = "summary"
	}
	in.ClusterName = strings.TrimSpace(in.ClusterName)
	in.ClusterStatus = strings.TrimSpace(in.ClusterStatus)
	in.ClusterMode = strings.TrimSpace(in.ClusterMode)
	in.Namespace = strings.TrimSpace(in.Namespace)
	in.Kind = strings.TrimSpace(in.Kind)
	in.NodeName = strings.TrimSpace(in.NodeName)
	in.Phase = strings.TrimSpace(in.Phase)
	in.EventType = strings.TrimSpace(in.EventType)
	in.Reason = strings.TrimSpace(in.Reason)
	in.InvolvedKind = strings.TrimSpace(in.InvolvedKind)
	in.InvolvedName = strings.TrimSpace(in.InvolvedName)
	if in.Limit <= 0 {
		in.Limit = 50
	}
	if in.Limit > 200 {
		in.Limit = 200
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
}

func queryK8sSnapshotFilters(in QueryK8sSnapshotArgs) map[string]any {
	f := map[string]any{}
	if in.ClusterID > 0 {
		f["cluster_id"] = in.ClusterID
	}
	addNonEmptyFilter(f, "cluster_name", in.ClusterName)
	addNonEmptyFilter(f, "cluster_status", in.ClusterStatus)
	addNonEmptyFilter(f, "cluster_mode", in.ClusterMode)
	addNonEmptyFilter(f, "namespace", in.Namespace)
	addNonEmptyFilter(f, "kind", in.Kind)
	addNonEmptyFilter(f, "node_name", in.NodeName)
	addNonEmptyFilter(f, "phase", in.Phase)
	addNonEmptyFilter(f, "event_type", in.EventType)
	addNonEmptyFilter(f, "reason", in.Reason)
	addNonEmptyFilter(f, "involved_kind", in.InvolvedKind)
	addNonEmptyFilter(f, "involved_name", in.InvolvedName)
	if len(f) == 0 {
		return nil
	}
	return f
}

func addNonEmptyFilter(f map[string]any, key, value string) {
	if value != "" {
		f[key] = value
	}
}

func clusterSnapshotRow(c *k8smodel.Cluster) k8sClusterSnapshotRow {
	return k8sClusterSnapshotRow{
		ClusterID:        c.ID,
		Name:             c.Name,
		UID:              c.UID,
		Mode:             c.Mode,
		Status:           c.Status,
		ControllerEdgeID: c.ControllerEdgeID,
		Version:          c.Version,
		LastSeenAt:       c.LastSeenAt,
	}
}

func nodeSnapshotRow(n *k8smodel.Node) k8sNodeSnapshotRow {
	return k8sNodeSnapshotRow{
		ClusterID:      n.ClusterID,
		Name:           n.NodeName,
		UID:            n.NodeUID,
		ProviderID:     n.ProviderID,
		EdgeID:         n.EdgeID,
		DeviceID:       n.DeviceID,
		KubeletVersion: n.KubeletVersion,
		LastSeenAt:     n.LastSeenAt,
	}
}

func workloadSnapshotRow(w *k8smodel.Workload) k8sWorkloadSnapshotRow {
	return k8sWorkloadSnapshotRow{
		ClusterID:       w.ClusterID,
		Namespace:       w.Namespace,
		Kind:            w.Kind,
		Name:            w.Name,
		UID:             w.UID,
		DesiredReplicas: w.DesiredReplicas,
		ReadyReplicas:   w.ReadyReplicas,
		LastSeenAt:      w.LastSeenAt,
	}
}

func podSnapshotRow(p *k8smodel.Pod) k8sPodSnapshotRow {
	return k8sPodSnapshotRow{
		ClusterID:    p.ClusterID,
		Namespace:    p.Namespace,
		Name:         p.Name,
		UID:          p.UID,
		NodeName:     p.NodeName,
		Phase:        p.Phase,
		OwnerKind:    p.OwnerKind,
		OwnerName:    p.OwnerName,
		RestartCount: p.RestartCount,
		Reason:       p.Reason,
		LastSeenAt:   p.LastSeenAt,
	}
}

func eventSnapshotRow(e *k8smodel.Event) k8sEventSnapshotRow {
	return k8sEventSnapshotRow{
		ClusterID:         e.ClusterID,
		Namespace:         e.Namespace,
		Name:              e.Name,
		UID:               e.UID,
		Type:              e.Type,
		Reason:            e.Reason,
		Message:           e.Message,
		InvolvedKind:      e.InvolvedKind,
		InvolvedNamespace: e.InvolvedNamespace,
		InvolvedName:      e.InvolvedName,
		InvolvedUID:       e.InvolvedUID,
		Count:             e.Count,
		LastTimestamp:     e.LastTimestamp,
		EventTime:         e.EventTime,
		LastSeenAt:        e.LastSeenAt,
	}
}

func (r *Registry) executeQueryK8sSnapshot(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	tool := NewQueryK8sSnapshotTool(r.k8sSnapshot, r.log)
	out, err := tool.InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}
