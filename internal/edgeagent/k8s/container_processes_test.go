package k8s

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNodeContainerIDsRestrictsNodeAndSkipsTerminatedContainers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fieldSelector") != "spec.nodeName=node-1" {
			t.Errorf("missing node selector: %s", r.URL)
		}
		fmt.Fprint(w, `{"items":[{"metadata":{"uid":"pod-uid"},"status":{"containerStatuses":[{"name":"app","containerID":"containerd://abc","state":{"running":{}}},{"name":"old","containerID":"containerd://def","state":{"terminated":{}}}],"initContainerStatuses":[{"name":"sidecar","containerID":"cri-o://xyz","state":{"running":{}}}]}}]}`)
	}))
	defer server.Close()
	c := &apiClient{baseURL: server.URL, http: server.Client()}
	got, err := c.nodeContainerIDs(t.Context(), "node-1")
	if err != nil || !reflect.DeepEqual(got, map[string]string{"pod-uid/app": "abc", "pod-uid/sidecar": "xyz"}) {
		t.Fatalf("identities: %v %v", got, err)
	}
}
