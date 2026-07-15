package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/k8sredact"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

const (
	defaultInventoryInterval = 30 * time.Second
	defaultWatchDebounce     = 2 * time.Second
	defaultWatchRetry        = 2 * time.Second
	maxWatchRetry            = 30 * time.Second
	watchTimeoutSeconds      = 10

	defaultServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

	inventoryScopeCluster   = "cluster"
	inventoryScopeNamespace = "namespace"
)

var (
	errForbidden       = errors.New("kubernetes api forbidden")
	errNotFound        = errors.New("kubernetes api not found")
	errResourceExpired = errors.New("kubernetes api resource version expired")
)

type InventoryPusher struct {
	client   tunnel.Client
	info     tunnel.KubernetesInfo
	edgeID   func() uint64
	interval time.Duration
	log      *slog.Logger
	api      *apiClient
	watch    bool
}

func NewInventoryPusher(client tunnel.Client, info tunnel.KubernetesInfo, edgeID func() uint64, interval time.Duration, watchEnabled bool, log *slog.Logger) (*InventoryPusher, error) {
	if client == nil {
		return nil, errors.New("k8s inventory: tunnel client is required")
	}
	if info.ClusterID == 0 {
		return nil, errors.New("k8s inventory: cluster_id is required")
	}
	if interval <= 0 {
		interval = defaultInventoryInterval
	}
	if edgeID == nil {
		edgeID = func() uint64 { return 0 }
	}
	if log == nil {
		log = slog.Default()
	}
	api, err := newInClusterAPIClient()
	if err != nil {
		return nil, err
	}
	return &InventoryPusher{
		client:   client,
		info:     info,
		edgeID:   edgeID,
		interval: interval,
		log:      log,
		api:      api,
		watch:    watchEnabled,
	}, nil
}

func (p *InventoryPusher) Run(ctx context.Context) error {
	if !isControllerRole(p.info.Role) {
		return nil
	}
	t := time.NewTicker(p.interval)
	defer t.Stop()

	var snap *inventorySnapshot
	for {
		if id := p.edgeID(); id != 0 {
			nextSnap, err := p.pushOnceWithSnapshot(ctx, id, inventoryWatchTrigger{})
			if err != nil && !errors.Is(err, context.Canceled) {
				p.log.Warn("k8s inventory push failed", slog.Any("err", err))
			} else {
				snap = nextSnap
			}
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}

	cache := newInventoryCache(snap)
	watchTriggers := newInventoryWatchAccumulator()
	var watchOnce sync.Once
	startWatch := func(snapshot *inventorySnapshot) {
		if !p.watch || snapshot == nil {
			return
		}
		watchOnce.Do(func() {
			go p.runWatchTriggers(ctx, snapshot, cache, watchTriggers)
		})
	}
	startWatch(snap)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-watchTriggers.notifications():
			id := p.edgeID()
			if id == 0 {
				continue
			}
			trigger, ok := waitForWatchDebounce(ctx, watchTriggers, defaultWatchDebounce)
			if !ok {
				return nil
			}
			var err error
			if trigger.fullResync {
				var nextSnap *inventorySnapshot
				nextSnap, err = p.pushOnceWithSnapshot(ctx, id, trigger)
				if err == nil {
					snap = nextSnap
					cache.reset(nextSnap)
					startWatch(nextSnap)
				}
			} else {
				err = p.pushDelta(ctx, id, snap, trigger)
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				p.log.Warn("k8s inventory watch-triggered push failed",
					slog.String("reason", trigger.reason),
					slog.Any("err", err),
				)
				continue
			}
		case <-t.C:
			id := p.edgeID()
			if id == 0 {
				continue
			}
			nextSnap, err := p.pushOnceWithSnapshot(ctx, id, inventoryWatchTrigger{})
			if err != nil && !errors.Is(err, context.Canceled) {
				p.log.Warn("k8s inventory push failed", slog.Any("err", err))
				continue
			}
			snap = nextSnap
			cache.reset(nextSnap)
			startWatch(nextSnap)
		}
	}
}

func (p *InventoryPusher) pushOnce(ctx context.Context, edgeID uint64) error {
	_, err := p.pushOnceWithSnapshot(ctx, edgeID, inventoryWatchTrigger{})
	return err
}

