package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameDescribeK8sResource = "describe_k8s_resource"

const DescribeK8sResourceDescription = "Read one Kubernetes resource live through the cluster controller edge. " +
	"Use only when the user asks for describe/latest/live state or when DB snapshot freshness is not enough."

var DescribeK8sResourceSchema = json.RawMessage(`{
  "type": "object",
  "required": ["cluster_id", "kind", "name"],
  "properties": {
    "cluster_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Kubernetes cluster id in ongrid."
    },
    "kind": {
      "type": "string",
      "description": "Resource kind: Pod, Node, Namespace, Service, Deployment, StatefulSet, DaemonSet, ReplicaSet, Job, CronJob, or Event."
    },
    "api_version": {
      "type": "string",
      "description": "Optional apiVersion override. Defaults from kind, for example v1, apps/v1, batch/v1."
    },
    "namespace": {
      "type": "string",
      "description": "Namespace for namespaced resources. Required for Pod/Service/Deployment/StatefulSet/DaemonSet/ReplicaSet/Job/CronJob/Event."
    },
    "name": {
      "type": "string",
      "description": "Resource name."
    },
    "include_events": {
      "type": "boolean",
      "description": "Whether to include related Events. Default true."
    },
    "events_limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Max related Events to return. Default 20."
    }
  }
}`)

const describeK8sResourceWhenToUse = "When the user explicitly asks to describe a Kubernetes resource, asks for latest/live state, " +
	"or needs current Events for one named object. NOT for counts/lists/overview (use query_k8s_snapshot). " +
	"Read-only only; does not execute kubectl, exec, logs, scale, restart, or delete."

const describeK8sResourceCallTimeout = 15 * time.Second

type DescribeK8sResourceTool struct {
	caller Caller
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewDescribeK8sResourceTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *DescribeK8sResourceTool {
	if log == nil {
		log = slog.Default()
	}
	return &DescribeK8sResourceTool{caller: caller, reader: reader, log: log}
}

func (t *DescribeK8sResourceTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameDescribeK8sResource,
		Description: DescribeK8sResourceDescription,
		WhenToUse:   describeK8sResourceWhenToUse,
		Parameters:  DescribeK8sResourceSchema,
		Class:       "read",
	}, nil
}

type DescribeK8sResourceArgs struct {
	ClusterID     uint64 `json:"cluster_id"`
	Kind          string `json:"kind"`
	APIVersion    string `json:"api_version,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Name          string `json:"name"`
	IncludeEvents *bool  `json:"include_events,omitempty"`
	EventsLimit   int    `json:"events_limit,omitempty"`
}

type describeK8sResourceResponse struct {
	Source           string                                    `json:"source"`
	ControllerEdgeID uint64                                    `json:"controller_edge_id"`
	Result           tunnel.KubernetesDescribeResourceResponse `json:"result"`
}

func (t *DescribeK8sResourceTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameDescribeK8sResource)
	}
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameDescribeK8sResource)
	}
	var in DescribeK8sResourceArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameDescribeK8sResource, err)
	}
	req, err := normalizeDescribeK8sResourceArgs(in)
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, describeK8sResourceCallTimeout)
	defer cancel()

	cluster, err := t.reader.GetCluster(callCtx, req.ClusterID)
	if err != nil {
		return "", fmt.Errorf("%s: get cluster %d: %w", ToolNameDescribeK8sResource, req.ClusterID, err)
	}
	if cluster.ControllerEdgeID == nil || *cluster.ControllerEdgeID == 0 {
		return "", fmt.Errorf("%s: cluster %d has no online controller edge", ToolNameDescribeK8sResource, req.ClusterID)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameDescribeK8sResource, err)
	}
	respBody, err := t.caller.Call(callCtx, *cluster.ControllerEdgeID, tunnel.MethodDescribeK8sResource, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameDescribeK8sResource, err)
	}
	var resp tunnel.KubernetesDescribeResourceResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameDescribeK8sResource, err)
	}
	out, err := json.Marshal(describeK8sResourceResponse{
		Source:           "kubernetes_api",
		ControllerEdgeID: *cluster.ControllerEdgeID,
		Result:           resp,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameDescribeK8sResource, err)
	}
	return string(out), nil
}

func normalizeDescribeK8sResourceArgs(in DescribeK8sResourceArgs) (tunnel.KubernetesDescribeResourceRequest, error) {
	if in.ClusterID == 0 {
		return tunnel.KubernetesDescribeResourceRequest{}, fmt.Errorf("%s: cluster_id is required", ToolNameDescribeK8sResource)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		return tunnel.KubernetesDescribeResourceRequest{}, fmt.Errorf("%s: kind is required", ToolNameDescribeK8sResource)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return tunnel.KubernetesDescribeResourceRequest{}, fmt.Errorf("%s: name is required", ToolNameDescribeK8sResource)
	}
	eventsLimit := in.EventsLimit
	if eventsLimit <= 0 {
		eventsLimit = 20
	}
	if eventsLimit > 100 {
		eventsLimit = 100
	}
	includeEvents := true
	if in.IncludeEvents != nil {
		includeEvents = *in.IncludeEvents
	}
	return tunnel.KubernetesDescribeResourceRequest{
		ClusterID:     in.ClusterID,
		Kind:          kind,
		APIVersion:    strings.TrimSpace(in.APIVersion),
		Namespace:     strings.TrimSpace(in.Namespace),
		Name:          name,
		IncludeEvents: includeEvents,
		EventsLimit:   eventsLimit,
	}, nil
}

func (r *Registry) executeDescribeK8sResource(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	tool := NewDescribeK8sResourceTool(r.caller, r.k8sSnapshot, r.log)
	out, err := tool.InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}
