package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestRegisterHandlersExecuteK8sActionRolloutRestart(t *testing.T) {
	var patchSeen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/apis/apps/v1/namespaces/default/deployments/api" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default","uid":"deploy-uid","resourceVersion":"10"}}`))
		case http.MethodPatch:
			patchSeen = true
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, kubernetesMergePatchContentType) {
				t.Fatalf("Content-Type = %q, want %s", ct, kubernetesMergePatchContentType)
			}
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			spec := patch["spec"].(map[string]any)
			template := spec["template"].(map[string]any)
			metadata := template["metadata"].(map[string]any)
			annotations := metadata["annotations"].(map[string]any)
			if annotations["kubectl.kubernetes.io/restartedAt"] == "" {
				t.Fatalf("restart annotation missing: %#v", annotations)
			}
			_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default","uid":"deploy-uid","resourceVersion":"11"}}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
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
	h := fc.handlers[tunnel.MethodExecuteK8sAction]
	if h == nil {
		t.Fatalf("handler %q not registered", tunnel.MethodExecuteK8sAction)
	}

	body, _ := json.Marshal(tunnel.KubernetesActionRequest{
		ClusterID:               7,
		Action:                  "rollout_restart",
		Kind:                    "Deployment",
		Namespace:               "default",
		Name:                    "api",
		ExpectedResourceVersion: "10",
	})
	out, err := h(context.Background(), tunnel.Session{}, tunnel.MethodExecuteK8sAction, body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !patchSeen {
		t.Fatalf("PATCH was not called")
	}
	var resp tunnel.KubernetesActionResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, out)
	}
	if resp.ClusterID != 7 || resp.Action != "rollout_restart" || resp.Kind != "Deployment" || !resp.Applied {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Preflight.UID != "deploy-uid" || resp.Preflight.ResourceVersion != "10" || resp.ResultResourceVersion != "11" {
		t.Fatalf("unexpected versions: %+v", resp)
	}
}

func TestExecuteK8sActionRejectsResourceVersionConflict(t *testing.T) {
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			t.Fatalf("unexpected write method %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default","uid":"deploy-uid","resourceVersion":"10"}}`))
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	_, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID:               7,
		Action:                  "rollout_restart",
		Kind:                    "Deployment",
		Namespace:               "default",
		Name:                    "api",
		ExpectedResourceVersion: "9",
	})
	if err == nil || !strings.Contains(err.Error(), "resourceVersion conflict") {
		t.Fatalf("err=%v, want resourceVersion conflict", err)
	}
	if writes != 0 {
		t.Fatalf("writes=%d want 0", writes)
	}
}

func TestExecuteK8sActionDryRunDoesNotMutate(t *testing.T) {
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
			t.Fatalf("unexpected write method %s", r.Method)
		}
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"default","uid":"deploy-uid","resourceVersion":"10"}}`))
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	resp, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID: 7,
		Action:    "scale",
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "api",
		Replicas:  intPtr(3),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if resp.Applied || !resp.DryRun || resp.Preflight.ResourceVersion != "10" {
		t.Fatalf("unexpected dry-run response: %+v", resp)
	}
	if writes != 0 {
		t.Fatalf("writes=%d want 0", writes)
	}
}

func TestExecuteK8sActionCordonNodePatchesUnschedulable(t *testing.T) {
	var patched bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/node-a" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"20"}}`))
		case http.MethodPatch:
			patched = true
			if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, kubernetesMergePatchContentType) {
				t.Fatalf("Content-Type = %q, want %s", ct, kubernetesMergePatchContentType)
			}
			var patch map[string]any
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Fatalf("decode patch: %v", err)
			}
			spec := patch["spec"].(map[string]any)
			if spec["unschedulable"] != true {
				t.Fatalf("unschedulable patch = %#v, want true", spec["unschedulable"])
			}
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"21"}}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	resp, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID:               7,
		Action:                  "cordon",
		Name:                    "node-a",
		ExpectedResourceVersion: "20",
	})
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if !patched || !resp.Applied || resp.Kind != "Node" || resp.ResultResourceVersion != "21" {
		t.Fatalf("unexpected cordon result patched=%v resp=%+v", patched, resp)
	}
}

