package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIClientClusterUIDUsesKubeSystemNamespace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/kube-system" {
			http.NotFound(w, r)
			return
		}
		writeTestResponse(t, w, `{"metadata":{"name":"kube-system","uid":"physical-cluster-uid"}}`)
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "token", http: srv.Client()}
	uid, err := client.clusterUID(context.Background())
	if err != nil {
		t.Fatalf("clusterUID() error = %v", err)
	}
	if uid != "physical-cluster-uid" {
		t.Fatalf("clusterUID() = %q, want physical-cluster-uid", uid)
	}
}

func TestAPIClientClusterUIDRejectsEmptyUID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestResponse(t, w, `{"metadata":{"name":"kube-system"}}`)
	}))
	defer srv.Close()

	client := &apiClient{baseURL: srv.URL, token: "token", http: srv.Client()}
	if _, err := client.clusterUID(context.Background()); err == nil {
		t.Fatal("clusterUID() error = nil, want empty UID rejection")
	}
}