func (p *InventoryPusher) pushOnceWithSnapshot(ctx context.Context, edgeID uint64, trigger inventoryWatchTrigger) (*inventorySnapshot, error) {
	snap, err := p.collect(ctx)
	if err != nil {
		return nil, err
	}
	base := tunnel.KubernetesInventoryRequest{
		EdgeID:               edgeID,
		ClusterID:            p.info.ClusterID,
		Mode:                 p.info.Mode,
		Role:                 p.info.Role,
		Scope:                snap.scope,
		Namespace:            snap.namespace,
		Ts:                   snap.collectedAt,
		ResourceVersion:      snap.resourceVersion,
		ResourceVersions:     snap.resourceVersions,
		CollectDurationMS:    snap.collectDurationMS,
		WatchEventObservedAt: trigger.observedUnix(),
		WatchTriggerReason:   trigger.reason,
		SyncType:             inventorySyncFull,
	}
	chunks, err := buildInventorySnapshotChunks(base, snap)
	if err != nil {
		return nil, err
	}
	var accepted tunnel.KubernetesInventoryResponse
	for _, req := range chunks {
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		var resp tunnel.KubernetesInventoryResponse
		err := p.client.Call(rctx, tunnel.MethodPushK8sInventory, req, &resp)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("push kubernetes inventory chunk %d/%d: %w", req.ChunkIndex+1, req.ChunkCount, err)
		}
		accepted.AcceptedNodes += resp.AcceptedNodes
		accepted.AcceptedWorkloads += resp.AcceptedWorkloads
		accepted.AcceptedPods += resp.AcceptedPods
		accepted.AcceptedEvents += resp.AcceptedEvents
	}
	p.log.Info("k8s inventory pushed",
		slog.Int("nodes", accepted.AcceptedNodes),
		slog.Int("workloads", accepted.AcceptedWorkloads),
		slog.Int("pods", accepted.AcceptedPods),
		slog.Int("events", accepted.AcceptedEvents),
		slog.Int("chunks", len(chunks)),
		slog.String("resource_version", snap.resourceVersion),
		slog.Int64("collect_duration_ms", snap.collectDurationMS),
	)
	return snap, nil
}

func (p *InventoryPusher) pushDelta(ctx context.Context, edgeID uint64, snap *inventorySnapshot, trigger inventoryWatchTrigger) error {
	if trigger.isEmpty() {
		return nil
	}
	scope := inventoryScopeCluster
	namespace := ""
	if snap != nil {
		if strings.TrimSpace(snap.scope) != "" {
			scope = snap.scope
		}
		namespace = snap.namespace
	}
	req := tunnel.KubernetesInventoryRequest{
		EdgeID:               edgeID,
		ClusterID:            p.info.ClusterID,
		Mode:                 p.info.Mode,
		Role:                 p.info.Role,
		Scope:                scope,
		Namespace:            namespace,
		Ts:                   time.Now().UTC().Unix(),
		ResourceVersion:      trigger.resourceVersion,
		ResourceVersions:     trigger.resourceVersions,
		WatchEventObservedAt: trigger.observedUnix(),
		WatchTriggerReason:   trigger.reason,
		SyncType:             inventorySyncDelta,
		Nodes:                trigger.nodes,
		Workloads:            trigger.workloads,
		Pods:                 trigger.pods,
		Events:               trigger.events,
		DeletedNodes:         trigger.deletedNodes,
		DeletedWorkloads:     trigger.deletedWorkloads,
		DeletedPods:          trigger.deletedPods,
		DeletedEvents:        trigger.deletedEvents,
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var resp tunnel.KubernetesInventoryResponse
	if err := p.client.Call(rctx, tunnel.MethodPushK8sInventory, req, &resp); err != nil {
		return err
	}
	p.log.Info("k8s inventory delta pushed",
		slog.Int("nodes", len(req.Nodes)),
		slog.Int("workloads", len(req.Workloads)),
		slog.Int("pods", len(req.Pods)),
		slog.Int("events", len(req.Events)),
		slog.Int("deleted_nodes", len(req.DeletedNodes)),
		slog.Int("deleted_workloads", len(req.DeletedWorkloads)),
		slog.Int("deleted_pods", len(req.DeletedPods)),
		slog.Int("deleted_events", len(req.DeletedEvents)),
		slog.String("resource_version", req.ResourceVersion),
		slog.String("reason", trigger.reason),
	)
	return nil
}

type inventoryWatchTrigger struct {
	reason           string
	observedAt       time.Time
	count            int
	syncType         string
	fullResync       bool
	resourceVersion  string
	resourceVersions map[string]string
	nodes            []tunnel.KubernetesNodeSnapshot
	workloads        []tunnel.KubernetesWorkloadSnapshot
	pods             []tunnel.KubernetesPodSnapshot
	events           []tunnel.KubernetesEventSnapshot
	deletedNodes     []tunnel.KubernetesNodeRef
	deletedWorkloads []tunnel.KubernetesWorkloadRef
	deletedPods      []tunnel.KubernetesPodRef
	deletedEvents    []tunnel.KubernetesEventRef
}

func newInventoryWatchTrigger(reason string, observedAt time.Time) inventoryWatchTrigger {
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	return inventoryWatchTrigger{
		reason:     strings.TrimSpace(reason),
		observedAt: observedAt.UTC(),
		count:      1,
		syncType:   inventorySyncDelta,
	}
}

func newInventoryFullResyncTrigger(reason string, observedAt time.Time) inventoryWatchTrigger {
	trigger := newInventoryWatchTrigger(reason, observedAt)
	trigger.syncType = inventorySyncFull
	trigger.fullResync = true
	return trigger
}

func (t inventoryWatchTrigger) observedUnix() int64 {
	if t.observedAt.IsZero() {
		return 0
	}
	return t.observedAt.UTC().Unix()
}

func (t inventoryWatchTrigger) isEmpty() bool {
	return !t.fullResync &&
		len(t.nodes) == 0 &&
		len(t.workloads) == 0 &&
		len(t.pods) == 0 &&
		len(t.events) == 0 &&
		len(t.deletedNodes) == 0 &&
		len(t.deletedWorkloads) == 0 &&
		len(t.deletedPods) == 0 &&
		len(t.deletedEvents) == 0
}

type inventoryWatchSpec struct {
	name            string
	apiPath         string
	resourceVersion string
	resource        string
	workloadKind    string
}

func (p *InventoryPusher) runWatchTriggers(ctx context.Context, snap *inventorySnapshot, cache *inventoryCache, triggers *inventoryWatchAccumulator) {
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("k8s inventory watch panic recovered", slog.Any("panic", r))
		}
	}()
	specs := p.watchSpecs(snap)
	if len(specs) == 0 {
		return
	}
	p.log.Info("k8s inventory watch enabled", slog.Int("resources", len(specs)))
	var wg sync.WaitGroup
	for _, spec := range specs {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					p.log.Error("k8s inventory watch worker panic recovered",
						slog.String("resource", spec.name),
						slog.Any("panic", r),
					)
				}
			}()
			p.watchResourceLoop(ctx, spec, cache, triggers)
		}()
	}
	wg.Wait()
}

