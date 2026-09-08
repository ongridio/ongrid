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

const ToolNameKubeEvents = "kube_events"

const KubeEventsDescription = "List a bounded page of live Kubernetes Events through the cluster controller edge. " +
	"Use for current/live event triage; use query_k8s_snapshot for manager DB snapshots."

var KubeEventsSchema = json.RawMessage(`{
  "type":"object", "required":["cluster_id"], "properties": {
    "cluster_id":{"type":"integer","minimum":1,"description":"Kubernetes cluster id in Ongrid."},
    "namespace":{"type":"string","description":"Optional namespace. Empty lists Events cluster-wide."},
    "type":{"type":"string","enum":["Normal","Warning"],"description":"Optional event type filter."},
    "reason":{"type":"string","description":"Optional reason filter, for example BackOff or FailedScheduling."},
    "involved_kind":{"type":"string","description":"Optional involved object kind, for example Pod or Deployment."},
    "involved_name":{"type":"string","description":"Optional involved object name."},
    "limit":{"type":"integer","minimum":1,"maximum":200,"description":"Max events to return. Default 50, maximum 200."}
  }
}`)

const kubeEventsWhenToUse = "Use when the user asks about recent or live Kubernetes Events for a cluster or namespace. " +
	"This reads the live Kubernetes API and returns bounded, already-redacted Event summaries; " +
	"it is not for full object state (use describe_k8s_resource) or historical DB inventory (use query_k8s_snapshot with resource=events)."

const kubeEventsCallTimeout = 15 * time.Second

type KubeEventsTool struct {
	caller Caller
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewKubeEventsTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *KubeEventsTool {
	if log == nil {
		log = slog.Default()
	}
	return &KubeEventsTool{caller: caller, reader: reader, log: log}
}

func (t *KubeEventsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameKubeEvents,
		Description: KubeEventsDescription,
		WhenToUse:   kubeEventsWhenToUse,
		Parameters:  KubeEventsSchema,
		Class:       "read",
	}, nil
}

type KubeEventsArgs struct {
	ClusterID    uint64 `json:"cluster_id"`
	Namespace    string `json:"namespace,omitempty"`
	Type         string `json:"type,omitempty"`
	Reason       string `json:"reason,omitempty"`
	InvolvedKind string `json:"involved_kind,omitempty"`
	InvolvedName string `json:"involved_name,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

type kubeEventsResponse struct {
	Source           string                              `json:"source"`
	ControllerEdgeID uint64                              `json:"controller_edge_id"`
	Result           tunnel.KubernetesListEventsResponse `json:"result"`
}

func (t *KubeEventsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameKubeEvents)
	}
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameKubeEvents)
	}
	var in KubeEventsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameKubeEvents, err)
	}
	if in.ClusterID == 0 {
		return "", fmt.Errorf("%s: cluster_id is required", ToolNameKubeEvents)
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	req := tunnel.KubernetesListEventsRequest{
		ClusterID:    in.ClusterID,
		Namespace:    strings.TrimSpace(in.Namespace),
		Type:         strings.TrimSpace(in.Type),
		Reason:       strings.TrimSpace(in.Reason),
		InvolvedKind: strings.TrimSpace(in.InvolvedKind),
		InvolvedName: strings.TrimSpace(in.InvolvedName),
		Limit:        limit,
	}

	callCtx, cancel := context.WithTimeout(ctx, kubeEventsCallTimeout)
	defer cancel()

	cluster, err := t.reader.GetCluster(callCtx, req.ClusterID)
	if err != nil {
		return "", fmt.Errorf("%s: get cluster %d: %w", ToolNameKubeEvents, req.ClusterID, err)
	}
	if cluster.ControllerEdgeID == nil || *cluster.ControllerEdgeID == 0 {
		return "", fmt.Errorf("%s: cluster %d has no online controller edge", ToolNameKubeEvents, req.ClusterID)
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameKubeEvents, err)
	}
	respBody, err := t.caller.Call(callCtx, *cluster.ControllerEdgeID, tunnel.MethodListK8sEvents, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameKubeEvents, err)
	}
	var resp tunnel.KubernetesListEventsResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameKubeEvents, err)
	}

	out, err := json.Marshal(kubeEventsResponse{
		Source:           "kubernetes_api",
		ControllerEdgeID: *cluster.ControllerEdgeID,
		Result:           resp,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameKubeEvents, err)
	}
	return string(out), nil
}

func (r *Registry) executeKubeEvents(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	out, err := NewKubeEventsTool(r.caller, r.k8sSnapshot, r.log).InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}

var _ basetool.BaseTool = (*KubeEventsTool)(nil)
