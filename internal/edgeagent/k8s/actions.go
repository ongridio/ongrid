package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	k8sActionRolloutRestart = "rollout_restart"
	k8sActionScale          = "scale"
	k8sActionDeletePod      = "delete_pod"
	k8sActionEvictPod       = "evict_pod"
	k8sActionCordon         = "cordon"
	k8sActionUncordon       = "uncordon"
	k8sActionDrain          = "drain"

	kubernetesMergePatchContentType = "application/merge-patch+json"
	kubernetesJSONContentType       = "application/json"

	defaultDrainTimeoutSeconds = 120
	maxDrainTimeoutSeconds     = 600
	defaultDrainRetrySeconds   = 2
	maxDrainRetrySeconds       = 30
	maxGracePeriodSeconds      = 3600
)

func (c *apiClient) executeAction(ctx context.Context, req tunnel.KubernetesActionRequest) (*tunnel.KubernetesActionResponse, error) {
	action, err := normalizeK8sAction(req.Action)
	if err != nil {
		return nil, err
	}
	req.Action = action

	spec, namespace, name, err := actionTarget(req)
	if err != nil {
		return nil, err
	}
	apiPath := spec.path(namespace, name)
	start := time.Now()

	raw, err := c.getRaw(ctx, apiPath)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: preflight get %s/%s: %w", spec.kind, name, err)
	}
	uid, rv, err := k8sObjectMetadata(raw)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: preflight metadata %s/%s: %w", spec.kind, name, err)
	}
	expectedRV := strings.TrimSpace(req.ExpectedResourceVersion)
	if expectedRV != "" && rv != expectedRV {
		return nil, fmt.Errorf("execute_k8s_action: resourceVersion conflict for %s/%s: expected %s, got %s", spec.kind, name, expectedRV, rv)
	}
	gracePeriod, err := normalizeGracePeriodSeconds(req.GracePeriodSeconds)
	if err != nil {
		return nil, err
	}

	resp := &tunnel.KubernetesActionResponse{
		ClusterID:  req.ClusterID,
		Action:     action,
		Kind:       spec.kind,
		APIVersion: spec.apiVersion,
		Namespace:  namespace,
		Name:       name,
		DryRun:     req.DryRun,
		StartedAt:  start.Unix(),
		Preflight: tunnel.KubernetesActionPreflight{
			Kind:            spec.kind,
			APIVersion:      spec.apiVersion,
			Namespace:       namespace,
			Name:            name,
			UID:             uid,
			ResourceVersion: rv,
			Exists:          true,
		},
	}
	var resultRaw []byte
	messageSuffix := ""
	switch action {
	case k8sActionRolloutRestart:
		resultRaw, err = c.rolloutRestart(ctx, apiPath, expectedRV, req.DryRun)
	case k8sActionScale:
		resultRaw, err = c.scaleWorkload(ctx, apiPath, req.Replicas, expectedRV, req.DryRun)
	case k8sActionDeletePod:
		resultRaw, err = c.deletePod(ctx, apiPath, uid, expectedRV, gracePeriod, req.DryRun)
	case k8sActionEvictPod:
		resultRaw, err = c.evictPod(ctx, namespace, name, uid, expectedRV, gracePeriod, req.DryRun)
	case k8sActionCordon:
		resultRaw, err = c.patchNodeUnschedulable(ctx, apiPath, true, expectedRV, req.DryRun)
	case k8sActionUncordon:
		resultRaw, err = c.patchNodeUnschedulable(ctx, apiPath, false, expectedRV, req.DryRun)
	case k8sActionDrain:
		var summary drainSummary
		opts, optErr := normalizeDrainOptions(req, gracePeriod)
		if optErr != nil {
			return nil, optErr
		}
		opts.nodeResourceVersion = expectedRV
		resultRaw, summary, err = c.drainNode(ctx, apiPath, name, opts)
		resp.EvictedPodCount = summary.evicted
		resp.DeletedPodCount = summary.deleted
		resp.SkippedPodCount = summary.skipped
		resp.SkippedPods = summary.skippedPods
		if req.DryRun {
			messageSuffix = fmt.Sprintf("; would evict %d pod(s), would delete %d pod(s), skipped %d pod(s)", summary.evicted, summary.deleted, summary.skipped)
		} else {
			messageSuffix = fmt.Sprintf("; evicted %d pod(s), deleted %d pod(s), skipped %d pod(s)", summary.evicted, summary.deleted, summary.skipped)
		}
	default:
		err = fmt.Errorf("execute_k8s_action: unsupported action %q", req.Action)
	}
	if err != nil {
		return nil, err
	}
	if len(resultRaw) > 0 {
		if _, _, resultRV, metaErr := sanitizeK8sObject(resultRaw); metaErr == nil {
			resp.ResultResourceVersion = resultRV
		}
	}
	resp.Applied = !req.DryRun
	resp.EndedAt = time.Now().Unix()
	if req.DryRun {
		resp.Message = actionMessage(action, spec.kind, namespace, name) + " dry-run validated by Kubernetes API"
	} else {
		resp.Message = actionMessage(action, spec.kind, namespace, name)
	}
	if messageSuffix != "" {
		resp.Message += messageSuffix
	}
	return resp, nil
}

