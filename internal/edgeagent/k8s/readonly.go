package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultPodLogTailLines    = 100
	defaultPodLogLimitBytes   = 16 * 1024
	defaultPodLogSinceSeconds = int64(3600)
	maxPodLogTailLines        = 500
	maxPodLogLimitBytes       = 64 * 1024
	maxPodLogSinceSeconds     = int64(24 * 3600)
)

// RegisterReadOnlyHandlers installs live Kubernetes controller handlers. The
// name is kept for compatibility with the existing call site; write actions are
// limited to execute_k8s_action and are gated manager-side before dispatch.
func (p *InventoryPusher) RegisterReadOnlyHandlers() {
	if p == nil || p.client == nil || p.api == nil {
		return
	}
	p.client.RegisterHandler(tunnel.MethodDescribeK8sResource,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.KubernetesDescribeResourceRequest
			if len(body) > 0 {
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("describe_k8s_resource: decode: %w", err)
				}
			}
			if req.ClusterID != 0 && req.ClusterID != p.info.ClusterID {
				return nil, fmt.Errorf("describe_k8s_resource: cluster_id %d does not match controller cluster_id %d", req.ClusterID, p.info.ClusterID)
			}
			req.ClusterID = p.info.ClusterID
			resp, err := p.api.describeResource(ctx, req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(resp)
		})
	p.client.RegisterHandler(tunnel.MethodQueryK8sLogs,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.KubernetesPodLogsRequest
			if len(body) > 0 {
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("query_k8s_logs: decode: %w", err)
				}
			}
			if req.ClusterID != 0 && req.ClusterID != p.info.ClusterID {
				return nil, fmt.Errorf("query_k8s_logs: cluster_id %d does not match controller cluster_id %d", req.ClusterID, p.info.ClusterID)
			}
			req.ClusterID = p.info.ClusterID
			resp, err := p.api.queryPodLogs(ctx, req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(resp)
		})
	p.client.RegisterHandler(tunnel.MethodExecuteK8sAction,
		func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
			var req tunnel.KubernetesActionRequest
			if len(body) > 0 {
				if err := json.Unmarshal(body, &req); err != nil {
					return nil, fmt.Errorf("execute_k8s_action: decode: %w", err)
				}
			}
			if req.ClusterID != 0 && req.ClusterID != p.info.ClusterID {
				return nil, fmt.Errorf("execute_k8s_action: cluster_id %d does not match controller cluster_id %d", req.ClusterID, p.info.ClusterID)
			}
			req.ClusterID = p.info.ClusterID
			resp, err := p.api.executeAction(ctx, req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(resp)
		})
}

func (c *apiClient) describeResource(ctx context.Context, req tunnel.KubernetesDescribeResourceRequest) (*tunnel.KubernetesDescribeResourceResponse, error) {
	spec, err := describeSpecFor(req.Kind, req.APIVersion)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, errors.New("describe_k8s_resource: name is required")
	}
	namespace := strings.TrimSpace(req.Namespace)
	if spec.namespaced && namespace == "" {
		return nil, fmt.Errorf("describe_k8s_resource: namespace is required for %s", spec.kind)
	}
	apiPath := spec.path(namespace, name)
	raw, err := c.getRaw(ctx, apiPath)
	if err != nil {
		return nil, fmt.Errorf("describe_k8s_resource: get %s/%s: %w", spec.kind, name, err)
	}
	object, uid, rv, err := sanitizeK8sObject(raw)
	if err != nil {
		return nil, fmt.Errorf("describe_k8s_resource: sanitize %s/%s: %w", spec.kind, name, err)
	}
	resp := &tunnel.KubernetesDescribeResourceResponse{
		ClusterID:       req.ClusterID,
		Kind:            spec.kind,
		APIVersion:      spec.apiVersion,
		Namespace:       namespace,
		Name:            name,
		UID:             uid,
		ResourceVersion: rv,
		FetchedAt:       time.Now().Unix(),
		Object:          object,
	}
	if req.IncludeEvents {
		limit := req.EventsLimit
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		events, err := c.relatedEvents(ctx, spec.kind, namespace, name, limit)
		if err != nil && !isOptionalAPIError(err) {
			return nil, fmt.Errorf("describe_k8s_resource: events %s/%s: %w", spec.kind, name, err)
		}
		resp.Events = events
	}
	return resp, nil
}

func (c *apiClient) queryPodLogs(ctx context.Context, req tunnel.KubernetesPodLogsRequest) (*tunnel.KubernetesPodLogsResponse, error) {
	namespace := strings.TrimSpace(req.Namespace)
	if namespace == "" {
		return nil, errors.New("query_k8s_logs: namespace is required")
	}
	pod := strings.TrimSpace(req.Pod)
	if pod == "" {
		return nil, errors.New("query_k8s_logs: pod is required")
	}
	req.Namespace = namespace
	req.Pod = pod
	req.Container = strings.TrimSpace(req.Container)
	if req.TailLines <= 0 {
		req.TailLines = defaultPodLogTailLines
	}
	if req.TailLines > maxPodLogTailLines {
		req.TailLines = maxPodLogTailLines
	}
	if req.LimitBytes <= 0 {
		req.LimitBytes = defaultPodLogLimitBytes
	}
	if req.LimitBytes > maxPodLogLimitBytes {
		req.LimitBytes = maxPodLogLimitBytes
	}
	if req.SinceSeconds <= 0 {
		req.SinceSeconds = defaultPodLogSinceSeconds
	}
	if req.SinceSeconds > maxPodLogSinceSeconds {
		req.SinceSeconds = maxPodLogSinceSeconds
	}
	apiPath := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod) + "/log"
	q := url.Values{}
	q.Set("tailLines", strconv.Itoa(req.TailLines))
	q.Set("limitBytes", strconv.Itoa(req.LimitBytes))
	q.Set("sinceSeconds", strconv.FormatInt(req.SinceSeconds, 10))
	if req.Container != "" {
		q.Set("container", req.Container)
	}
	if req.Previous {
		q.Set("previous", "true")
	}
	if req.Timestamps {
		q.Set("timestamps", "true")
	}
	body, err := c.getRaw(ctx, apiPath+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("query_k8s_logs: get pod logs %s/%s: %w", namespace, pod, err)
	}
	if len(body) > req.LimitBytes {
		body = body[:req.LimitBytes]
	}
	logs := string(body)
	return &tunnel.KubernetesPodLogsResponse{
		ClusterID:    req.ClusterID,
		Namespace:    namespace,
		Pod:          pod,
		Container:    req.Container,
		Previous:     req.Previous,
		SinceSeconds: req.SinceSeconds,
		TailLines:    req.TailLines,
		LimitBytes:   req.LimitBytes,
		Timestamps:   req.Timestamps,
		FetchedAt:    time.Now().Unix(),
		Bytes:        len(body),
		LineCount:    countLogLines(logs),
		Truncated:    len(body) >= req.LimitBytes,
		Logs:         logs,
	}, nil
}

