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

const ToolNameKubeGetPods = "kube_get_pods"

const KubeGetPodsDescription = "List a bounded page of live Kubernetes Pods through the cluster controller edge. " +
	"Use for current Pod state; use query_k8s_snapshot for manager snapshots."

var KubeGetPodsSchema = json.RawMessage(`{
  "type":"object", "required":["cluster_id"], "properties": {
    "cluster_id":{"type":"integer","minimum":1,"description":"Kubernetes cluster id in Ongrid."},
    "namespace":{"type":"string","description":"Optional namespace. Empty lists Pods cluster-wide."},
    "label_selector":{"type":"string","description":"Optional Kubernetes label selector, for example app=api."},
    "limit":{"type":"integer","minimum":1,"maximum":100,"description":"Page size. Default 50, maximum 100."},
    "continue":{"type":"string","description":"Continuation token returned by a previous call."}
  }
}`)

const kubeGetPodsWhenToUse = "Use for a current/live Pod list or a namespace/label-filtered Pod query. " +
	"This is read-only and paginated; it does not retrieve logs, execute commands, or change Kubernetes resources."

const kubeGetPodsCallTimeout = 15 * time.Second

type KubeGetPodsTool struct {
	caller Caller
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewKubeGetPodsTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *KubeGetPodsTool {
	if log == nil {
		log = slog.Default()
	}
	return &KubeGetPodsTool{caller: caller, reader: reader, log: log}
}

func (t *KubeGetPodsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: ToolNameKubeGetPods, Description: KubeGetPodsDescription, WhenToUse: kubeGetPodsWhenToUse, Parameters: KubeGetPodsSchema, Class: "read"}, nil
}

type KubeGetPodsArgs struct {
	ClusterID     uint64 `json:"cluster_id"`
	Namespace     string `json:"namespace,omitempty"`
	LabelSelector string `json:"label_selector,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Continue      string `json:"continue,omitempty"`
}

type kubeGetPodsResponse struct {
	Source           string                            `json:"source"`
	ControllerEdgeID uint64                            `json:"controller_edge_id"`
	Result           tunnel.KubernetesListPodsResponse `json:"result"`
}

func (t *KubeGetPodsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameKubeGetPods)
	}
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameKubeGetPods)
	}
	var in KubeGetPodsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameKubeGetPods, err)
	}
	if in.ClusterID == 0 {
		return "", fmt.Errorf("%s: cluster_id is required", ToolNameKubeGetPods)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	req := tunnel.KubernetesListPodsRequest{ClusterID: in.ClusterID, Namespace: strings.TrimSpace(in.Namespace), LabelSelector: strings.TrimSpace(in.LabelSelector), Limit: limit, Continue: strings.TrimSpace(in.Continue)}
	callCtx, cancel := context.WithTimeout(ctx, kubeGetPodsCallTimeout)
	defer cancel()
	cluster, err := t.reader.GetCluster(callCtx, req.ClusterID)
	if err != nil {
		return "", fmt.Errorf("%s: get cluster %d: %w", ToolNameKubeGetPods, req.ClusterID, err)
	}
	if cluster.ControllerEdgeID == nil || *cluster.ControllerEdgeID == 0 {
		return "", fmt.Errorf("%s: cluster %d has no online controller edge", ToolNameKubeGetPods, req.ClusterID)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameKubeGetPods, err)
	}
	respBody, err := t.caller.Call(callCtx, *cluster.ControllerEdgeID, tunnel.MethodListK8sPods, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameKubeGetPods, err)
	}
	var resp tunnel.KubernetesListPodsResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameKubeGetPods, err)
	}
	out, err := json.Marshal(kubeGetPodsResponse{Source: "kubernetes_api", ControllerEdgeID: *cluster.ControllerEdgeID, Result: resp})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameKubeGetPods, err)
	}
	return string(out), nil
}

func (r *Registry) executeKubeGetPods(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	out, err := NewKubeGetPodsTool(r.caller, r.k8sSnapshot, r.log).InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}

var _ basetool.BaseTool = (*KubeGetPodsTool)(nil)
