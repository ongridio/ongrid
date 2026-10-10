package autoapm

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"github.com/prometheus/procfs"
)

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