type k8sDescribeSpec struct {
	kind       string
	apiVersion string
	resource   string
	group      string
	namespaced bool
}

func (s k8sDescribeSpec) path(namespace, name string) string {
	escapedName := url.PathEscape(name)
	if s.group == "" {
		if s.namespaced {
			return "/api/v1/namespaces/" + url.PathEscape(namespace) + "/" + s.resource + "/" + escapedName
		}
		return "/api/v1/" + s.resource + "/" + escapedName
	}
	if s.namespaced {
		return "/apis/" + s.group + "/v1/namespaces/" + url.PathEscape(namespace) + "/" + s.resource + "/" + escapedName
	}
	return "/apis/" + s.group + "/v1/" + s.resource + "/" + escapedName
}

func describeSpecFor(kind, apiVersion string) (k8sDescribeSpec, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	v := strings.TrimSpace(apiVersion)
	switch k {
	case "pod", "pods":
		return k8sDescribeSpec{kind: "Pod", apiVersion: firstNonEmpty(v, "v1"), resource: "pods", namespaced: true}, nil
	case "node", "nodes":
		return k8sDescribeSpec{kind: "Node", apiVersion: firstNonEmpty(v, "v1"), resource: "nodes"}, nil
	case "namespace", "namespaces":
		return k8sDescribeSpec{kind: "Namespace", apiVersion: firstNonEmpty(v, "v1"), resource: "namespaces"}, nil
	case "service", "services":
		return k8sDescribeSpec{kind: "Service", apiVersion: firstNonEmpty(v, "v1"), resource: "services", namespaced: true}, nil
	case "deployment", "deployments":
		return k8sDescribeSpec{kind: "Deployment", apiVersion: firstNonEmpty(v, "apps/v1"), group: "apps", resource: "deployments", namespaced: true}, nil
	case "statefulset", "statefulsets":
		return k8sDescribeSpec{kind: "StatefulSet", apiVersion: firstNonEmpty(v, "apps/v1"), group: "apps", resource: "statefulsets", namespaced: true}, nil
	case "daemonset", "daemonsets":
		return k8sDescribeSpec{kind: "DaemonSet", apiVersion: firstNonEmpty(v, "apps/v1"), group: "apps", resource: "daemonsets", namespaced: true}, nil
	case "replicaset", "replicasets":
		return k8sDescribeSpec{kind: "ReplicaSet", apiVersion: firstNonEmpty(v, "apps/v1"), group: "apps", resource: "replicasets", namespaced: true}, nil
	case "job", "jobs":
		return k8sDescribeSpec{kind: "Job", apiVersion: firstNonEmpty(v, "batch/v1"), group: "batch", resource: "jobs", namespaced: true}, nil
	case "cronjob", "cronjobs":
		return k8sDescribeSpec{kind: "CronJob", apiVersion: firstNonEmpty(v, "batch/v1"), group: "batch", resource: "cronjobs", namespaced: true}, nil
	case "event", "events":
		return k8sDescribeSpec{kind: "Event", apiVersion: firstNonEmpty(v, "v1"), resource: "events", namespaced: true}, nil
	case "secret", "secrets", "configmap", "configmaps":
		return k8sDescribeSpec{}, fmt.Errorf("describe_k8s_resource: kind %q is not allowed", kind)
	default:
		return k8sDescribeSpec{}, fmt.Errorf("describe_k8s_resource: unsupported kind %q", kind)
	}
}

func sanitizeK8sObject(raw []byte) (json.RawMessage, string, string, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, "", "", err
	}
	metadata, _ := obj["metadata"].(map[string]any)
	if metadata != nil {
		delete(metadata, "managedFields")
		delete(metadata, "annotations")
	}
	uid := stringField(metadata, "uid")
	rv := stringField(metadata, "resourceVersion")
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, "", "", err
	}
	return out, uid, rv, nil
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func (c *apiClient) relatedEvents(ctx context.Context, kind, namespace, name string, limit int) ([]tunnel.KubernetesEventSnapshot, error) {
	events, _, err := c.listEvents(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := make([]tunnel.KubernetesEventSnapshot, 0, minInt(limit, len(events)))
	for _, ev := range events {
		if ev.InvolvedKind != kind || ev.InvolvedName != name {
			continue
		}
		if namespace != "" && ev.InvolvedNamespace != "" && ev.InvolvedNamespace != namespace {
			continue
		}
		out = append(out, ev)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func countLogLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
