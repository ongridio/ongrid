package traces

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

type bridgeConflictPlugin struct {
	configs     []plugins.PluginConfig
	starts      []context.Context
	stops       int
	readyErrors []error
	state       plugins.PluginState
}

func (p *bridgeConflictPlugin) Name() string { return Name }
func (p *bridgeConflictPlugin) HealthSnapshot() plugins.PluginHealth {
	return plugins.PluginHealth{State: p.state}
}

func (p *bridgeConflictPlugin) Configure(cfg plugins.PluginConfig) error {
	p.configs = append(p.configs, cfg)
	return nil
}
func (p *bridgeConflictPlugin) Start(ctx context.Context) error {
	p.starts = append(p.starts, ctx)
	p.state = plugins.StateRunning
	return nil
}
func (p *bridgeConflictPlugin) Stop(context.Context) error {
	p.stops++
	p.state = plugins.StateStopped
	return nil
}
func (p *bridgeConflictPlugin) WaitReady(context.Context) error {
	if len(p.readyErrors) == 0 {
		return nil
	}
	err := p.readyErrors[0]
	p.readyErrors = p.readyErrors[1:]
	return err
}

func TestBridgeConflictPreservesActiveProtocolsAndRetriesReleasedPort(t *testing.T) {
	base := &bridgeTestFetcher{configs: map[string]plugins.PluginConfig{Name: {Enabled: true}}}
	child := &bridgeConflictPlugin{}
	f := WithDockerBridges(base, child, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.discover = func(context.Context) ([]string, error) { return []string{"172.17.0.1", "fd00::1"}, nil }
	busy := "172.17.0.1:4318"
	var probed []string
	f.probe = func(_ context.Context, endpoint string) error {
		probed = append(probed, endpoint)
		if endpoint == busy {
			return errors.New("address already in use")
		}
		return nil
	}
	snapshot, err := f.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot[Name].Spec[dockerBridgeBlockedKey], []string{busy}) {
		t.Fatalf("blocked = %v", snapshot[Name].Spec)
	}
	if err := f.Configure(snapshot[Name]); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.WaitReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	probed = nil
	if _, err := f.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(probed, []string{busy}) {
		t.Fatalf("reprobed active listeners: %v", probed)
	}
	busy = ""
	recovered, err := f.Fetch(t.Context())
	if err != nil || recovered[Name].Spec[dockerBridgeBlockedKey] != nil {
		t.Fatalf("released port not retried: %v, %v", recovered, err)
	}
	if base.configs[Name].Spec != nil {
		t.Fatal("manager snapshot mutated")
	}
	child.state = plugins.StateCrashed
	probed = nil
	if _, err := f.Fetch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(probed) != 4 {
		t.Fatalf("stopped Collector kept stale port ownership: %v", probed)
	}
}

