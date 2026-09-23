//go:build linux

package autoapm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"golang.org/x/sys/unix"
)

func TestDiscoveryListenerChild(t *testing.T) {
	port := os.Getenv("ONGRID_TEST_LISTEN_PORT")
	if port == "" {
		return
	}
	cfg := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var e error
		if err := c.Control(func(fd uintptr) { e = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1) }); err != nil {
			return err
		}
		return e
	}}
	var listener net.Listener
	var err error
	if port == "inherited" {
		file := os.NewFile(3, "listener")
		listener, err = net.FileListener(file)
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	} else {
		listener, err = cfg.Listen(context.Background(), "tcp4", "127.0.0.1:"+port)
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	_, err = io.Copy(io.Discard, os.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestDiscoverySameBinarySamePortWorkers(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherited=%t", shared), func(t *testing.T) {
			testListeningWorkers(t, shared)
		})
	}
}

func testListeningWorkers(t *testing.T, shared bool) {
	t.Helper()
	var file *os.File
	if shared {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		file, err = listener.(*net.TCPListener).File()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := file.Close(); err != nil {
				t.Error(err)
			}
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	start := func(port int) (int, int) {
		c := exec.Command(os.Args[0], "-test.run=^TestDiscoveryListenerChild$")
		value := strconv.Itoa(port)
		if shared {
			c.ExtraFiles = []*os.File{file}
			value = "inherited"
		}
		c.Env = append(os.Environ(), "ONGRID_TEST_LISTEN_PORT="+value)
		c.Stderr = os.Stderr
		in, err := c.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := in.Close(); err != nil {
				t.Error(err)
			}
			if err := c.Wait(); err != nil {
				t.Error(err)
			}
		})
		s := bufio.NewScanner(out)
		if !s.Scan() {
			t.Fatal("child did not listen")
		}
		actual, err := strconv.Atoi(s.Text())
		if err != nil {
			t.Fatal(err)
		}
		return actual, c.Process.Pid
	}
	port, first := start(0)
	_, second := start(port)
	candidates, err := discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := map[int32]bool{}
	for _, c := range candidates {
		if int(c.Port) == port && (int(c.PID) == first || int(c.PID) == second) {
			found[c.PID] = true
		}
	}
	if len(found) != 2 {
		t.Fatalf("two live processes (%d,%d) sharing port %d: expected both PIDs for resource collection, got %v", first, second, port, found)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec := contract.Spec{Environment: "test", Targets: []contract.Target{{Executable: exe, Port: uint16(port), ServiceName: "workers", ServiceNamespace: "shop"}}}
	identities := []tunnel.PromSample{}
	for _, pid := range []int{first, second} {
		identities = append(identities, tunnel.PromSample{Name: "target_info", Value: 1, Labels: map[string]string{
			"ongrid_instrumentation_source": "obi", "host_name": "host", "service_name": "workers", "service_namespace": "shop", "deployment_environment_name": "test", "service_instance_id": fmt.Sprintf("host:%d", pid),
		}})
	}
	p := Plugin{discover: discover}
	samples, err := p.collectResources(t.Context(), spec, identities)
	if err != nil || len(samples) != 16 {
		t.Fatalf("both workers must report resources: %d %v", len(samples), err)
	}
}

func TestDiscoveryDisplayLimitDoesNotDropResources(t *testing.T) {
	for i := 0; i <= contract.MaxCandidates; i++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := listener.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	candidates, err := discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) <= contract.MaxCandidates {
		t.Fatalf("inventory unexpectedly capped: %d", len(candidates))
	}
	var chosen contract.Candidate
	for _, candidate := range candidates[contract.MaxCandidates:] {
		if int(candidate.PID) == os.Getpid() {
			chosen = candidate
			break
		}
	}
	if chosen.PID == 0 {
		t.Fatal("missing process beyond discovery display limit")
	}
	p := Plugin{discover: discover}
	p.refresh(t.Context())
	if len(p.health.Candidates) != contract.MaxCandidates || p.health.DiscoveryError == "" {
		t.Fatalf("heartbeat should remain bounded with a warning: %+v", p.health)
	}
	spec := contract.Spec{Environment: "test", Targets: []contract.Target{{Executable: chosen.Executable, Port: chosen.Port, ServiceName: "orders", ServiceNamespace: "shop"}}}
	labels := map[string]string{"ongrid_instrumentation_source": "obi", "host_name": "host", "service_name": "orders", "service_namespace": "shop", "deployment_environment_name": "test", "service_instance_id": fmt.Sprintf("host:%d", os.Getpid())}
	samples, err := p.collectResources(t.Context(), spec, []tunnel.PromSample{{Name: "target_info", Value: 1, Labels: labels}})
	if err != nil || len(samples) != 8 {
		t.Fatalf("selected process beyond display limit lost resources: %d %v", len(samples), err)
	}
}