func normalizeK8sAction(action string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(action, "-", "_"))) {
	case k8sActionRolloutRestart, "restart", "rolloutrestart":
		return k8sActionRolloutRestart, nil
	case k8sActionScale:
		return k8sActionScale, nil
	case k8sActionDeletePod, "delete":
		return k8sActionDeletePod, nil
	case k8sActionEvictPod, "evict":
		return k8sActionEvictPod, nil
	case k8sActionCordon:
		return k8sActionCordon, nil
	case k8sActionUncordon:
		return k8sActionUncordon, nil
	case k8sActionDrain:
		return k8sActionDrain, nil
	default:
		return "", fmt.Errorf("execute_k8s_action: unsupported action %q", action)
	}
}

func normalizeGracePeriodSeconds(in *int) (*int, error) {
	if in == nil {
		return nil, nil
	}
	if *in < 0 || *in > maxGracePeriodSeconds {
		return nil, fmt.Errorf("execute_k8s_action: grace_period_seconds must be between 0 and %d, got %d", maxGracePeriodSeconds, *in)
	}
	v := *in
	return &v, nil
}

func normalizeDrainOptions(req tunnel.KubernetesActionRequest, gracePeriodSeconds *int) (drainOptions, error) {
	timeoutSeconds := req.DrainTimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultDrainTimeoutSeconds
	}
	if timeoutSeconds < 1 || timeoutSeconds > maxDrainTimeoutSeconds {
		return drainOptions{}, fmt.Errorf("execute_k8s_action: drain_timeout_seconds must be between 1 and %d, got %d", maxDrainTimeoutSeconds, req.DrainTimeoutSeconds)
	}
	retrySeconds := req.DrainRetrySeconds
	if retrySeconds == 0 {
		retrySeconds = defaultDrainRetrySeconds
	}
	if retrySeconds < 1 || retrySeconds > maxDrainRetrySeconds {
		return drainOptions{}, fmt.Errorf("execute_k8s_action: drain_retry_seconds must be between 1 and %d, got %d", maxDrainRetrySeconds, req.DrainRetrySeconds)
	}
	ignoreDaemonSets := true
	if req.IgnoreDaemonSets != nil {
		ignoreDaemonSets = *req.IgnoreDaemonSets
	}
	return drainOptions{
		gracePeriodSeconds: gracePeriodSeconds,
		timeoutSeconds:     timeoutSeconds,
		retrySeconds:       retrySeconds,
		ignoreDaemonSets:   ignoreDaemonSets,
		deleteEmptyDirData: req.DeleteEmptyDirData,
		force:              req.Force,
		disableEviction:    req.DisableEviction,
		dryRun:             req.DryRun,
	}, nil
}

