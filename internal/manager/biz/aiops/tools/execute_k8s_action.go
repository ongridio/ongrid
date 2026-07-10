package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameExecuteK8sAction = "execute_k8s_action"

const ExecuteK8sActionDescription = "Execute a small, audited Kubernetes write through the cluster controller edge. " +
	"MUTATING — calls trigger reviewer approval before dispatch."

var ExecuteK8sActionSchema = json.RawMessage(`{
  "type": "object",
  "required": ["cluster_id", "action", "name"],
  "properties": {
    "cluster_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Kubernetes cluster id in ongrid."
    },
    "action": {
      "type": "string",
      "enum": ["rollout_restart", "scale", "delete_pod", "evict_pod", "cordon", "uncordon", "drain"],
      "description": "Kubernetes write action to execute."
    },
    "kind": {
      "type": "string",
      "description": "Resource kind. Required for rollout_restart and scale. Defaults to Pod for delete_pod/evict_pod and Node for cordon/uncordon/drain."
    },
    "api_version": {
      "type": "string",
      "description": "Optional apiVersion override. Defaults from kind, for example apps/v1 or v1."
    },
    "namespace": {
      "type": "string",
      "description": "Namespace for namespaced resources. Required for Deployment/StatefulSet/DaemonSet/Pod."
    },
    "name": {
      "type": "string",
      "description": "Resource name."
    },
    "replicas": {
      "type": "integer",
      "minimum": 0,
      "maximum": 10000,
      "description": "Desired replicas. Required only when action=scale."
    },
    "expected_resource_version": {
      "type": "string",
      "description": "Optional optimistic-lock resourceVersion observed by a prior describe/preflight. If set and stale, the controller refuses to mutate."
    },
    "dry_run": {
      "type": "boolean",
      "description": "When true, ask the Kubernetes API to validate the write with dryRun=All without persisting it."
    },
    "grace_period_seconds": {
      "type": "integer",
      "minimum": 0,
      "maximum": 3600,
      "description": "Optional Kubernetes DeleteOptions gracePeriodSeconds for delete_pod, evict_pod, or drain."
    },
    "drain_timeout_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 600,
      "description": "Drain-only timeout while retrying PDB-blocked evictions. Defaults to 120."
    },
    "drain_retry_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 30,
      "description": "Drain-only retry interval for PDB 429 eviction responses. Defaults to 2."
    },
    "ignore_daemonsets": {
      "type": "boolean",
      "default": true,
      "description": "Drain-only. When true, DaemonSet Pods are skipped; when false, the controller refuses drain if DaemonSet Pods are present."
    },
    "delete_emptydir_data": {
      "type": "boolean",
      "description": "Drain-only. Must be true before evicting/deleting Pods that use emptyDir volumes."
    },
    "force": {
      "type": "boolean",
      "description": "Drain-only. Allows unmanaged Pods without a controller owner to be drained."
    },
    "disable_eviction": {
      "type": "boolean",
      "description": "Drain-only. Deletes Pods directly instead of using eviction, bypassing PDB protection. Use only with explicit operator approval."
    },
    "reason": {
      "type": "string",
      "description": "Why the action is requested. Included in review/audit context."
    }
  }
}`)

const executeK8sActionWhenToUse = "Use ONLY when the user explicitly asks to mutate Kubernetes state: rollout restart a Deployment/StatefulSet/DaemonSet, " +
	"scale a Deployment/StatefulSet, delete or evict one Pod, or cordon/uncordon/drain one Node. This tool is MUTATING and is approved by a reviewer before dispatch. " +
	"For drain, keep ignore_daemonsets=true by default, set delete_emptydir_data or force only when the user explicitly accepts that risk, and avoid disable_eviction unless bypassing PDB is explicitly requested. " +
	"Always prefer describe_k8s_resource first when the target or resourceVersion is unclear. " +
	"Never use this for kubectl exec, Secret reads, arbitrary patches, or host service restarts."

const executeK8sActionCallTimeout = 30 * time.Second
const (
	defaultK8sDrainTimeoutSeconds = 120
	maxK8sDrainTimeoutSeconds     = 600
	defaultK8sDrainRetrySeconds   = 2
	maxK8sDrainRetrySeconds       = 30
	maxK8sGracePeriodSeconds      = 3600
)

