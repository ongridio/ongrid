package autoapm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins/traces"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/prometheus/procfs"
)

func TestDockerBindingUsesLiveContainerIdentityBehindInit(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	resourceProc(t, root, 21, "0::/docker/"+id+"\n")
	resourceProc(t, root, 22, "0::/system.slice/docker-"+id+".scope\n")
	fs, err := procfs.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "autoapm-docker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	socket := filepath.Join(dir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var live atomic.Value
	live.Store(traces.DockerProcess{ID: id, PID: 21})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/app-blue/json" {
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
		}
		current := live.Load().(traces.DockerProcess)
		if _, err := fmt.Fprintf(w, `{"Id":%q,"State":{"Running":true,"Pid":%d}}`, current.ID, current.PID); err != nil {
			t.Error(err)
		}
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	t.Setenv("DOCKER_HOST", "unix://"+socket)
	// The init process may lack readable I/O; a verified application binding is sufficient.
	p := Plugin{bindings: resourceBindings([]tunnel.PromSample{{Name: "ongrid_apm_process_scrape_success", Value: 1, TsMs: time.Now().UnixMilli(), Labels: map[string]string{
		"service_name": "orders", "service_namespace": "shop", "deployment_environment_name": "test", "service_instance_id": "opaque-app",
		"container_name": "app-blue", "container_id": id, "process_pid": "22", "process_start_ticks": "1000", "ongrid_target_id": "selected-target",
	}}})}
	request := tunnel.ApplicationReceiverRequest{ServiceName: "orders", Namespace: "shop", Environment: "test", InstanceID: "opaque-app", ContainerName: "app-blue"}
	pid, target, _, err := p.receiverProcess(t.Context(), fs, request)
	if err != nil || pid != 22 || target != "selected-target" {
		t.Fatalf("application behind init not linked: pid=%d target=%q err=%v", pid, target, err)
	}
	live.Store(traces.DockerProcess{ID: strings.Repeat("b", 64), PID: 21})
	if _, _, _, err := p.receiverProcess(t.Context(), fs, request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced container accepted: %v", err)
	}
	live.Store(traces.DockerProcess{ID: id, PID: 21})
	if err := os.WriteFile(filepath.Join(root, "22/cgroup"), []byte("0::/docker/"+strings.Repeat("b", 64)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.receiverProcess(t.Context(), fs, request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("process outside the live container accepted: %v", err)
	}
}

func TestNativeBindingFollowsOpaqueInstanceAcrossRestarts(t *testing.T) {
	root := t.TempDir()
	fs, err := procfs.NewFS(root)
	if err != nil {
		t.Fatal(err)
	}
	target := contract.Target{Executable: "/usr/bin/python3", Port: 8080, ServiceName: "orders", ServiceNamespace: "shop"}
	spec := contract.Spec{Environment: "test", Targets: []contract.Target{target}}
	identities := []map[string]string{}
	p := Plugin{}
	for _, pid := range []int{21, 22, 23} {
		if pid > 21 {
			if err := os.RemoveAll(filepath.Join(root, strconv.Itoa(pid-1))); err != nil {
				t.Fatal(err)
			}
		}
		resourceProc(t, root, pid, "")
		id := "opaque-instance-" + strconv.Itoa(pid)
		if err := os.WriteFile(filepath.Join(root, strconv.Itoa(pid), "environ"), []byte("SECRET=never-return-this\x00OTEL_RESOURCE_ATTRIBUTES=service.instance.id="+id+"\x00"), 0600); err != nil {
			t.Fatal(err)
		}
		identities = append(identities, map[string]string{"service_name": "orders", "service_namespace": "shop", "deployment_environment_name": "test", "service_instance_id": id, "instance": "unrelated-exporter-locator"})
		candidates := []contract.Candidate{{Executable: target.Executable, Port: target.Port, PID: int32(pid)}}
		samples, err := collectProcessResources(t.Context(), fs, spec, identities, candidates, nil, nil)
		if err != nil || len(samples) != 8 {
			t.Fatalf("restart PID %d: %d samples, %v", pid, len(samples), err)
		}
		for _, sample := range samples {
			if sample.Labels["service_instance_id"] != id || sample.Labels["ongrid_target_id"] != target.ID() {
				t.Fatalf("wrong identity or environment leaked: %+v", sample)
			}
			for _, value := range sample.Labels {
				if strings.Contains(value, "never-return-this") {
					t.Fatal("application environment leaked into metrics")
				}
			}
		}
		p.bindings = resourceBindings(samples)
		request := tunnel.ApplicationReceiverRequest{ServiceName: "orders", Namespace: "shop", Environment: "test", InstanceID: id}
		got, gotTarget, _, err := p.receiverProcess(t.Context(), fs, request)
		if err != nil || got != int32(pid) || gotTarget != target.ID() {
			t.Fatalf("new process not linked: %d %q %v", got, gotTarget, err)
		}
		request.Environment = "another-environment"
		if _, _, _, err := p.receiverProcess(t.Context(), fs, request); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cross-environment binding accepted: %v", err)
		}
	}
	request := tunnel.ApplicationReceiverRequest{ServiceName: "orders", Namespace: "shop", Environment: "test", InstanceID: "opaque-instance-23"}
	statPath := filepath.Join(root, "23/stat")
	stat, err := os.ReadFile(statPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statPath, []byte(strings.Replace(string(stat), "1000", "2000", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.receiverProcess(t.Context(), fs, request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reused PID accepted: %v", err)
	}
	for key, bindings := range p.bindings {
		bindings[0].StartTicks = 2000
		bindings[0].ObservedAt = time.Now().Add(-time.Minute).UnixMilli()
		p.bindings[key] = bindings
	}
	if _, _, _, err := p.receiverProcess(t.Context(), fs, request); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired process binding accepted: %v", err)
	}
}
