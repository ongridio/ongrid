package k8s

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestAPIClientWatchDecodesEventsAndBookmarks(t *testing.T) {
	var gotPath, gotWatch, gotRV, gotBookmarks, gotTimeout string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotWatch = r.URL.Query().Get("watch")
		gotRV = r.URL.Query().Get("resourceVersion")
		gotBookmarks = r.URL.Query().Get("allowWatchBookmarks")
		gotTimeout = r.URL.Query().Get("timeoutSeconds")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"ADDED","object":{"metadata":{"resourceVersion":"101","name":"api-1"}}}` + "\n"))
		_, _ = w.Write([]byte(`{"type":"BOOKMARK","object":{"metadata":{"resourceVersion":"102"}}}` + "\n"))
	}))
	defer srv.Close()

	c := &apiClient{
		baseURL: srv.URL,
		token:   "test-token",
		http:    srv.Client(),
	}
	var gotTypes []string
	latest, err := c.watch(context.Background(), "/api/v1/pods", "100", func(event k8sWatchEvent) error {
		gotTypes = append(gotTypes, event.Type)
		return nil
	})
	if err != nil {
		t.Fatalf("watch() error = %v", err)
	}
	if gotPath != "/api/v1/pods" || gotWatch != "1" || gotRV != "100" || gotBookmarks != "true" || gotTimeout == "" {
		t.Fatalf("unexpected watch request path=%q watch=%q rv=%q bookmarks=%q timeout=%q", gotPath, gotWatch, gotRV, gotBookmarks, gotTimeout)
	}
	if latest != "102" {
		t.Fatalf("latest resourceVersion = %q, want 102", latest)
	}
	if len(gotTypes) != 2 || gotTypes[0] != "ADDED" || gotTypes[1] != "BOOKMARK" {
		t.Fatalf("event types = %v", gotTypes)
	}
}

func TestInventoryPusherWatchSpecsNamespaceScopeExcludeNodes(t *testing.T) {
	p := &InventoryPusher{
		info: tunnel.KubernetesInfo{ClusterID: 7, Role: "controller", Namespace: "apps"},
		api:  &apiClient{namespace: "apps"},
	}
	specs := p.watchSpecs(&inventorySnapshot{
		scope:     inventoryScopeNamespace,
		namespace: "apps",
		resourceVersions: map[string]string{
			"pods:apps":             "10",
			"events:apps":           "11",
			"apps/deployments:apps": "12",
		},
	})
	if len(specs) == 0 {
		t.Fatalf("expected namespace watch specs")
	}
	for _, spec := range specs {
		if spec.name == "nodes" {
			t.Fatalf("namespace watch specs must not include nodes: %+v", specs)
		}
		if spec.apiPath == "/api/v1/pods" || spec.apiPath == "/api/v1/events" {
			t.Fatalf("namespace watch spec must use namespaced API path: %+v", spec)
		}
	}
}

func TestInventoryCacheApplyWatchDeleteBuildsDelta(t *testing.T) {
	cache := newInventoryCache(&inventorySnapshot{
		scope: inventoryScopeCluster,
		pods: []tunnel.KubernetesPodSnapshot{{
			Namespace: "default",
			Name:      "api-1",
			UID:       "pod-uid-1",
			Phase:     "Running",
		}},
	})
	spec := inventoryWatchSpec{
		name:     "pods",
		resource: watchResourcePods,
	}
	trigger, err := cache.applyWatchEvent(spec, k8sWatchEvent{
		Type:   "DELETED",
		Object: []byte(`{"metadata":{"namespace":"default","name":"api-1","uid":"pod-uid-1","resourceVersion":"123"}}`),
	}, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("applyWatchEvent() error = %v", err)
	}
	if trigger.syncType != inventorySyncDelta || trigger.resourceVersion != "123" {
		t.Fatalf("trigger metadata = sync:%q rv:%q", trigger.syncType, trigger.resourceVersion)
	}
	if len(trigger.deletedPods) != 1 || trigger.deletedPods[0].UID != "pod-uid-1" {
		t.Fatalf("deleted pods = %+v, want pod-uid-1", trigger.deletedPods)
	}
	if len(cache.pods) != 0 {
		t.Fatalf("cache pods len = %d, want 0", len(cache.pods))
	}
}

func TestInventoryPusherPushDeltaUsesDeltaPayload(t *testing.T) {
	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		log:    slog.Default(),
	}
	trigger := newInventoryWatchTrigger("pods:ADDED", time.Unix(100, 0))
	trigger.resourceVersion = "124"
	trigger.resourceVersions = map[string]string{"pods": "124"}
	trigger.pods = []tunnel.KubernetesPodSnapshot{{
		Namespace: "default",
		Name:      "api-2",
		UID:       "pod-uid-2",
		Phase:     "Pending",
	}}
	if err := p.pushDelta(context.Background(), 55, &inventorySnapshot{scope: inventoryScopeCluster}, trigger); err != nil {
		t.Fatalf("pushDelta() error = %v", err)
	}
	req, ok := fc.lastRequest.(tunnel.KubernetesInventoryRequest)
	if !ok {
		t.Fatalf("request type = %T, want KubernetesInventoryRequest", fc.lastRequest)
	}
	if req.SyncType != inventorySyncDelta {
		t.Fatalf("SyncType = %q, want delta", req.SyncType)
	}
	if req.ResourceVersion != "124" || req.ResourceVersions["pods"] != "124" {
		t.Fatalf("resource versions = rv:%q all:%v", req.ResourceVersion, req.ResourceVersions)
	}
	if len(req.Pods) != 1 || req.Pods[0].UID != "pod-uid-2" {
		t.Fatalf("pods = %+v, want pod-uid-2", req.Pods)
	}
	if req.WatchEventObservedAt != 100 || req.WatchTriggerReason != "pods:ADDED" {
		t.Fatalf("watch metadata = observed:%d reason:%q", req.WatchEventObservedAt, req.WatchTriggerReason)
	}
}