func TestExecuteK8sActionDrainCordonsAndEvictsEligiblePods(t *testing.T) {
	var patched bool
	var evictions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"20"}}`))
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodPatch:
			patched = true
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"21"}}`))
		case r.URL.Path == "/api/v1/pods" && r.Method == http.MethodGet:
			if got := r.URL.Query().Get("fieldSelector"); got != "spec.nodeName=node-a" {
				t.Fatalf("fieldSelector = %q, want spec.nodeName=node-a", got)
			}
			_, _ = w.Write([]byte(`{
			  "apiVersion":"v1",
			  "kind":"PodList",
			  "metadata":{"resourceVersion":"30"},
			  "items":[
			    {"metadata":{"namespace":"default","name":"api-1","uid":"pod-1","ownerReferences":[{"kind":"ReplicaSet","name":"api-rs","controller":true}]},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}},
			    {"metadata":{"namespace":"kube-system","name":"kube-proxy","uid":"pod-2","ownerReferences":[{"kind":"DaemonSet","name":"kube-proxy","controller":true}]},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}},
			    {"metadata":{"namespace":"kube-system","name":"static","uid":"pod-3","annotations":{"kubernetes.io/config.mirror":"mirror-uid"}},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}}
			  ]
			}`))
		case r.URL.Path == "/api/v1/namespaces/default/pods/api-1/eviction" && r.Method == http.MethodPost:
			evictions = append(evictions, r.URL.Path)
			var eviction map[string]any
			if err := json.NewDecoder(r.Body).Decode(&eviction); err != nil {
				t.Fatalf("decode eviction: %v", err)
			}
			if eviction["apiVersion"] != "policy/v1" || eviction["kind"] != "Eviction" {
				t.Fatalf("unexpected eviction body: %#v", eviction)
			}
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	resp, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID: 7,
		Action:    "drain",
		Name:      "node-a",
	})
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if !patched || len(evictions) != 1 || !resp.Applied || !strings.Contains(resp.Message, "evicted 1 pod(s), deleted 0 pod(s), skipped 2 pod(s)") {
		t.Fatalf("unexpected drain patched=%v evictions=%v resp=%+v", patched, evictions, resp)
	}
	if resp.EvictedPodCount != 1 || resp.SkippedPodCount != 2 || len(resp.SkippedPods) != 2 {
		t.Fatalf("unexpected drain counts: %+v", resp)
	}
}

