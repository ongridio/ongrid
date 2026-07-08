package k8s

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestRegisterHandlersDescribePod(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/namespaces/default/pods/api-1":
			_, _ = w.Write([]byte(`{
				"apiVersion":"v1",
				"kind":"Pod",
				"metadata":{
					"name":"api-1",
					"namespace":"default",
					"uid":"pod-uid",
					"resourceVersion":"123",
					"annotations":{"large":"drop-me"},
					"managedFields":[{"manager":"kubectl"}],
					"labels":{"app":"api"}
				},
				"spec":{"nodeName":"node-a"},
				"status":{"phase":"Running"}
			}`))
		case "/api/v1/namespaces/default/events":
			_, _ = w.Write([]byte(`{"items":[
				{
					"metadata":{"name":"api-1.1","namespace":"default","uid":"event-1"},
					"involvedObject":{"kind":"Pod","namespace":"default","name":"api-1","uid":"pod-uid"},
					"type":"Normal",
					"reason":"Started",
					"message":"Started container"
				},
				{
					"metadata":{"name":"other.1","namespace":"default","uid":"event-2"},
					"involvedObject":{"kind":"Pod","namespace":"default","name":"other","uid":"other-uid"},
					"type":"Warning",
					"reason":"BackOff",
					"message":"ignored"
				}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		api: &apiClient{
			baseURL: srv.URL,
			token:   "test-token",
			http:    srv.Client(),
		},
	}
	p.RegisterHandlers()
	h := fc.handlers[tunnel.MethodDescribeK8sResource]
	if h == nil {
		t.Fatalf("handler %q not registered", tunnel.MethodDescribeK8sResource)
	}

	body, _ := json.Marshal(tunnel.KubernetesDescribeResourceRequest{
		ClusterID:     7,
		Kind:          "pod",
		Namespace:     "default",
		Name:          "api-1",
		IncludeEvents: true,
		EventsLimit:   10,
	})
	out, err := h(context.Background(), tunnel.Session{}, tunnel.MethodDescribeK8sResource, body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var resp tunnel.KubernetesDescribeResourceResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, out)
	}
	if resp.ClusterID != 7 || resp.Kind != "Pod" || resp.Namespace != "default" || resp.Name != "api-1" {
		t.Fatalf("unexpected response identity: %+v", resp)
	}
	if resp.UID != "pod-uid" || resp.ResourceVersion != "123" {
		t.Fatalf("unexpected metadata: uid=%q rv=%q", resp.UID, resp.ResourceVersion)
	}
	if len(resp.Events) != 1 || resp.Events[0].Reason != "Started" {
		t.Fatalf("unexpected events: %+v", resp.Events)
	}
	var object map[string]any
	if err := json.Unmarshal(resp.Object, &object); err != nil {
		t.Fatalf("decode object: %v", err)
	}
	meta := object["metadata"].(map[string]any)
	if _, ok := meta["managedFields"]; ok {
		t.Fatalf("managedFields should be removed: %s", resp.Object)
	}
	if _, ok := meta["annotations"]; ok {
		t.Fatalf("annotations should be removed: %s", resp.Object)
	}
	wantPaths := []string{"/api/v1/namespaces/default/pods/api-1", "/api/v1/namespaces/default/events"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("paths=%v want %v", gotPaths, wantPaths)
	}
	for i := range wantPaths {
		if gotPaths[i] != wantPaths[i] {
			t.Fatalf("paths=%v want %v", gotPaths, wantPaths)
		}
	}
}

func TestDescribeResourceRejectsDisallowedKind(t *testing.T) {
	api := &apiClient{baseURL: "http://127.0.0.1", token: "token", http: http.DefaultClient}
	_, err := api.describeResource(context.Background(), tunnel.KubernetesDescribeResourceRequest{
		ClusterID: 1,
		Kind:      "Secret",
		Namespace: "default",
		Name:      "token",
	})
	if err == nil {
		t.Fatalf("expected disallowed kind error")
	}
}

func TestRegisterHandlersQueryPodLogs(t *testing.T) {
	var gotPath, gotContainer, gotTail, gotLimit, gotSince, gotPrevious, gotTimestamps string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContainer = r.URL.Query().Get("container")
		gotTail = r.URL.Query().Get("tailLines")
		gotLimit = r.URL.Query().Get("limitBytes")
		gotSince = r.URL.Query().Get("sinceSeconds")
		gotPrevious = r.URL.Query().Get("previous")
		gotTimestamps = r.URL.Query().Get("timestamps")
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1/namespaces/default/pods/api-1/log" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("2026-06-30T00:00:00Z booted\n2026-06-30T00:00:01Z ready\n"))
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		api: &apiClient{
			baseURL: srv.URL,
			token:   "test-token",
			http:    srv.Client(),
		},
	}
	p.RegisterHandlers()
	h := fc.handlers[tunnel.MethodQueryK8sLogs]
	if h == nil {
		t.Fatalf("handler %q not registered", tunnel.MethodQueryK8sLogs)
	}

	body, _ := json.Marshal(tunnel.KubernetesPodLogsRequest{
		ClusterID:    7,
		Namespace:    "default",
		Pod:          "api-1",
		Container:    "api",
		Previous:     true,
		SinceSeconds: 120,
		TailLines:    5,
		LimitBytes:   4096,
		Timestamps:   true,
	})
	out, err := h(context.Background(), tunnel.Session{}, tunnel.MethodQueryK8sLogs, body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if gotPath != "/api/v1/namespaces/default/pods/api-1/log" ||
		gotContainer != "api" || gotTail != "5" || gotLimit != "4096" ||
		gotSince != "120" || gotPrevious != "true" || gotTimestamps != "true" {
		t.Fatalf("unexpected pod log request path=%q container=%q tail=%q limit=%q since=%q previous=%q timestamps=%q",
			gotPath, gotContainer, gotTail, gotLimit, gotSince, gotPrevious, gotTimestamps)
	}
	var resp tunnel.KubernetesPodLogsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, out)
	}
	if resp.ClusterID != 7 || resp.Namespace != "default" || resp.Pod != "api-1" || resp.Container != "api" {
		t.Fatalf("unexpected identity: %+v", resp)
	}
	if resp.LineCount != 2 || resp.Bytes == 0 || resp.Logs == "" {
		t.Fatalf("unexpected log payload: %+v", resp)
	}
}

func TestQueryPodLogsRejectsWrongCluster(t *testing.T) {
	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		api:    &apiClient{baseURL: "http://127.0.0.1", token: "token", http: http.DefaultClient},
	}
	p.RegisterHandlers()
	h := fc.handlers[tunnel.MethodQueryK8sLogs]
	body, _ := json.Marshal(tunnel.KubernetesPodLogsRequest{
		ClusterID: 8,
		Namespace: "default",
		Pod:       "api-1",
	})
	if _, err := h(context.Background(), tunnel.Session{}, tunnel.MethodQueryK8sLogs, body); err == nil {
		t.Fatalf("expected cluster mismatch error")
	}
}

func TestInventoryPusherIncludesResourceVersions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"100"},"items":[{"metadata":{"name":"node-a","uid":"node-uid-a"},"status":{"nodeInfo":{"kubeletVersion":"v1.34.0"}}}]}`))
		case "/api/v1/pods":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"101"},"items":[]}`))
		case "/api/v1/events":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"99"},"items":[]}`))
		case "/apis/apps/v1/deployments":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"102"},"items":[{"metadata":{"namespace":"default","name":"api","uid":"deploy-uid"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		api: &apiClient{
			baseURL: srv.URL,
			token:   "test-token",
			http:    srv.Client(),
		},
		log: slog.Default(),
	}
	if err := p.pushOnce(context.Background(), 55); err != nil {
		t.Fatalf("pushOnce() error = %v", err)
	}
	if fc.lastMethod != tunnel.MethodPushK8sInventory {
		t.Fatalf("method = %q, want %q", fc.lastMethod, tunnel.MethodPushK8sInventory)
	}
	req, ok := fc.lastRequest.(tunnel.KubernetesInventoryRequest)
	if !ok {
		t.Fatalf("request type = %T, want KubernetesInventoryRequest", fc.lastRequest)
	}
	if req.ResourceVersion != "102" {
		t.Fatalf("ResourceVersion = %q, want 102", req.ResourceVersion)
	}
	wantVersions := map[string]string{
		"nodes":            "100",
		"pods":             "101",
		"events":           "99",
		"apps/deployments": "102",
	}
	for key, want := range wantVersions {
		if got := req.ResourceVersions[key]; got != want {
			t.Fatalf("ResourceVersions[%q] = %q, want %q; all=%v", key, got, want, req.ResourceVersions)
		}
	}
	if req.Ts == 0 || req.CollectDurationMS < 0 {
		t.Fatalf("sync timing not populated: ts=%d duration=%d", req.Ts, req.CollectDurationMS)
	}
}

func TestInventoryPusherIncludesWatchTriggerMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"100"},"items":[]}`))
		case "/api/v1/pods":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"101"},"items":[]}`))
		case "/api/v1/events":
			_, _ = w.Write([]byte(`{"metadata":{"resourceVersion":"99"},"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	fc := &fakeTunnelClient{handlers: map[string]tunnel.Handler{}}
	p := &InventoryPusher{
		client: fc,
		info:   tunnel.KubernetesInfo{ClusterID: 7, Role: "controller"},
		api: &apiClient{
			baseURL: srv.URL,
			token:   "test-token",
			http:    srv.Client(),
		},
		log: slog.Default(),
	}
	observedAt := time.Now().Add(-3 * time.Second).UTC()
	if _, err := p.pushOnceWithSnapshot(context.Background(), 55, newInventoryWatchTrigger("pods:MODIFIED", observedAt)); err != nil {
		t.Fatalf("pushOnceWithSnapshot() error = %v", err)
	}
	req, ok := fc.lastRequest.(tunnel.KubernetesInventoryRequest)
	if !ok {
		t.Fatalf("request type = %T, want KubernetesInventoryRequest", fc.lastRequest)
	}
	if req.WatchEventObservedAt != observedAt.Unix() {
		t.Fatalf("WatchEventObservedAt = %d, want %d", req.WatchEventObservedAt, observedAt.Unix())
	}
	if req.WatchTriggerReason != "pods:MODIFIED" {
		t.Fatalf("WatchTriggerReason = %q, want pods:MODIFIED", req.WatchTriggerReason)
	}
}

type fakeTunnelClient struct {
	handlers    map[string]tunnel.Handler
	lastMethod  string
	lastRequest any
}

func (f *fakeTunnelClient) Dial(context.Context) error { return nil }

func (f *fakeTunnelClient) RegisterHandler(method string, h tunnel.Handler) {
	f.handlers[method] = h
}

func (f *fakeTunnelClient) Call(_ context.Context, method string, req any, resp any) error {
	f.lastMethod = method
	f.lastRequest = req
	if out, ok := resp.(*tunnel.KubernetesInventoryResponse); ok {
		*out = tunnel.KubernetesInventoryResponse{}
	}
	if out, ok := resp.(*tunnel.PushPromSamplesResponse); ok {
		if in, ok := req.(tunnel.PushPromSamplesRequest); ok {
			out.Accepted = len(in.Samples)
		}
	}
	return nil
}

func (f *fakeTunnelClient) AcceptStream() (tunnel.StreamConn, error) { return nil, nil }

func (f *fakeTunnelClient) OnReconnect(func()) {}

func (f *fakeTunnelClient) Close() error { return nil }
