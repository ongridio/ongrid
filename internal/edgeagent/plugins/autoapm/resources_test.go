package autoapm

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/prometheus/procfs"
)

func resourceProc(t *testing.T, root string, pid int, cgroup string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0700); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(strings.Repeat("0 ", 50))
	for field, value := range map[int]string{3: "S", 14: "300", 15: "100", 20: "5", 22: "1000", 23: "1048576", 24: "2"} {
		fields[field-3] = value
	}
	for name, body := range map[string]string{
		"stat":   fmt.Sprintf("%d (python worker) %s\n", pid, strings.Join(fields, " ")),
		"io":     "rchar: 0\nwchar: 0\nsyscr: 0\nsyscw: 0\nread_bytes: 100\nwrite_bytes: 200\ncancelled_write_bytes: 0\n",
		"cgroup": cgroup,
		"fd/0":   "",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/usr/bin/python3", filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
}

func TestResourcesMatchProcessNotInterpreterOrPort(t *testing.T) {
	root := t.TempDir()
	resourceProc(t, root, 21, "")
	resourceProc(t, root, 22, "")
	fs, err := procfs.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"service_name": "orders", "service_namespace": "shop", "service_instance_id": "host:21", "host_name": "host", "deployment_environment_name": "test", "instance": "host:21"}
	spec := contract.Spec{Environment: "test", Targets: []contract.Target{
		{Executable: "/usr/bin/python3", Port: 8080, ServiceName: "orders", ServiceNamespace: "shop"},
		{Executable: "/usr/bin/python3", Port: 9090, ServiceName: "orders", ServiceNamespace: "shop"},
	}}
	candidates := []contract.Candidate{{Executable: "/usr/bin/python3", PID: 21, Port: 8080}, {Executable: "/usr/bin/python3", PID: 21, Port: 9090}, {Executable: "/usr/bin/python3", PID: 22, Port: 8180}}
	got, err := collectProcessResources(t.Context(), fs, spec, []map[string]string{identity, identity}, candidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 8 {
		t.Fatalf("multi-port process counted more than once: %d", len(got))
	}
	for _, s := range got {
		if s.Labels["process_pid"] != "21" || s.Labels["deployment_environment_name"] != "test" || s.Labels["service_instance_id"] != "host:21" {
			t.Fatalf("identity leak: %+v", s)
		}
		if s.Name == "ongrid_apm_process_cpu_seconds_total" && s.Value != 4 {
			t.Fatalf("CPU must be user + system seconds: %v", s.Value)
		}
	}
	// Missing I/O is not reported as zero and does not hide readable CPU/RSS.
	if err := os.Remove(filepath.Join(root, "21/io")); err != nil {
		t.Fatal(err)
	}
	partial, err := collectProcessResources(t.Context(), fs, spec, []map[string]string{identity}, candidates, nil)
	if err == nil || len(partial) != 6 || partial[len(partial)-1].Value != 0 {
		t.Fatalf("partial error hidden: %v %v", partial, err)
	}
	wrong := maps.Clone(identity)
	wrong["service_instance_id"] = "host:22"
	got, err = collectProcessResources(t.Context(), fs, spec, []map[string]string{wrong}, candidates, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("another Python service was included: %v %v", got, err)
	}
	got, err = collectProcessResources(t.Context(), fs, contract.Spec{}, []map[string]string{identity}, candidates, nil)
	if err != nil || len(got) != 0 {
		t.Fatal("empty selection collected resources")
	}
}

func TestResourcesResolveOnlySelectedKubernetesContainer(t *testing.T) {
	root := t.TempDir()
	resourceProc(t, root, 21, "0::/kubepods/pod-uid/cri-containerd-abc.scope\n")
	resourceProc(t, root, 22, "0::/kubepods/pod-uid/abc\n")
	resourceProc(t, root, 23, "0::/kubepods/pod-uid/sidecar\n")
	fs, err := procfs.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	identity := map[string]string{"service_name": "orders", "service_namespace": "shop", "service_instance_id": "shop.pod.app", "k8s_pod_uid": "uid", "k8s_namespace_name": "shop", "k8s_container_name": "app", "k8s_deployment_name": "orders"}
	spec := contract.Spec{Kubernetes: &contract.Kubernetes{Rules: []contract.KubernetesRule{{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "orders"}}}}
	got, err := collectProcessResources(t.Context(), fs, spec, []map[string]string{identity}, nil, map[string]string{"uid/app": "abc"})
	if err != nil || len(got) != 16 {
		t.Fatalf("container processes: %d %v", len(got), err)
	}
	for _, s := range got {
		if s.Labels["process_pid"] == "23" {
			t.Fatal("unselected sidecar included")
		}
	}
	got, err = collectProcessResources(t.Context(), fs, spec, []map[string]string{identity}, nil, map[string]string{"uid/app": "replacement"})
	if err != nil || len(got) != 0 {
		t.Fatal("old container associated with replacement")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectProcessResources(ctx, fs, spec, []map[string]string{identity}, nil, map[string]string{"uid/app": "abc"}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestResourceIdentitiesUseOBIResourceMetadata(t *testing.T) {
	labels := map[string]string{"ongrid_instrumentation_source": "obi", "instance": "host:21", "job": "shop/orders", "cluster_id": "48", "host_name": "host", "http_route": "/private"}
	got := resourceIdentities([]tunnel.PromSample{{Name: "target_info", Value: 1, Labels: labels}, {Name: "target_info", Value: 1, Labels: labels}, {Name: "http_server_request_duration_seconds_count", Value: 1, Labels: labels}})
	if len(got) != 1 || got[0]["service_name"] != "orders" || got[0]["service_namespace"] != "shop" || got[0]["service_instance_id"] != "host:21" || got[0]["cluster_id"] != "48" || got[0]["http_route"] != "" {
		t.Fatalf("wrong resource metadata: %v", got)
	}
}

func TestResourceIdentitiesUseKubernetesNamespace(t *testing.T) {
	labels := map[string]string{
		"ongrid_instrumentation_source": "obi", "service_name": "obi-mock-java",
		"service_namespace": "ongrid-obi-mock", "k8s_namespace_name": "obi-mock",
		"service_instance_id": "obi-mock.pod.java", "k8s_pod_uid": "pod-uid",
	}
	got := resourceIdentities([]tunnel.PromSample{{Name: "target_info", Value: 1, Labels: labels}})
	if len(got) != 1 || got[0]["service_namespace"] != "obi-mock" {
		t.Fatalf("Kubernetes resource identity must use the Pod namespace: %v", got)
	}
}