func TestExecuteK8sActionDrainSkipsUnsafePodsByDefault(t *testing.T) {
	var evictions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"20"}}`))
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"21"}}`))
		case r.URL.Path == "/api/v1/pods" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{
			  "apiVersion":"v1",
			  "kind":"PodList",
			  "items":[
			    {"metadata":{"namespace":"default","name":"api-1","uid":"pod-1","resourceVersion":"31","ownerReferences":[{"kind":"ReplicaSet","name":"api-rs","controller":true}]},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}},
			    {"metadata":{"namespace":"default","name":"bare","uid":"pod-2","resourceVersion":"32"},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}},
			    {"metadata":{"namespace":"default","name":"cache","uid":"pod-3","resourceVersion":"33","ownerReferences":[{"kind":"ReplicaSet","name":"cache-rs","controller":true}]},"spec":{"nodeName":"node-a","volumes":[{"emptyDir":{}}]},"status":{"phase":"Running"}}
			  ]
			}`))
		case r.URL.Path == "/api/v1/namespaces/default/pods/api-1/eviction" && r.Method == http.MethodPost:
			evictions = append(evictions, r.URL.Path)
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	resp, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID: 7,
		Action:    "drain",
		Name:      "node-a",
	})
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if len(evictions) != 1 || resp.EvictedPodCount != 1 || resp.SkippedPodCount != 2 || len(resp.SkippedPods) != 2 {
		t.Fatalf("unexpected drain result evictions=%v resp=%+v", evictions, resp)
	}
	reasons := []string{resp.SkippedPods[0].Reason, resp.SkippedPods[1].Reason}
	if !containsString(reasons, "unmanaged pod requires force=true") || !containsString(reasons, "emptyDir data requires delete_emptydir_data=true") {
		t.Fatalf("unexpected skipped reasons: %+v", resp.SkippedPods)
	}
}

func TestExecuteK8sActionDrainCanForceDeleteWithGracePeriod(t *testing.T) {
	grace := 15
	var deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"20"}}`))
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"21"}}`))
		case r.URL.Path == "/api/v1/pods" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{
			  "apiVersion":"v1",
			  "kind":"PodList",
			  "items":[
			    {"metadata":{"namespace":"default","name":"bare","uid":"pod-1","resourceVersion":"31"},"spec":{"nodeName":"node-a","volumes":[{"emptyDir":{}}]},"status":{"phase":"Running"}}
			  ]
			}`))
		case r.URL.Path == "/api/v1/namespaces/default/pods/bare" && r.Method == http.MethodDelete:
			deletes = append(deletes, r.URL.Path)
			var options map[string]any
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Fatalf("decode delete options: %v", err)
			}
			if options["gracePeriodSeconds"] != float64(grace) {
				t.Fatalf("gracePeriodSeconds=%#v want %d", options["gracePeriodSeconds"], grace)
			}
			preconditions := options["preconditions"].(map[string]any)
			if preconditions["uid"] != "pod-1" || preconditions["resourceVersion"] != "31" {
				t.Fatalf("unexpected preconditions: %#v", preconditions)
			}
			_, _ = w.Write([]byte(`{"kind":"Status","status":"Success"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	resp, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID:          7,
		Action:             "drain",
		Name:               "node-a",
		GracePeriodSeconds: &grace,
		Force:              true,
		DeleteEmptyDirData: true,
		DisableEviction:    true,
	})
	if err != nil {
		t.Fatalf("executeAction: %v", err)
	}
	if len(deletes) != 1 || resp.DeletedPodCount != 1 || resp.EvictedPodCount != 0 || resp.SkippedPodCount != 0 {
		t.Fatalf("unexpected forced drain deletes=%v resp=%+v", deletes, resp)
	}
}

func TestExecuteK8sActionDrainRejectsDaemonSetWhenNotIgnored(t *testing.T) {
	ignoreDaemonSets := false
	var patched bool
	var evictions int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"20"}}`))
		case r.URL.Path == "/api/v1/nodes/node-a" && r.Method == http.MethodPatch:
			patched = true
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Node","metadata":{"name":"node-a","uid":"node-uid","resourceVersion":"21"}}`))
		case r.URL.Path == "/api/v1/pods" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{
			  "apiVersion":"v1",
			  "kind":"PodList",
			  "items":[
			    {"metadata":{"namespace":"kube-system","name":"kube-proxy","uid":"pod-1","resourceVersion":"31","ownerReferences":[{"kind":"DaemonSet","name":"kube-proxy","controller":true}]},"spec":{"nodeName":"node-a"},"status":{"phase":"Running"}}
			  ]
			}`))
		case strings.HasSuffix(r.URL.Path, "/eviction"):
			evictions++
			t.Fatalf("unexpected eviction %s", r.URL.Path)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	api := &apiClient{baseURL: srv.URL, token: "test-token", http: srv.Client()}
	_, err := api.executeAction(context.Background(), tunnel.KubernetesActionRequest{
		ClusterID:        7,
		Action:           "drain",
		Name:             "node-a",
		IgnoreDaemonSets: &ignoreDaemonSets,
	})
	if err == nil || !strings.Contains(err.Error(), "daemonset pod requires ignore_daemonsets=true") {
		t.Fatalf("err=%v, want daemonset refusal", err)
	}
	if patched {
		t.Fatalf("node was cordoned before daemonset refusal")
	}
	if evictions != 0 {
		t.Fatalf("evictions=%d want 0", evictions)
	}
}

func intPtr(v int) *int { return &v }

func containsString(in []string, want string) bool {
	for _, got := range in {
		if got == want {
			return true
		}
	}
	return false
}