func actionTarget(req tunnel.KubernetesActionRequest) (k8sDescribeSpec, string, string, error) {
	kind := strings.TrimSpace(req.Kind)
	switch strings.TrimSpace(req.Action) {
	case k8sActionDeletePod, k8sActionEvictPod:
		if kind == "" {
			kind = "Pod"
		}
	case k8sActionCordon, k8sActionUncordon, k8sActionDrain:
		if kind == "" {
			kind = "Node"
		}
	}
	if kind == "" {
		return k8sDescribeSpec{}, "", "", errors.New("execute_k8s_action: kind is required")
	}
	spec, err := describeSpecFor(kind, req.APIVersion)
	if err != nil {
		return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: %w", err)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return k8sDescribeSpec{}, "", "", errors.New("execute_k8s_action: name is required")
	}
	namespace := strings.TrimSpace(req.Namespace)
	if spec.namespaced && namespace == "" {
		return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: namespace is required for %s", spec.kind)
	}
	action := strings.TrimSpace(req.Action)
	switch action {
	case k8sActionRolloutRestart:
		if spec.kind != "Deployment" && spec.kind != "StatefulSet" && spec.kind != "DaemonSet" {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: rollout_restart supports Deployment, StatefulSet, or DaemonSet, got %s", spec.kind)
		}
	case k8sActionScale:
		if spec.kind != "Deployment" && spec.kind != "StatefulSet" {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: scale supports Deployment or StatefulSet, got %s", spec.kind)
		}
		if req.Replicas == nil {
			return k8sDescribeSpec{}, "", "", errors.New("execute_k8s_action: replicas is required for scale")
		}
		if *req.Replicas < 0 || *req.Replicas > 10000 {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: replicas must be between 0 and 10000, got %d", *req.Replicas)
		}
	case k8sActionDeletePod:
		if spec.kind != "Pod" {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: delete_pod supports Pod only, got %s", spec.kind)
		}
	case k8sActionEvictPod:
		if spec.kind != "Pod" {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: evict_pod supports Pod only, got %s", spec.kind)
		}
	case k8sActionCordon, k8sActionUncordon, k8sActionDrain:
		if spec.kind != "Node" {
			return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: %s supports Node only, got %s", action, spec.kind)
		}
	default:
		return k8sDescribeSpec{}, "", "", fmt.Errorf("execute_k8s_action: unsupported action %q", action)
	}
	return spec, namespace, name, nil
}

func (c *apiClient) rolloutRestart(ctx context.Context, apiPath, resourceVersion string, dryRun bool) ([]byte, error) {
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{
						"kubectl.kubernetes.io/restartedAt": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	addResourceVersionPrecondition(patch, resourceVersion)
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: marshal rollout_restart patch: %w", err)
	}
	out, err := c.doRaw(ctx, http.MethodPatch, withDryRun(apiPath, dryRun), kubernetesMergePatchContentType, body)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: rollout_restart patch %s: %w", apiPath, err)
	}
	return out, nil
}

func (c *apiClient) scaleWorkload(ctx context.Context, apiPath string, replicas *int, resourceVersion string, dryRun bool) ([]byte, error) {
	patch := map[string]any{"spec": map[string]int{"replicas": *replicas}}
	addResourceVersionPrecondition(patch, resourceVersion)
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: marshal scale patch: %w", err)
	}
	out, err := c.doRaw(ctx, http.MethodPatch, withDryRun(apiPath, dryRun), kubernetesMergePatchContentType, body)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: scale patch %s: %w", apiPath, err)
	}
	return out, nil
}

func (c *apiClient) deletePod(ctx context.Context, apiPath, uid, resourceVersion string, gracePeriodSeconds *int, dryRun bool) ([]byte, error) {
	body, err := deleteOptionsBody(uid, resourceVersion, gracePeriodSeconds)
	if err != nil {
		return nil, err
	}
	out, err := c.doRaw(ctx, http.MethodDelete, withDryRun(apiPath, dryRun), kubernetesJSONContentType, body)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: delete pod %s: %w", apiPath, err)
	}
	return out, nil
}

func deleteOptionsBody(uid, resourceVersion string, gracePeriodSeconds *int) ([]byte, error) {
	options := map[string]any{
		"apiVersion": "v1",
		"kind":       "DeleteOptions",
	}
	if gracePeriodSeconds != nil {
		options["gracePeriodSeconds"] = *gracePeriodSeconds
	}
	preconditions := map[string]string{}
	if strings.TrimSpace(uid) != "" {
		preconditions["uid"] = uid
	}
	if strings.TrimSpace(resourceVersion) != "" {
		preconditions["resourceVersion"] = resourceVersion
	}
	if len(preconditions) > 0 {
		options["preconditions"] = preconditions
	}
	body, err := json.Marshal(options)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: marshal delete options: %w", err)
	}
	return body, nil
}

func addResourceVersionPrecondition(patch map[string]any, resourceVersion string) {
	resourceVersion = strings.TrimSpace(resourceVersion)
	if resourceVersion == "" {
		return
	}
	patch["metadata"] = map[string]string{"resourceVersion": resourceVersion}
}

