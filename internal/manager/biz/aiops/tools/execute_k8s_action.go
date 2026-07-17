package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const ToolNameExecuteK8sAction = "execute_k8s_action"

const ExecuteK8sActionDescription = "Execute a small, audited Kubernetes write through the cluster controller edge. " +
	"MUTATING — calls trigger reviewer approval and require a successful dry-run preflight before dispatch."

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
      "description": "Set true first. A successful Kubernetes dryRun=All returns a short-lived preflight_token and expected_resource_version required by the real write."
    },
    "preflight_token": {
      "type": "string",
      "description": "One-time token returned by a matching dry-run. Required when dry_run is false."
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
	"Always call it first with dry_run=true, then repeat the same action with the returned preflight_token and expected_resource_version; the manager rejects direct writes, changed parameters, stale versions, and reused tokens. " +
	"For drain, keep ignore_daemonsets=true by default, set delete_emptydir_data or force only when the user explicitly accepts that risk, and avoid disable_eviction unless bypassing PDB is explicitly requested. " +
	"Always prefer describe_k8s_resource first when the target or resourceVersion is unclear. " +
	"Never use this for kubectl exec, Secret reads, arbitrary patches, or host service restarts."

const executeK8sActionCallTimeout = 30 * time.Second
const (
	executeK8sActionPreflightTTL  = 5 * time.Minute
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

	preflightMu sync.Mutex
	preflights  map[string]executeK8sActionPreflightGrant
}

type executeK8sActionPreflightGrant struct {
	Fingerprint [sha256.Size]byte
	UserID      uint64
	SessionID   string
	ExpiresAt   time.Time
}

func NewExecuteK8sActionTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *ExecuteK8sActionTool {
	if log == nil {
		log = slog.Default()
	}
	return &ExecuteK8sActionTool{
		caller:     caller,
		reader:     reader,
		log:        log,
		preflights: make(map[string]executeK8sActionPreflightGrant),
	}
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
	PreflightToken          string `json:"preflight_token,omitempty"`
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
	Source                  string                          `json:"source"`
	ControllerEdgeID        uint64                          `json:"controller_edge_id"`
	Result                  tunnel.KubernetesActionResponse `json:"result"`
	PreflightToken          string                          `json:"preflight_token,omitempty"`
	PreflightExpiresAt      string                          `json:"preflight_expires_at,omitempty"`
	ExpectedResourceVersion string                          `json:"expected_resource_version,omitempty"`
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
	if !req.DryRun {
		if err := t.consumePreflight(strings.TrimSpace(in.PreflightToken), req, caller.UserID, basetool.SessionIDFromContext(ctx)); err != nil {
			return "", err
		}
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
	if err := validateExecuteK8sActionResponse(req, resp); err != nil {
		return "", err
	}
	result := executeK8sActionResponse{
		Source:           "kubernetes_api",
		ControllerEdgeID: *cluster.ControllerEdgeID,
		Result:           resp,
	}
	if req.DryRun {
		if !resp.DryRun || resp.Applied || !resp.Preflight.Exists || strings.TrimSpace(resp.Preflight.ResourceVersion) == "" {
			return "", fmt.Errorf("%s: controller returned an invalid dry-run preflight", ToolNameExecuteK8sAction)
		}
		approved := req
		approved.DryRun = false
		approved.ExpectedResourceVersion = strings.TrimSpace(resp.Preflight.ResourceVersion)
		token, expiresAt, err := t.issuePreflight(approved, caller.UserID, basetool.SessionIDFromContext(ctx))
		if err != nil {
			return "", err
		}
		result.PreflightToken = token
		result.PreflightExpiresAt = expiresAt.UTC().Format(time.RFC3339)
		result.ExpectedResourceVersion = approved.ExpectedResourceVersion
	} else if resp.DryRun || !resp.Applied {
		return "", fmt.Errorf("%s: controller did not confirm that the write was applied", ToolNameExecuteK8sAction)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameExecuteK8sAction, err)
	}
	return string(out), nil
}

func (t *ExecuteK8sActionTool) issuePreflight(req tunnel.KubernetesActionRequest, userID uint64, sessionID string) (string, time.Time, error) {
	fingerprint, err := executeK8sActionFingerprint(req)
	if err != nil {
		return "", time.Time{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("%s: generate preflight token: %w", ToolNameExecuteK8sAction, err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	now := time.Now()
	expiresAt := now.Add(executeK8sActionPreflightTTL)

	t.preflightMu.Lock()
	defer t.preflightMu.Unlock()
	t.deleteExpiredPreflightsLocked(now)
	t.preflights[token] = executeK8sActionPreflightGrant{
		Fingerprint: fingerprint,
		UserID:      userID,
		SessionID:   sessionID,
		ExpiresAt:   expiresAt,
	}
	return token, expiresAt, nil
}

func (t *ExecuteK8sActionTool) consumePreflight(token string, req tunnel.KubernetesActionRequest, userID uint64, sessionID string) error {
	if token == "" {
		return fmt.Errorf("%s: preflight_token is required; run the same action with dry_run=true first", ToolNameExecuteK8sAction)
	}
	fingerprint, err := executeK8sActionFingerprint(req)
	if err != nil {
		return err
	}
	now := time.Now()
	t.preflightMu.Lock()
	defer t.preflightMu.Unlock()
	grant, ok := t.preflights[token]
	delete(t.preflights, token)
	t.deleteExpiredPreflightsLocked(now)
	if !ok || !grant.ExpiresAt.After(now) {
		return fmt.Errorf("%s: preflight_token is invalid, expired, or already used", ToolNameExecuteK8sAction)
	}
	if grant.UserID != userID || grant.SessionID != sessionID {
		return fmt.Errorf("%s: preflight_token belongs to a different user or session", ToolNameExecuteK8sAction)
	}
	if grant.Fingerprint != fingerprint {
		return fmt.Errorf("%s: action parameters changed after dry-run; run dry_run=true again", ToolNameExecuteK8sAction)
	}
	return nil
}

func (t *ExecuteK8sActionTool) deleteExpiredPreflightsLocked(now time.Time) {
	for token, grant := range t.preflights {
		if !grant.ExpiresAt.After(now) {
			delete(t.preflights, token)
		}
	}
}

func executeK8sActionFingerprint(req tunnel.KubernetesActionRequest) ([sha256.Size]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("%s: fingerprint request: %w", ToolNameExecuteK8sAction, err)
	}
	return sha256.Sum256(body), nil
}

func validateExecuteK8sActionResponse(req tunnel.KubernetesActionRequest, resp tunnel.KubernetesActionResponse) error {
	if resp.ClusterID != req.ClusterID || resp.Action != req.Action || resp.Name != req.Name || resp.Namespace != req.Namespace {
		return fmt.Errorf("%s: controller response target does not match the request", ToolNameExecuteK8sAction)
	}
	if resp.Preflight.Name != req.Name || resp.Preflight.Namespace != req.Namespace {
		return fmt.Errorf("%s: controller preflight target does not match the request", ToolNameExecuteK8sAction)
	}
	return nil
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