func (p *InventoryPusher) watchResourceLoop(ctx context.Context, spec inventoryWatchSpec, cache *inventoryCache, triggers *inventoryWatchAccumulator) {
	resourceVersion := strings.TrimSpace(spec.resourceVersion)
	retry := defaultWatchRetry
	for ctx.Err() == nil {
		latest, err := p.api.watch(ctx, spec.apiPath, resourceVersion, func(event k8sWatchEvent) error {
			if rv := eventResourceVersion(event); rv != "" {
				resourceVersion = rv
			}
			eventType := strings.ToUpper(strings.TrimSpace(event.Type))
			if eventType == "" || eventType == "BOOKMARK" {
				return nil
			}
			observedAt := time.Now()
			watchTrigger, err := cache.applyWatchEvent(spec, event, observedAt)
			if err != nil {
				return err
			}
			if watchTrigger.isEmpty() {
				return nil
			}
			triggers.add(watchTrigger)
			return nil
		})
		if latest != "" {
			resourceVersion = latest
		}
		if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			retry = defaultWatchRetry
			continue
		}
		if errors.Is(err, errForbidden) || errors.Is(err, errNotFound) {
			p.log.Warn("k8s inventory watch disabled for resource",
				slog.String("resource", spec.name),
				slog.Any("err", err),
			)
			return
		}
		if errors.Is(err, errResourceExpired) {
			resourceVersion = ""
			retry = defaultWatchRetry
			triggers.add(newInventoryFullResyncTrigger(spec.name+":RESYNC", time.Now()))
		} else {
			p.log.Warn("k8s inventory watch failed",
				slog.String("resource", spec.name),
				slog.Duration("retry_after", retry),
				slog.Any("err", err),
			)
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
		if retry < maxWatchRetry {
			retry *= 2
			if retry > maxWatchRetry {
				retry = maxWatchRetry
			}
		}
	}
}

