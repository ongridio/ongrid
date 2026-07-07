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

const ToolNameQueryK8sLogs = "query_k8s_logs"

const (
	defaultPodLogTailLines    = 100
	defaultPodLogLimitBytes   = 16 * 1024
	defaultPodLogSinceSeconds = int64(3600)
	maxPodLogTailLines        = 500
	maxPodLogLimitBytes       = 64 * 1024
	maxPodLogSinceSeconds     = int64(24 * 3600)
)

const QueryK8sLogsDescription = "Read a bounded slice of one Kubernetes Pod's logs through the cluster controller edge. " +
	"Use as a small troubleshooting fallback when Loki or the log gateway has no data."

var QueryK8sLogsSchema = json.RawMessage(`{
  "type": "object",
  "required": ["cluster_id", "namespace", "pod"],
  "properties": {
    "cluster_id": {
      "type": "integer",
      "minimum": 1,
      "description": "Kubernetes cluster id in ongrid."
    },
    "namespace": {
      "type": "string",
      "description": "Pod namespace."
    },
    "pod": {
      "type": "string",
      "description": "Pod name."
    },
    "container": {
      "type": "string",
      "description": "Optional container name for multi-container Pods."
    },
    "previous": {
      "type": "boolean",
      "description": "Read the previous terminated container logs. Useful for CrashLoopBackOff."
    },
    "since_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 86400,
      "description": "Only return logs newer than this many seconds. Default 3600, max 86400."
    },
    "tail_lines": {
      "type": "integer",
      "minimum": 1,
      "maximum": 500,
      "description": "Max log lines to return. Default 100, max 500."
    },
    "limit_bytes": {
      "type": "integer",
      "minimum": 1024,
      "maximum": 65536,
      "description": "Max bytes to return. Default 16384, max 65536."
    },
    "timestamps": {
      "type": "boolean",
      "description": "Ask Kubernetes to prefix log lines with timestamps. Default true."
    }
  }
}`)

const queryK8sLogsWhenToUse = "Use when the user asks for recent Pod logs, CrashLoopBackOff output, or application stdout/stderr for one named Pod. " +
	"This is a bounded live Kubernetes pods/log fallback; prefer query_logql for production log search and query_k8s_snapshot for counts/lists. " +
	"Never use this for kubectl exec, Secret reads, arbitrary files, or high-volume log export."

const queryK8sLogsCallTimeout = 20 * time.Second

type QueryK8sLogsTool struct {
	caller Caller
	reader K8sSnapshotReader
	log    *slog.Logger
}

func NewQueryK8sLogsTool(caller Caller, reader K8sSnapshotReader, log *slog.Logger) *QueryK8sLogsTool {
	if log == nil {
		log = slog.Default()
	}
	return &QueryK8sLogsTool{caller: caller, reader: reader, log: log}
}

func (t *QueryK8sLogsTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameQueryK8sLogs,
		Description: QueryK8sLogsDescription,
		WhenToUse:   queryK8sLogsWhenToUse,
		Parameters:  QueryK8sLogsSchema,
		Class:       "read",
	}, nil
}