func (c *apiClient) evictPod(ctx context.Context, namespace, name, uid, resourceVersion string, gracePeriodSeconds *int, dryRun bool) ([]byte, error) {
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(name) + "/eviction"
	eviction := map[string]any{
		"apiVersion": "policy/v1",
		"kind":       "Eviction",
		"metadata": map[string]string{
			"name":      name,
			"namespace": namespace,
		},
	}
	deleteOptions := map[string]any{}
	preconditions := map[string]string{}
	if strings.TrimSpace(uid) != "" {
		preconditions["uid"] = uid
	}
	if strings.TrimSpace(resourceVersion) != "" {
		preconditions["resourceVersion"] = resourceVersion
	}
	if len(preconditions) > 0 {
		deleteOptions["preconditions"] = preconditions
	}
	if gracePeriodSeconds != nil {
		deleteOptions["gracePeriodSeconds"] = *gracePeriodSeconds
	}
	if len(deleteOptions) > 0 {
		eviction["deleteOptions"] = deleteOptions
	}
	body, err := json.Marshal(eviction)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: marshal eviction: %w", err)
	}
	out, err := c.doRaw(ctx, http.MethodPost, withDryRun(path, dryRun), kubernetesJSONContentType, body)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: evict pod %s/%s: %w", namespace, name, err)
	}
	return out, nil
}

func (c *apiClient) patchNodeUnschedulable(ctx context.Context, apiPath string, unschedulable bool, resourceVersion string, dryRun bool) ([]byte, error) {
	patch := map[string]any{"spec": map[string]bool{"unschedulable": unschedulable}}
	addResourceVersionPrecondition(patch, resourceVersion)
	body, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: marshal node patch: %w", err)
	}
	out, err := c.doRaw(ctx, http.MethodPatch, withDryRun(apiPath, dryRun), kubernetesMergePatchContentType, body)
	if err != nil {
		return nil, fmt.Errorf("execute_k8s_action: node patch %s: %w", apiPath, err)
	}
	return out, nil
}

type drainOptions struct {
	gracePeriodSeconds  *int
	timeoutSeconds      int
	retrySeconds        int
	ignoreDaemonSets    bool
	deleteEmptyDirData  bool
	force               bool
	disableEviction     bool
	dryRun              bool
	nodeResourceVersion string
}

type drainSummary struct {
	evicted     int
	deleted     int
	skipped     int
	skippedPods []tunnel.KubernetesActionPodResult
}

func (c *apiClient) drainNode(ctx context.Context, apiPath, nodeName string, opts drainOptions) ([]byte, drainSummary, error) {
	pods, err := c.listPodsOnNode(ctx, nodeName)
	if err != nil {
		return nil, drainSummary{}, fmt.Errorf("execute_k8s_action: list pods on node %s: %w", nodeName, err)
	}
	for _, pod := range pods {
		decision := drainDecision(pod, opts)
		if decision.abort {
			return nil, drainSummary{}, fmt.Errorf("execute_k8s_action: drain refused pod %s/%s: %s", pod.Metadata.Namespace, pod.Metadata.Name, decision.reason)
		}
	}
	nodeRaw, err := c.patchNodeUnschedulable(ctx, apiPath, true, opts.nodeResourceVersion, opts.dryRun)
	if err != nil {
		return nil, drainSummary{}, err
	}
	drainCtx := ctx
	cancel := func() {}
	if opts.timeoutSeconds > 0 {
		drainCtx, cancel = context.WithTimeout(ctx, time.Duration(opts.timeoutSeconds)*time.Second)
	}
	defer cancel()

	summary := drainSummary{}
	for _, pod := range pods {
		decision := drainDecision(pod, opts)
		if !decision.drain {
			summary.skipped++
			summary.skippedPods = append(summary.skippedPods, tunnel.KubernetesActionPodResult{
				Namespace: pod.Metadata.Namespace,
				Name:      pod.Metadata.Name,
				Action:    "skipped",
				Reason:    decision.reason,
			})
			continue
		}
		if pod.Metadata.Namespace == "" || pod.Metadata.Name == "" {
			summary.skipped++
			summary.skippedPods = append(summary.skippedPods, tunnel.KubernetesActionPodResult{
				Name:   pod.Metadata.Name,
				Action: "skipped",
				Reason: "missing namespace or name",
			})
			continue
		}
		if opts.disableEviction {
			if _, err := c.deleteNamespacedPod(drainCtx, pod, opts.gracePeriodSeconds, opts.dryRun); err != nil {
				return nil, summary, err
			}
			summary.deleted++
			continue
		}
		if _, err := c.evictPodWithRetry(drainCtx, pod, opts); err != nil {
			return nil, summary, err
		}
		summary.evicted++
	}
	return nodeRaw, summary, nil
}