func (p *InventoryPusher) watchSpecs(snap *inventorySnapshot) []inventoryWatchSpec {
	if snap == nil {
		return nil
	}
	namespace := strings.TrimSpace(snap.namespace)
	if strings.TrimSpace(snap.scope) == inventoryScopeNamespace && namespace == "" {
		namespace = strings.TrimSpace(p.info.Namespace)
	}
	if strings.TrimSpace(snap.scope) == inventoryScopeNamespace && namespace == "" {
		namespace = strings.TrimSpace(p.api.namespace)
	}
	var specs []inventoryWatchSpec
	if strings.TrimSpace(snap.scope) != inventoryScopeNamespace {
		specs = append(specs, inventoryWatchSpec{
			name:            "nodes",
			apiPath:         "/api/v1/nodes",
			resourceVersion: snap.resourceVersions["nodes"],
			resource:        watchResourceNodes,
		})
	}
	podPath := "/api/v1/pods"
	eventPath := "/api/v1/events"
	if namespace != "" {
		podPath = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
		eventPath = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/events"
	}
	specs = append(specs,
		inventoryWatchSpec{
			name:            resourceVersionKey("pods", namespace),
			apiPath:         podPath,
			resourceVersion: snap.resourceVersions[resourceVersionKey("pods", namespace)],
			resource:        watchResourcePods,
		},
		inventoryWatchSpec{
			name:            resourceVersionKey("events", namespace),
			apiPath:         eventPath,
			resourceVersion: snap.resourceVersions[resourceVersionKey("events", namespace)],
			resource:        watchResourceEvents,
		},
	)
	for _, spec := range workloadWatchSpecs(namespace, snap.resourceVersions) {
		specs = append(specs, spec)
	}
	return specs
}

func workloadWatchSpecs(namespace string, versions map[string]string) []inventoryWatchSpec {
	specs := []struct {
		group    string
		resource string
		kind     string
	}{
		{group: "apps", resource: "deployments", kind: "Deployment"},
		{group: "apps", resource: "statefulsets", kind: "StatefulSet"},
		{group: "apps", resource: "daemonsets", kind: "DaemonSet"},
		{group: "apps", resource: "replicasets", kind: "ReplicaSet"},
		{group: "batch", resource: "jobs", kind: "Job"},
		{group: "batch", resource: "cronjobs", kind: "CronJob"},
	}
	out := make([]inventoryWatchSpec, 0, len(specs))
	for _, spec := range specs {
		path := "/apis/" + spec.group + "/v1/" + spec.resource
		if namespace != "" {
			path = "/apis/" + spec.group + "/v1/namespaces/" + url.PathEscape(namespace) + "/" + spec.resource
		}
		key := resourceVersionKey(spec.group+"/"+spec.resource, namespace)
		out = append(out, inventoryWatchSpec{
			name:            key,
			apiPath:         path,
			resourceVersion: versions[key],
			resource:        watchResourceWorkloads,
			workloadKind:    spec.kind,
		})
	}
	return out
}

type inventorySnapshot struct {
	scope             string
	namespace         string
	resourceVersion   string
	resourceVersions  map[string]string
	collectDurationMS int64
	collectedAt       int64
	nodes             []tunnel.KubernetesNodeSnapshot
	workloads         []tunnel.KubernetesWorkloadSnapshot
	pods              []tunnel.KubernetesPodSnapshot
	events            []tunnel.KubernetesEventSnapshot
}

func (p *InventoryPusher) collect(ctx context.Context) (*inventorySnapshot, error) {
	start := time.Now()
	ns := strings.TrimSpace(p.info.Namespace)
	if ns == "" {
		ns = p.api.namespace
	}
	out := &inventorySnapshot{scope: inventoryScopeCluster, resourceVersions: map[string]string{}}
	defer func() {
		out.collectedAt = time.Now().Unix()
		out.collectDurationMS = time.Since(start).Milliseconds()
		out.resourceVersion = latestResourceVersion(out.resourceVersions)
	}()

	nodes, rv, err := p.api.listNodes(ctx)
	if err != nil {
		return nil, err
	}
	recordResourceVersion(out.resourceVersions, "nodes", rv)
	out.nodes = nodes

	pods, rv, err := p.api.listPods(ctx, "")
	if errors.Is(err, errForbidden) && ns != "" {
		out.scope = inventoryScopeNamespace
		out.namespace = ns
		pods, rv, err = p.api.listPods(ctx, ns)
	}
	if err != nil {
		return nil, err
	}
	recordResourceVersion(out.resourceVersions, resourceVersionKey("pods", out.namespace), rv)
	out.pods = pods

	events, rv, err := p.api.listEvents(ctx, "")
	if errors.Is(err, errForbidden) && ns != "" {
		out.scope = inventoryScopeNamespace
		out.namespace = ns
		events, rv, err = p.api.listEvents(ctx, ns)
	}
	if err != nil {
		return nil, err
	}
	recordResourceVersion(out.resourceVersions, resourceVersionKey("events", out.namespace), rv)
	out.events = events

	workloads, workloadVersions, err := p.collectWorkloads(ctx, "")
	if errors.Is(err, errForbidden) && ns != "" {
		out.scope = inventoryScopeNamespace
		out.namespace = ns
		workloads, workloadVersions, err = p.collectWorkloads(ctx, ns)
	}
	if err != nil {
		return nil, err
	}
	for key, rv := range workloadVersions {
		recordResourceVersion(out.resourceVersions, key, rv)
	}
	out.workloads = workloads
	return out, nil
}

