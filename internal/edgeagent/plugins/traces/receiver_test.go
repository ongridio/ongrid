package traces

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestApplicationReceiverUsesActualProcessNetworkAndCollectorPipeline(t *testing.T) {
	for _, container := range []bool{false, true} {
		t.Run(map[bool]string{false: "host", true: "container"}[container], func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"self/ns", "123/ns", "123/net"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			host := filepath.Join(root, "self/ns/net")
			process := filepath.Join(root, "123/ns/net")
			if err := os.WriteFile(host, []byte("network"), 0600); err != nil {
				t.Fatal(err)
			}
			if container {
				if err := os.WriteFile(process, []byte("other network"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Link(host, process); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "123/net/route"), []byte("Iface Destination Gateway Flags RefCnt Use Metric Mask\neth0 00000000 010017AC 0003 0 0 0 00000000\n"), 0600); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(root, "otelcol.yaml")
			body := `receivers:
  otlp:
    protocols:
      http:
        endpoint: "127.0.0.1:14318"
  otlp/docker_0:
    protocols:
      http:
        endpoint: "172.23.0.1:18418"
service:
  pipelines:
    traces:
      receivers: [otlp, otlp/docker_0]
    metrics:
      receivers: [otlp]
exporters:
  otlphttp/manager:
    headers:
      Authorization: private-credential
`
			if err := os.WriteFile(config, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := receiverForProcess(config, root, 123)
			expected := "http://127.0.0.1:14318"
			if container {
				expected = "http://172.23.0.1:18418"
			}
			if err != nil || out.Endpoint != expected || out.Metrics == container {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if strings.Contains(out.Endpoint, "private-credential") {
				t.Fatal("credential leaked")
			}
			if container {
				if err := os.WriteFile(config, []byte(strings.ReplaceAll(body, "172.23.0.1", "172.24.0.1")), 0600); err != nil {
					t.Fatal(err)
				}
				out, err = receiverForProcess(config, root, 123)
				if err != nil || out.Endpoint != "" || out.Reason != "receiver_unavailable" {
					t.Fatalf("wrong bridge used: %+v %v", out, err)
				}
			}
			out, err = receiverForProcess(config, root, 999)
			if err != nil || out.Reason != "process_unavailable" {
				t.Fatalf("exited process: %+v %v", out, err)
			}
		})
	}
}

func TestDockerContainerProcessResolvesTheCurrentPIDAfterRestart(t *testing.T) {
	dir, err := os.MkdirTemp("", "traces-container-")
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
	var pid atomic.Int32
	pid.Store(123)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/app-blue/json" {
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
		}
		current := pid.Load()
		if current < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id := strings.Repeat("a", 64)
		if current == 999 {
			id = "invalid"
		}
		if _, err := fmt.Fprintf(w, `{"Id":%q,"State":{"Running":%t,"Pid":%d},"Config":{"Env":["SECRET=not-returned"]}}`, id, current > 0, current); err != nil {
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
	for _, expected := range []int32{123, 456, 0} {
		pid.Store(expected)
		actual, err := DockerContainerProcess(t.Context(), "app-blue")
		if err != nil || actual.PID != expected || expected > 0 && actual.ID != strings.Repeat("a", 64) {
			t.Fatalf("current container=%+v want PID=%d err=%v", actual, expected, err)
		}
	}
	pid.Store(999)
	if _, err := DockerContainerProcess(t.Context(), "app-blue"); err == nil {
		t.Fatal("accepted invalid runtime container ID")
	}
	pid.Store(-1)
	if _, err := DockerContainerProcess(t.Context(), "app-blue"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing container: %v", err)
	}
	for _, name := range []string{"", "../app-blue", "app?query=x", strings.Repeat("a", 256)} {
		if _, err := DockerContainerProcess(t.Context(), name); err == nil {
			t.Fatalf("accepted invalid container name %q", name)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := DockerContainerProcess(ctx, "app-blue"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Docker request: %v", err)
	}
}
func TestDefaultGatewayRejectsInvalidOrInactiveRoutes(t *testing.T) {
	for _, route := range []string{"", "eth0 00000000 010017AC 0000 0 0 0 00000000", "eth0 00000000 invalid 0003 0 0 0 00000000"} {
		if got := defaultGateway(route); got != "" {
			t.Fatalf("invalid gateway: %s", got)
		}
	}
}