func (c *apiClient) listPodsOnNode(ctx context.Context, nodeName string) ([]podItem, error) {
	apiPath := "/api/v1/pods?fieldSelector=" + url.QueryEscape("spec.nodeName="+nodeName)
	var list podList
	if err := c.get(ctx, apiPath, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

type drainPodDecision struct {
	drain  bool
	abort  bool
	reason string
}

func drainDecision(pod podItem, opts drainOptions) drainPodDecision {
	switch pod.Status.Phase {
	case "Succeeded", "Failed":
		return drainPodDecision{reason: "terminal phase " + pod.Status.Phase}
	}
	if pod.Metadata.Annotations["kubernetes.io/config.mirror"] != "" {
		return drainPodDecision{reason: "mirror/static pod"}
	}
	ownerKind, _ := controllerOwner(pod.Metadata.OwnerReferences)
	if ownerKind == "DaemonSet" {
		if opts.ignoreDaemonSets {
			return drainPodDecision{reason: "daemonset pod"}
		}
		return drainPodDecision{abort: true, reason: "daemonset pod requires ignore_daemonsets=true"}
	}
	if ownerKind == "" && !opts.force {
		return drainPodDecision{reason: "unmanaged pod requires force=true"}
	}
	if podHasEmptyDir(pod) && !opts.deleteEmptyDirData {
		return drainPodDecision{reason: "emptyDir data requires delete_emptydir_data=true"}
	}
	return drainPodDecision{drain: true}
}

func (c *apiClient) evictPodWithRetry(ctx context.Context, pod podItem, opts drainOptions) ([]byte, error) {
	retry := time.Duration(opts.retrySeconds) * time.Second
	for {
		out, err := c.evictPod(ctx, pod.Metadata.Namespace, pod.Metadata.Name, pod.Metadata.UID, pod.Metadata.ResourceVersion, opts.gracePeriodSeconds, opts.dryRun)
		if err == nil {
			return out, nil
		}
		if !isKubernetesStatusError(err, http.StatusTooManyRequests) {
			return nil, err
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, fmt.Errorf("execute_k8s_action: eviction for pod %s/%s blocked by PDB until timeout: %w", pod.Metadata.Namespace, pod.Metadata.Name, err)
		case <-timer.C:
		}
	}
}

func (c *apiClient) deleteNamespacedPod(ctx context.Context, pod podItem, gracePeriodSeconds *int, dryRun bool) ([]byte, error) {
	apiPath := "/api/v1/namespaces/" + url.PathEscape(pod.Metadata.Namespace) + "/pods/" + url.PathEscape(pod.Metadata.Name)
	return c.deletePod(ctx, apiPath, pod.Metadata.UID, pod.Metadata.ResourceVersion, gracePeriodSeconds, dryRun)
}

func podHasEmptyDir(pod podItem) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir != nil {
			return true
		}
	}
	return false
}

func actionMessage(action, kind, namespace, name string) string {
	target := kind + "/" + name
	if namespace != "" {
		target = namespace + "/" + target
	}
	switch action {
	case k8sActionRolloutRestart:
		return "rollout restart requested for " + target
	case k8sActionScale:
		return "scale requested for " + target
	case k8sActionDeletePod:
		return "pod delete requested for " + target
	case k8sActionEvictPod:
		return "pod eviction requested for " + target
	case k8sActionCordon:
		return "cordon requested for " + target
	case k8sActionUncordon:
		return "uncordon requested for " + target
	case k8sActionDrain:
		return "drain requested for " + target
	default:
		return "Kubernetes action applied for " + target
	}
}

func k8sObjectMetadata(raw []byte) (uid, resourceVersion string, err error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", "", err
	}
	metadata, _ := obj["metadata"].(map[string]any)
	return stringField(metadata, "uid"), stringField(metadata, "resourceVersion"), nil
}

type kubernetesAPIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *kubernetesAPIError) Error() string {
	return fmt.Sprintf("kubernetes api %s %s: status=%d body=%s", e.Method, e.Path, e.StatusCode, e.Body)
}

func isKubernetesStatusError(err error, statusCode int) bool {
	var apiErr *kubernetesAPIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == statusCode
}

func (c *apiClient) doRaw(ctx context.Context, method, apiPath, contentType string, body []byte) ([]byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+apiPath, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, errForbidden
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &kubernetesAPIError{
			Method:     method,
			Path:       apiPath,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(respBody)),
		}
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func withDryRun(apiPath string, dryRun bool) string {
	if !dryRun {
		return apiPath
	}
	sep := "?"
	if strings.Contains(apiPath, "?") {
		sep = "&"
	}
	return apiPath + sep + "dryRun=All"
}