func (c *apiClient) listEvents(ctx context.Context, namespace string) ([]tunnel.KubernetesEventSnapshot, string, error) {
	apiPath := "/api/v1/events"
	if namespace != "" {
		apiPath = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/events"
	}
	items, rv, err := listAllK8sItems[eventItem](ctx, c, apiPath)
	if err != nil {
		return nil, "", err
	}
	out := make([]tunnel.KubernetesEventSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, tunnel.KubernetesEventSnapshot{
			Namespace:           item.Metadata.Namespace,
			Name:                item.Metadata.Name,
			UID:                 item.Metadata.UID,
			Type:                item.Type,
			Reason:              item.Reason,
			Message:             k8sredact.Text(item.Message),
			InvolvedKind:        item.InvolvedObject.Kind,
			InvolvedNamespace:   item.InvolvedObject.Namespace,
			InvolvedName:        item.InvolvedObject.Name,
			InvolvedUID:         item.InvolvedObject.UID,
			SourceComponent:     item.Source.Component,
			SourceHost:          item.Source.Host,
			ReportingController: item.ReportingComponent,
			ReportingInstance:   item.ReportingInstance,
			Action:              item.Action,
			Count:               item.Count,
			FirstTimestamp:      item.FirstTimestamp,
			LastTimestamp:       item.LastTimestamp,
			EventTime:           item.EventTime,
		})
	}
	return out, rv, nil
}

func (p *InventoryPusher) collectWorkloads(ctx context.Context, namespace string) ([]tunnel.KubernetesWorkloadSnapshot, map[string]string, error) {
	specs := []struct {
		kind     string
		group    string
		resource string
	}{
		{kind: "Deployment", group: "apps", resource: "deployments"},
		{kind: "StatefulSet", group: "apps", resource: "statefulsets"},
		{kind: "DaemonSet", group: "apps", resource: "daemonsets"},
		{kind: "ReplicaSet", group: "apps", resource: "replicasets"},
		{kind: "Job", group: "batch", resource: "jobs"},
		{kind: "CronJob", group: "batch", resource: "cronjobs"},
	}
	var out []tunnel.KubernetesWorkloadSnapshot
	versions := map[string]string{}
	for _, spec := range specs {
		items, rv, err := p.api.listWorkloads(ctx, spec.group, spec.resource, spec.kind, namespace)
		if err != nil {
			if errors.Is(err, errNotFound) {
				continue
			}
			return nil, nil, err
		}
		recordResourceVersion(versions, resourceVersionKey(spec.group+"/"+spec.resource, namespace), rv)
		out = append(out, items...)
	}
	return out, versions, nil
}

func recordResourceVersion(versions map[string]string, key, rv string) {
	key = strings.TrimSpace(key)
	rv = strings.TrimSpace(rv)
	if versions == nil || key == "" || rv == "" {
		return
	}
	versions[key] = rv
}

func resourceVersionKey(resource, namespace string) string {
	resource = strings.TrimSpace(resource)
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return resource
	}
	return resource + ":" + namespace
}

func latestResourceVersion(versions map[string]string) string {
	latest := ""
	for _, rv := range versions {
		latest = newerResourceVersion(latest, rv)
	}
	return latest
}

func newerResourceVersion(current, candidate string) string {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return current
	}
	if current == "" {
		return candidate
	}
	currentNum, currentErr := strconv.ParseUint(current, 10, 64)
	candidateNum, candidateErr := strconv.ParseUint(candidate, 10, 64)
	if currentErr == nil && candidateErr == nil {
		if candidateNum > currentNum {
			return candidate
		}
		return current
	}
	if candidate > current {
		return candidate
	}
	return current
}