type ExecuteK8sActionTool struct {
	caller Caller
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewExecuteK8sActionTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *ExecuteK8sActionTool {
	if log == nil {
		log = slog.Default()
	}
	return &ExecuteK8sActionTool{caller: caller, reader: reader, log: log}
}

func (t *ExecuteK8sActionTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameExecuteK8sAction,
		Description: ExecuteK8sActionDescription,
		WhenToUse:   executeK8sActionWhenToUse,
		Parameters:  ExecuteK8sActionSchema,
		Class:       "write",
	}, nil
}

type ExecuteK8sActionArgs struct {
	ClusterID               uint64 `json:"cluster_id"`
	Action                  string `json:"action"`
	Kind                    string `json:"kind,omitempty"`
	APIVersion              string `json:"api_version,omitempty"`
	Namespace               string `json:"namespace,omitempty"`
	Name                    string `json:"name"`
	Replicas                *int   `json:"replicas,omitempty"`
	ExpectedResourceVersion string `json:"expected_resource_version,omitempty"`
	DryRun                  bool   `json:"dry_run,omitempty"`
	Reason                  string `json:"reason,omitempty"`
	GracePeriodSeconds      *int   `json:"grace_period_seconds,omitempty"`
	DrainTimeoutSeconds     int    `json:"drain_timeout_seconds,omitempty"`
	DrainRetrySeconds       int    `json:"drain_retry_seconds,omitempty"`
	IgnoreDaemonSets        *bool  `json:"ignore_daemonsets,omitempty"`
	DeleteEmptyDirData      bool   `json:"delete_emptydir_data,omitempty"`
	Force                   bool   `json:"force,omitempty"`
	DisableEviction         bool   `json:"disable_eviction,omitempty"`
}

type executeK8sActionResponse struct {
	Source           string                          `json:"source"`
	ControllerEdgeID uint64                          `json:"controller_edge_id"`
	Result           tunnel.KubernetesActionResponse `json:"result"`
}

func (t *ExecuteK8sActionTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	caller, ok := tenantctx.From(ctx)
	if !ok || (caller.Role != "admin" && !caller.IsSuperuser) {
		return "", fmt.Errorf("%w: admin role required to execute kubernetes actions", errs.ErrForbidden)
	}
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameExecuteK8sAction)
	}
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameExecuteK8sAction)
	}
	var in ExecuteK8sActionArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameExecuteK8sAction, err)
	}
	req, err := normalizeExecuteK8sActionArgs(in)
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, executeK8sActionTimeout(req))
	defer cancel()

	cluster, err := t.reader.GetCluster(callCtx, req.ClusterID)
	if err != nil {
		return "", fmt.Errorf("%s: get cluster %d: %w", ToolNameExecuteK8sAction, req.ClusterID, err)
	}
	if cluster.ControllerEdgeID == nil || *cluster.ControllerEdgeID == 0 {
		return "", fmt.Errorf("%s: cluster %d has no online controller edge", ToolNameExecuteK8sAction, req.ClusterID)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameExecuteK8sAction, err)
	}
	respBody, err := t.caller.Call(callCtx, *cluster.ControllerEdgeID, tunnel.MethodExecuteK8sAction, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameExecuteK8sAction, err)
	}
	var resp tunnel.KubernetesActionResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameExecuteK8sAction, err)
	}
	out, err := json.Marshal(executeK8sActionResponse{
		Source:           "kubernetes_api",
		ControllerEdgeID: *cluster.ControllerEdgeID,
		Result:           resp,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameExecuteK8sAction, err)
	}
	return string(out), nil
}