type QueryK8sLogsArgs struct {
	ClusterID    uint64 `json:"cluster_id"`
	Namespace    string `json:"namespace"`
	Pod          string `json:"pod"`
	Container    string `json:"container,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	SinceSeconds int64  `json:"since_seconds,omitempty"`
	TailLines    int    `json:"tail_lines,omitempty"`
	LimitBytes   int    `json:"limit_bytes,omitempty"`
	Timestamps   *bool  `json:"timestamps,omitempty"`
}

type queryK8sLogsResponse struct {
	Source           string                           `json:"source"`
	ControllerEdgeID uint64                           `json:"controller_edge_id"`
	Result           tunnel.KubernetesPodLogsResponse `json:"result"`
}

func (t *QueryK8sLogsTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameQueryK8sLogs)
	}
	if t.reader == nil {
		return "", fmt.Errorf("%s: k8s snapshot reader not configured", ToolNameQueryK8sLogs)
	}
	var in QueryK8sLogsArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameQueryK8sLogs, err)
	}
	req, err := normalizeQueryK8sLogsArgs(in)
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, queryK8sLogsCallTimeout)
	defer cancel()

	cluster, err := t.reader.GetCluster(callCtx, req.ClusterID)
	if err != nil {
		return "", fmt.Errorf("%s: get cluster %d: %w", ToolNameQueryK8sLogs, req.ClusterID, err)
	}
	if cluster.ControllerEdgeID == nil || *cluster.ControllerEdgeID == 0 {
		return "", fmt.Errorf("%s: cluster %d has no online controller edge", ToolNameQueryK8sLogs, req.ClusterID)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: marshal request: %w", ToolNameQueryK8sLogs, err)
	}
	respBody, err := t.caller.Call(callCtx, *cluster.ControllerEdgeID, tunnel.MethodQueryK8sLogs, body)
	if err != nil {
		return "", fmt.Errorf("%s: dispatch: %w", ToolNameQueryK8sLogs, err)
	}
	var resp tunnel.KubernetesPodLogsResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode response: %w", ToolNameQueryK8sLogs, err)
	}
	out, err := json.Marshal(queryK8sLogsResponse{
		Source:           "kubernetes_pods_log",
		ControllerEdgeID: *cluster.ControllerEdgeID,
		Result:           resp,
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameQueryK8sLogs, err)
	}
	return string(out), nil
}

func normalizeQueryK8sLogsArgs(in QueryK8sLogsArgs) (tunnel.KubernetesPodLogsRequest, error) {
	if in.ClusterID == 0 {
		return tunnel.KubernetesPodLogsRequest{}, fmt.Errorf("%s: cluster_id is required", ToolNameQueryK8sLogs)
	}
	namespace := strings.TrimSpace(in.Namespace)
	if namespace == "" {
		return tunnel.KubernetesPodLogsRequest{}, fmt.Errorf("%s: namespace is required", ToolNameQueryK8sLogs)
	}
	pod := strings.TrimSpace(in.Pod)
	if pod == "" {
		return tunnel.KubernetesPodLogsRequest{}, fmt.Errorf("%s: pod is required", ToolNameQueryK8sLogs)
	}
	req := tunnel.KubernetesPodLogsRequest{
		ClusterID:    in.ClusterID,
		Namespace:    namespace,
		Pod:          pod,
		Container:    strings.TrimSpace(in.Container),
		Previous:     in.Previous,
		SinceSeconds: boundedInt64(in.SinceSeconds, defaultPodLogSinceSeconds, maxPodLogSinceSeconds),
		TailLines:    boundedInt(in.TailLines, defaultPodLogTailLines, maxPodLogTailLines),
		LimitBytes:   boundedInt(in.LimitBytes, defaultPodLogLimitBytes, maxPodLogLimitBytes),
		Timestamps:   true,
	}
	if in.Timestamps != nil {
		req.Timestamps = *in.Timestamps
	}
	return req, nil
}

func boundedInt(v, fallback, max int) int {
	if v <= 0 {
		return fallback
	}
	if v > max {
		return max
	}
	return v
}

func boundedInt64(v, fallback, max int64) int64 {
	if v <= 0 {
		return fallback
	}
	if v > max {
		return max
	}
	return v
}

func (r *Registry) executeQueryK8sLogs(ctx context.Context, args json.RawMessage) (ExecuteResult, error) {
	tool := NewQueryK8sLogsTool(r.caller, r.k8sSnapshot, r.log)
	out, err := tool.InvokableRun(ctx, string(args))
	if err != nil {
		return ExecuteResult{}, err
	}
	return ExecuteResult{ResultJSON: json.RawMessage(out)}, nil
}