func isOptionalAPIError(err error) bool {
	return errors.Is(err, errForbidden) || errors.Is(err, errNotFound)
}

func isControllerRole(role string) bool {
	return role == "controller"
}

type apiClient struct {
	baseURL   string
	token     string
	namespace string
	http      *http.Client
}

func newInClusterAPIClient() (*apiClient, error) {
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	base := "https://kubernetes.default.svc"
	if host != "" {
		if port == "" {
			port = "443"
		}
		base = "https://" + netJoinHostPort(host, port)
	}
	serviceAccountDir := strings.TrimSpace(os.Getenv("ONGRID_K8S_SERVICE_ACCOUNT_DIR"))
	if serviceAccountDir == "" {
		serviceAccountDir = defaultServiceAccountDir
	}
	tokenBytes, err := os.ReadFile(filepath.Join(serviceAccountDir, "token"))
	if err != nil {
		return nil, fmt.Errorf("read service account token: %w", err)
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(filepath.Join(serviceAccountDir, "ca.crt")); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	namespace := ""
	if b, err := os.ReadFile(filepath.Join(serviceAccountDir, "namespace")); err == nil {
		namespace = strings.TrimSpace(string(b))
	}
	return &apiClient{
		baseURL:   strings.TrimRight(base, "/"),
		token:     strings.TrimSpace(string(tokenBytes)),
		namespace: namespace,
		http:      &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}, nil
}

func netJoinHostPort(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

func (c *apiClient) listNodes(ctx context.Context) ([]tunnel.KubernetesNodeSnapshot, string, error) {
	items, rv, err := listAllK8sItems[nodeItem](ctx, c, "/api/v1/nodes")
	if err != nil {
		return nil, "", err
	}
	out := make([]tunnel.KubernetesNodeSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, tunnel.KubernetesNodeSnapshot{
			Name:           item.Metadata.Name,
			UID:            item.Metadata.UID,
			ProviderID:     item.Spec.ProviderID,
			Labels:         k8sredact.StringMap(item.Metadata.Labels),
			Taints:         item.Spec.Taints,
			Conditions:     conditionMaps(item.Status.Conditions),
			Capacity:       item.Status.Capacity,
			Allocatable:    item.Status.Allocatable,
			KubeletVersion: item.Status.NodeInfo.KubeletVersion,
		})
	}
	return out, rv, nil
}

func (c *apiClient) listPods(ctx context.Context, namespace string) ([]tunnel.KubernetesPodSnapshot, string, error) {
	apiPath := "/api/v1/pods"
	if namespace != "" {
		apiPath = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	items, rv, err := listAllK8sItems[podItem](ctx, c, apiPath)
	if err != nil {
		return nil, "", err
	}
	out := make([]tunnel.KubernetesPodSnapshot, 0, len(items))
	for _, item := range items {
		ownerKind, ownerName := controllerOwner(item.Metadata.OwnerReferences)
		out = append(out, tunnel.KubernetesPodSnapshot{
			Namespace:    item.Metadata.Namespace,
			Name:         item.Metadata.Name,
			UID:          item.Metadata.UID,
			NodeName:     item.Spec.NodeName,
			Phase:        item.Status.Phase,
			OwnerKind:    ownerKind,
			OwnerName:    ownerName,
			RestartCount: podRestartCount(item.Status.ContainerStatuses),
			Reason:       podReason(item.Status),
		})
	}
	return out, rv, nil
}

func (c *apiClient) listWorkloads(ctx context.Context, group, resource, kind, namespace string) ([]tunnel.KubernetesWorkloadSnapshot, string, error) {
	apiPath := "/apis/" + group + "/v1/" + resource
	if namespace != "" {
		apiPath = "/apis/" + group + "/v1/namespaces/" + url.PathEscape(namespace) + "/" + resource
	}
	items, rv, err := listAllK8sItems[workloadItem](ctx, c, apiPath)
	if err != nil {
		return nil, "", err
	}
	out := make([]tunnel.KubernetesWorkloadSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, tunnel.KubernetesWorkloadSnapshot{
			Kind:            kind,
			Namespace:       item.Metadata.Namespace,
			Name:            item.Metadata.Name,
			UID:             item.Metadata.UID,
			DesiredReplicas: desiredReplicas(kind, item),
			ReadyReplicas:   readyReplicas(kind, item),
			Labels:          k8sredact.StringMap(item.Metadata.Labels),
			Annotations:     k8sredact.StringMap(item.Metadata.Annotations),
			Conditions:      conditionMaps(item.Status.Conditions),
		})
	}
	return out, rv, nil
}

const k8sListPageSize = 500

type k8sListPage[T any] struct {
	Metadata listMeta `json:"metadata"`
	Items    []T      `json:"items"`
}

func listAllK8sItems[T any](ctx context.Context, c *apiClient, apiPath string) ([]T, string, error) {
	items := make([]T, 0, k8sListPageSize)
	continueToken := ""
	resourceVersion := ""
	for {
		parsed, err := url.Parse(apiPath)
		if err != nil {
			return nil, "", err
		}
		query := parsed.Query()
		query.Set("limit", strconv.Itoa(k8sListPageSize))
		if continueToken != "" {
			query.Set("continue", continueToken)
		}
		parsed.RawQuery = query.Encode()
		var page k8sListPage[T]
		if err := c.get(ctx, parsed.String(), &page); err != nil {
			return nil, "", err
		}
		items = append(items, page.Items...)
		if resourceVersion == "" {
			resourceVersion = page.Metadata.ResourceVersion
		}
		continueToken = page.Metadata.Continue
		if continueToken == "" {
			return items, resourceVersion, nil
		}
	}
}

func (c *apiClient) get(ctx context.Context, apiPath string, dst any) error {
	body, err := c.getRaw(ctx, apiPath)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}

func (c *apiClient) getRaw(ctx context.Context, apiPath string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, apiPath, "", nil)
}