func TestBridgeStartupRaceRestoresLoopbackAndForcesRediscovery(t *testing.T) {
	base := &bridgeTestFetcher{configs: map[string]plugins.PluginConfig{Name: {Enabled: true, Spec: map[string]interface{}{"grpc_endpoint": "127.0.0.1:14317"}}}}
	child := &bridgeConflictPlugin{readyErrors: []error{errors.New("bind failed after probe"), nil}}
	f := WithDockerBridges(base, child, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.discover = func(context.Context) ([]string, error) { return []string{"172.17.0.1"}, nil }
	f.probe = func(context.Context, string) error { return nil }
	snapshot, err := f.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Configure(snapshot[Name]); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := f.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	readyCtx, readyCancel := context.WithCancel(t.Context())
	if err := f.WaitReady(readyCtx); err != nil {
		t.Fatal(err)
	}
	readyCancel()
	if child.stops != 1 || len(child.configs) != 2 || len(child.starts) != 2 {
		t.Fatalf("fallback lifecycle: stops=%d configs=%d starts=%d", child.stops, len(child.configs), len(child.starts))
	}
	fallback := child.configs[1]
	if fallback.Spec[dockerBridgeAddressesKey] != nil || fallback.Spec["grpc_endpoint"] != "127.0.0.1:14317" {
		t.Fatalf("unsafe fallback: %v", fallback.Spec)
	}
	if child.starts[1] != runCtx || child.starts[1].Err() != nil {
		t.Fatal("fallback inherited readiness context")
	}
	if len(f.active) != 0 {
		t.Fatalf("failed receivers marked active: %v", f.active)
	}
	next, err := f.Fetch(t.Context())
	if err != nil || next[Name].Spec[dockerBridgeRevisionKey] == snapshot[Name].Spec[dockerBridgeRevisionKey] {
		t.Fatalf("recovered bridge would not be reapplied: %v, %v", next, err)
	}
	if len(dockerBridgeEndpoints(next[Name])) != 1 {
		t.Fatalf("explicit protocol override lost: %v", next)
	}
}

func TestBridgeReadinessFailureWithoutAutomaticReceiversIsPreserved(t *testing.T) {
	want := errors.New("collector failed")
	child := &bridgeConflictPlugin{readyErrors: []error{want}}
	f := WithDockerBridges(nil, child, nil)
	if err := f.Configure(plugins.PluginConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := f.WaitReady(t.Context()); !errors.Is(err, want) {
		t.Fatalf("readiness error = %v", err)
	}
	if child.stops != 0 {
		t.Fatal("unrelated Collector was restarted")
	}
}

func TestBridgeFallbackFailureIsReported(t *testing.T) {
	startupErr, fallbackErr := errors.New("bind failed"), errors.New("fallback failed")
	child := &bridgeConflictPlugin{readyErrors: []error{startupErr, fallbackErr}}
	f := WithDockerBridges(nil, child, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := f.Configure(plugins.PluginConfig{Spec: map[string]interface{}{dockerBridgeAddressesKey: []string{"172.17.0.1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.WaitReady(t.Context()); !errors.Is(err, startupErr) || !errors.Is(err, fallbackErr) {
		t.Fatalf("fallback error = %v", err)
	}
}

func TestProbeDockerBridgePort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	endpoint := listener.Addr().String()
	if err := probeDockerBridgePort(t.Context(), endpoint); err == nil {
		t.Fatal("occupied port accepted")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := probeDockerBridgePort(t.Context(), endpoint); err != nil {
		t.Fatalf("released port rejected: %v", err)
	}
}

func TestRenderSkipsOnlyConflictedBridgeProtocols(t *testing.T) {
	raw, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example.com/v1/traces", Spec: map[string]interface{}{
		dockerBridgeAddressesKey: []string{"172.17.0.1", "fd00::1"},
		dockerBridgeBlockedKey:   []string{"172.17.0.1:4318", "[fd00::1]:4317", "[fd00::1]:4318"},
		"enable_logs":            true, "logs_endpoint": "https://manager.example.com/loki/api/v1/push",
		"enable_metrics": true, "metrics_export_endpoint": "127.0.0.1:9464",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Receivers map[string]struct {
			Protocols map[string]struct{ Endpoint string }
		}
		Service struct {
			Pipelines map[string]struct{ Receivers []string }
		}
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Receivers) != 2 || len(cfg.Receivers["otlp"].Protocols) != 2 || len(cfg.Receivers["otlp/docker_0"].Protocols) != 1 || cfg.Receivers["otlp/docker_0"].Protocols["grpc"].Endpoint != "172.17.0.1:4317" {
		t.Fatalf("receivers = %+v", cfg.Receivers)
	}
	for signal, pipeline := range cfg.Service.Pipelines {
		if !reflect.DeepEqual(pipeline.Receivers, []string{"otlp", "otlp/docker_0"}) {
			t.Fatalf("%s receivers = %v", signal, pipeline.Receivers)
		}
	}
}

// Run only in an isolated Linux container: this test starts the real OTLP ports.
func TestDockerBridgeRuntimeWithCollector(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if runtime.GOOS != "linux" || binary == "" || os.Getenv("ONGRID_TEST_BRIDGE_RUNTIME") != "1" {
		t.Skip("requires an isolated Linux container and ONGRID_TEST_BRIDGE_RUNTIME=1")
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var address string
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
			address = ip.String()
			break
		}
	}
	if address == "" {
		t.Fatal("isolated container needs a non-loopback IPv4 address")
	}
	for _, race := range []bool{false, true} {
		name := "occupied before fetch"
		if race {
			name = "occupied after probe"
		}
		t.Run(name, func(t *testing.T) {
			base := &bridgeTestFetcher{configs: map[string]plugins.PluginConfig{Name: {
				Enabled: true, EdgeID: 42, Endpoint: "http://127.0.0.1:1/v1/traces",
				Spec: map[string]interface{}{"enable_metrics": true, "metrics_export_endpoint": "127.0.0.1:9464", "enable_logs": true, "logs_endpoint": "http://127.0.0.1:1/loki/api/v1/push"},
			}}}
			f := WithDockerBridges(base, New(filepath.Dir(binary), t.TempDir(), nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
			f.discover = func(context.Context) ([]string, error) { return nil, nil }
			runCtx, cancel := context.WithCancel(t.Context())
			t.Cleanup(func() {
				cancel()
				ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				if err := f.Stop(ctx); err != nil {
					t.Error(err)
				}
			})
			apply := func(cfg plugins.PluginConfig, restart bool) {
				t.Helper()
				if err := f.Configure(cfg); err != nil {
					t.Fatal(err)
				}
				ctx, done := context.WithTimeout(runCtx, 10*time.Second)
				defer done()
				if restart {
					if err := f.Stop(ctx); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.Start(runCtx); err != nil {
					t.Fatal(err)
				}
				if err := f.WaitReady(ctx); err != nil {
					t.Fatal(err)
				}
			}
			checkSignals := func(host string) {
				t.Helper()
				client := &http.Client{Timeout: 2 * time.Second}
				defer client.CloseIdleConnections()
				for _, signal := range []string{"traces", "metrics", "logs"} {
					resp, err := client.Post("http://"+net.JoinHostPort(host, "4318")+"/v1/"+signal, "application/json", strings.NewReader("{}"))
					if err != nil {
						t.Fatalf("%s %s: %v", host, signal, err)
					}
					_, readErr := io.Copy(io.Discard, resp.Body)
					closeErr := resp.Body.Close()
					if resp.StatusCode != http.StatusOK || readErr != nil || closeErr != nil {
						t.Fatalf("%s %s: status=%d, read=%v, close=%v", host, signal, resp.StatusCode, readErr, closeErr)
					}
				}
			}
			if race {
				apply(base.configs[Name], false)
				checkSignals("127.0.0.1")
			}
			f.discover = func(context.Context) ([]string, error) { return []string{address}, nil }
			endpoint := net.JoinHostPort(address, "4318")
			var occupied net.Listener
			occupy := func() {
				var err error
				occupied, err = net.Listen("tcp", endpoint)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = occupied.Close() })
			}
			if race {
				f.probe = func(ctx context.Context, target string) error {
					err := probeDockerBridgePort(ctx, target)
					if err == nil && target == endpoint {
						occupy()
					}
					return err
				}
			} else {
				occupy()
			}
			snapshot, err := f.Fetch(runCtx)
			if err != nil {
				t.Fatal(err)
			}
			apply(snapshot[Name], race)
			checkSignals("127.0.0.1")
			if race && f.revision == 0 {
				t.Fatal("startup race did not exercise fallback")
			}
			if !race {
				next, err := f.Fetch(runCtx)
				if err != nil || !reflect.DeepEqual(next, snapshot) {
					t.Fatalf("active listeners caused config churn: %v, %v", next, err)
				}
			}
			if err := occupied.Close(); err != nil {
				t.Fatal(err)
			}
			f.probe = probeDockerBridgePort
			next, err := f.Fetch(runCtx)
			if err != nil || reflect.DeepEqual(next, snapshot) {
				t.Fatalf("released port would not reconcile: %v, %v", next, err)
			}
			apply(next[Name], true)
			checkSignals("127.0.0.1")
			checkSignals(address)
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(address, "4317"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