func normalizeExecuteK8sActionArgs(in ExecuteK8sActionArgs) (tunnel.KubernetesActionRequest, error) {
	if in.ClusterID == 0 {
		return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: cluster_id is required", ToolNameExecuteK8sAction)
	}
	action, err := normalizeExecuteK8sActionName(in.Action)
	if err != nil {
		return tunnel.KubernetesActionRequest{}, err
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" && actionRequiresExplicitKind(action) {
		return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: kind is required for %s", ToolNameExecuteK8sAction, action)
	}
	if kind == "" && (action == "delete_pod" || action == "evict_pod") {
		kind = "Pod"
	}
	if kind == "" && (action == "cordon" || action == "uncordon" || action == "drain") {
		kind = "Node"
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: name is required", ToolNameExecuteK8sAction)
	}
	if action == "scale" {
		if in.Replicas == nil {
			return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: replicas is required for scale", ToolNameExecuteK8sAction)
		}
		if *in.Replicas < 0 || *in.Replicas > 10000 {
			return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: replicas must be between 0 and 10000", ToolNameExecuteK8sAction)
		}
	}
	gracePeriodSeconds := in.GracePeriodSeconds
	if gracePeriodSeconds != nil && (*gracePeriodSeconds < 0 || *gracePeriodSeconds > maxK8sGracePeriodSeconds) {
		return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: grace_period_seconds must be between 0 and %d", ToolNameExecuteK8sAction, maxK8sGracePeriodSeconds)
	}
	drainTimeoutSeconds := 0
	drainRetrySeconds := 0
	var ignoreDaemonSets *bool
	if action == "drain" {
		drainTimeoutSeconds = in.DrainTimeoutSeconds
		if drainTimeoutSeconds == 0 {
			drainTimeoutSeconds = defaultK8sDrainTimeoutSeconds
		}
		if drainTimeoutSeconds < 1 || drainTimeoutSeconds > maxK8sDrainTimeoutSeconds {
			return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: drain_timeout_seconds must be between 1 and %d", ToolNameExecuteK8sAction, maxK8sDrainTimeoutSeconds)
		}
		drainRetrySeconds = in.DrainRetrySeconds
		if drainRetrySeconds == 0 {
			drainRetrySeconds = defaultK8sDrainRetrySeconds
		}
		if drainRetrySeconds < 1 || drainRetrySeconds > maxK8sDrainRetrySeconds {
			return tunnel.KubernetesActionRequest{}, fmt.Errorf("%s: drain_retry_seconds must be between 1 and %d", ToolNameExecuteK8sAction, maxK8sDrainRetrySeconds)
		}
		ignoreDaemonSetsValue := true
		if in.IgnoreDaemonSets != nil {
			ignoreDaemonSetsValue = *in.IgnoreDaemonSets
		}
		ignoreDaemonSets = &ignoreDaemonSetsValue
	}
	return tunnel.KubernetesActionRequest{
		ClusterID:               in.ClusterID,
		Action:                  action,
		Kind:                    kind,
		APIVersion:              strings.TrimSpace(in.APIVersion),
		Namespace:               strings.TrimSpace(in.Namespace),
		Name:                    name,
		Replicas:                in.Replicas,
		ExpectedResourceVersion: strings.TrimSpace(in.ExpectedResourceVersion),
		DryRun:                  in.DryRun,
		Reason:                  strings.TrimSpace(in.Reason),
		GracePeriodSeconds:      gracePeriodSeconds,
		DrainTimeoutSeconds:     drainTimeoutSeconds,
		DrainRetrySeconds:       drainRetrySeconds,
		IgnoreDaemonSets:        ignoreDaemonSets,
		DeleteEmptyDirData:      in.DeleteEmptyDirData,
		Force:                   in.Force,
		DisableEviction:         in.DisableEviction,
	}, nil
}

func executeK8sActionTimeout(req tunnel.KubernetesActionRequest) time.Duration {
	if req.Action != "drain" || req.DrainTimeoutSeconds <= 0 {
		return executeK8sActionCallTimeout
	}
	timeout := time.Duration(req.DrainTimeoutSeconds+15) * time.Second
	if timeout < executeK8sActionCallTimeout {
		return executeK8sActionCallTimeout
	}
	return timeout
}

func normalizeExecuteK8sActionName(action string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(action, "-", "_"))) {
	case "rollout_restart", "restart", "rolloutrestart":
		return "rollout_restart", nil
	case "scale":
		return "scale", nil
	case "delete_pod", "delete":
		return "delete_pod", nil
	case "evict_pod", "evict":
		return "evict_pod", nil
	case "cordon":
		return "cordon", nil
	case "uncordon":
		return "uncordon", nil
	case "drain":
		return "drain", nil
	default:
		return "", fmt.Errorf("%s: unsupported action %q", ToolNameExecuteK8sAction, action)
	}
}

func actionRequiresExplicitKind(action string) bool {
	switch action {
	case "rollout_restart", "scale":
		return true
	default:
		return false
	}
}