type k8sWatchEvent struct {
	Type   string          `json:"type"`
	Object json.RawMessage `json:"object"`
}

func (c *apiClient) watch(ctx context.Context, apiPath, resourceVersion string, onEvent func(k8sWatchEvent) error) (string, error) {
	apiPath = watchAPIPath(apiPath, resourceVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+apiPath, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return "", errForbidden
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", errNotFound
	}
	if resp.StatusCode == http.StatusGone {
		return "", errResourceExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if readErr != nil {
			return "", fmt.Errorf("kubernetes watch %s: status=%d read body: %w", apiPath, resp.StatusCode, readErr)
		}
		return "", fmt.Errorf("kubernetes watch %s: status=%d body=%s", apiPath, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	latest := strings.TrimSpace(resourceVersion)
	dec := json.NewDecoder(resp.Body)
	for {
		var event k8sWatchEvent
		if err := dec.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return latest, nil
			}
			return latest, fmt.Errorf("decode kubernetes watch %s: %w", apiPath, err)
		}
		if rv := eventResourceVersion(event); rv != "" {
			latest = rv
		}
		if onEvent != nil {
			if err := onEvent(event); err != nil {
				return latest, err
			}
		}
	}
}

func watchAPIPath(apiPath, resourceVersion string) string {
	u, err := url.Parse(apiPath)
	if err != nil {
		return apiPath
	}
	q := u.Query()
	q.Set("watch", "1")
	q.Set("allowWatchBookmarks", "true")
	q.Set("timeoutSeconds", strconv.Itoa(watchTimeoutSeconds))
	if rv := strings.TrimSpace(resourceVersion); rv != "" {
		q.Set("resourceVersion", rv)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func eventResourceVersion(event k8sWatchEvent) string {
	if len(event.Object) == 0 {
		return ""
	}
	var meta struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(event.Object, &meta); err != nil {
		return ""
	}
	return strings.TrimSpace(meta.Metadata.ResourceVersion)
}

type objectMeta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	ResourceVersion string            `json:"resourceVersion"`
	Labels          map[string]string `json:"labels"`
	Annotations     map[string]string `json:"annotations"`
	OwnerReferences []ownerRef        `json:"ownerReferences"`
}

type listMeta struct {
	ResourceVersion string `json:"resourceVersion"`
	Continue        string `json:"continue"`
}

type ownerRef struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Controller *bool  `json:"controller"`
}

type k8sCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type nodeList struct {
	Metadata listMeta   `json:"metadata"`
	Items    []nodeItem `json:"items"`
}

type nodeItem struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		ProviderID string           `json:"providerID"`
		Taints     []map[string]any `json:"taints"`
	} `json:"spec"`
	Status struct {
		Conditions  []k8sCondition    `json:"conditions"`
		Capacity    map[string]string `json:"capacity"`
		Allocatable map[string]string `json:"allocatable"`
		NodeInfo    struct {
			KubeletVersion string `json:"kubeletVersion"`
		} `json:"nodeInfo"`
	} `json:"status"`
}

type workloadList struct {
	Metadata listMeta       `json:"metadata"`
	Items    []workloadItem `json:"items"`
}

type workloadItem struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Replicas    *int `json:"replicas"`
		Completions *int `json:"completions"`
	} `json:"spec"`
	Status struct {
		Replicas               int            `json:"replicas"`
		ReadyReplicas          int            `json:"readyReplicas"`
		AvailableReplicas      int            `json:"availableReplicas"`
		DesiredNumberScheduled int            `json:"desiredNumberScheduled"`
		NumberReady            int            `json:"numberReady"`
		Succeeded              int            `json:"succeeded"`
		Active                 int            `json:"active"`
		Conditions             []k8sCondition `json:"conditions"`
	} `json:"status"`
}

type podList struct {
	Metadata listMeta  `json:"metadata"`
	Items    []podItem `json:"items"`
}

type podItem struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name  string `json:"name"`
			Ports []struct {
				Name          string `json:"name"`
				ContainerPort int    `json:"containerPort"`
				Protocol      string `json:"protocol"`
			} `json:"ports"`
		} `json:"containers"`
		Volumes []struct {
			EmptyDir *struct{} `json:"emptyDir"`
		} `json:"volumes"`
	} `json:"spec"`
	Status podStatus `json:"status"`
}

type podStatus struct {
	PodIP             string            `json:"podIP"`
	Phase             string            `json:"phase"`
	Reason            string            `json:"reason"`
	ContainerStatuses []containerStatus `json:"containerStatuses"`
}

type eventList struct {
	Metadata listMeta    `json:"metadata"`
	Items    []eventItem `json:"items"`
}

type eventItem struct {
	Metadata       objectMeta `json:"metadata"`
	InvolvedObject struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		UID       string `json:"uid"`
	} `json:"involvedObject"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Source  struct {
		Component string `json:"component"`
		Host      string `json:"host"`
	} `json:"source"`
	ReportingComponent string `json:"reportingComponent"`
	ReportingInstance  string `json:"reportingInstance"`
	Action             string `json:"action"`
	Count              int    `json:"count"`
	FirstTimestamp     string `json:"firstTimestamp"`
	LastTimestamp      string `json:"lastTimestamp"`
	EventTime          string `json:"eventTime"`
}

type containerStatus struct {
	RestartCount int `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Terminated *struct {
			Reason string `json:"reason"`
		} `json:"terminated"`
	} `json:"state"`
}

func conditionMaps(in []k8sCondition) []map[string]string {
	out := make([]map[string]string, 0, len(in))
	for _, c := range in {
		out = append(out, map[string]string{
			"type":    c.Type,
			"status":  c.Status,
			"reason":  c.Reason,
			"message": c.Message,
		})
	}
	return out
}

func controllerOwner(refs []ownerRef) (string, string) {
	if len(refs) == 0 {
		return "", ""
	}
	for _, ref := range refs {
		if ref.Controller != nil && *ref.Controller {
			return ref.Kind, ref.Name
		}
	}
	return refs[0].Kind, refs[0].Name
}

func podRestartCount(items []containerStatus) int {
	total := 0
	for _, item := range items {
		total += item.RestartCount
	}
	return total
}

func podReason(status podStatus) string {
	if strings.TrimSpace(status.Reason) != "" {
		return status.Reason
	}
	for _, item := range status.ContainerStatuses {
		if item.State.Waiting != nil && strings.TrimSpace(item.State.Waiting.Reason) != "" {
			return item.State.Waiting.Reason
		}
		if item.State.Terminated != nil && strings.TrimSpace(item.State.Terminated.Reason) != "" {
			return item.State.Terminated.Reason
		}
	}
	return ""
}

func desiredReplicas(kind string, item workloadItem) int {
	if item.Spec.Replicas != nil {
		return *item.Spec.Replicas
	}
	if kind == "DaemonSet" {
		return item.Status.DesiredNumberScheduled
	}
	if item.Spec.Completions != nil {
		return *item.Spec.Completions
	}
	return item.Status.Replicas
}

func readyReplicas(kind string, item workloadItem) int {
	if kind == "DaemonSet" {
		return item.Status.NumberReady
	}
	if kind == "Job" {
		return item.Status.Succeeded
	}
	return item.Status.ReadyReplicas
}
