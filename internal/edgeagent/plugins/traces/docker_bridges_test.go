package traces

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"gopkg.in/yaml.v3"
)

func TestDockerBridgeAddresses(t *testing.T) {
	networks := []dockerNetwork{
		{Name: "bridge", Driver: "bridge"},
		{ID: "abcdef1234567890", Name: "app", Driver: "bridge"},
		{Name: "custom", Driver: "bridge", Options: map[string]string{"com.docker.network.bridge.name": "app-net"}},
		{Name: "overlay", Driver: "overlay"},
		{Name: "host", Driver: "host"},
	}
	interfaces := map[string][]net.Addr{
		"docker0":         {cidr(t, "172.17.0.1/16"), cidr(t, "fe80::1/64")},
		"br-abcdef123456": {cidr(t, "172.18.0.1/16"), cidr(t, "fd00::1/64")},
		"app-net":         {cidr(t, "172.18.0.1/16"), cidr(t, "192.168.20.1/24"), cidr(t, "0.0.0.0/0"), cidr(t, "127.0.0.1/8"), cidr(t, "224.0.0.1/24")},
	}
	got, err := dockerBridgeAddresses(t.Context(), networks, func(name string) ([]net.Addr, error) {
		addresses, ok := interfaces[name]
		if !ok {
			t.Fatalf("unexpected interface %q", name)
		}
		return addresses, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.17.0.1", "172.18.0.1", "192.168.20.1", "fd00::1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	// Network listing order must not restart an unchanged Collector.
	networks[0], networks[2] = networks[2], networks[0]
	got, err = dockerBridgeAddresses(t.Context(), networks, func(name string) ([]net.Addr, error) { return interfaces[name], nil })
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reordered addresses = %v, %v", got, err)
	}
}

func TestDockerBridgeDiscoveryRejectsInvalidData(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network dockerNetwork
		addrs   []net.Addr
		err     error
	}{
		{name: "missing identity", network: dockerNetwork{Driver: "bridge"}},
		{name: "path traversal", network: dockerNetwork{Driver: "bridge", Options: map[string]string{"com.docker.network.bridge.name": "../eth0"}}},
		{name: "address query error", network: dockerNetwork{Name: "bridge", Driver: "bridge"}, err: errors.New("address query failed")},
		{name: "malformed address", network: dockerNetwork{Name: "bridge", Driver: "bridge"}, addrs: []net.Addr{&net.IPAddr{IP: net.ParseIP("172.17.0.1")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dockerBridgeAddresses(t.Context(), []dockerNetwork{tc.network}, func(string) ([]net.Addr, error) { return tc.addrs, tc.err })
			if err == nil {
				t.Fatal("expected discovery error")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := dockerBridgeAddresses(ctx, []dockerNetwork{{Driver: "bridge"}}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery: %v", err)
	}
	if addrs, err := localBridgeAddresses("ongrid-no-bridge"); err != nil || len(addrs) != 0 {
		t.Fatalf("missing bridge = %v, %v", addrs, err)
	}
}

func TestListDockerNetworksUsesLocalSocket(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		ok     bool
	}{
		{"networks", 200, `[{"Id":"abcdef123456","Name":"app","Driver":"bridge","Options":{"com.docker.network.bridge.name":"app-net"}}]`, true},
		{"daemon error", 500, `daemon error`, false},
		{"invalid JSON", 200, `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the Unix socket path below the platform's length limit.
			dir, err := os.MkdirTemp("", "traces-docker-")
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
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/networks" {
					t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tc.status)
				if _, err := io.WriteString(w, tc.body); err != nil {
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
			networks, err := listDockerNetworks(t.Context(), socket)
			if !tc.ok {
				if err == nil {
					t.Fatal("expected request error")
				}
				return
			}
			if err != nil || len(networks) != 1 || networks[0].ID != "abcdef123456" || networks[0].Options["com.docker.network.bridge.name"] != "app-net" {
				t.Fatalf("networks = %+v, %v", networks, err)
			}
		})
	}
}

type bridgeTestFetcher struct {
	configs  map[string]plugins.PluginConfig
	err      error
	reported string
}

func (f *bridgeTestFetcher) Fetch(context.Context) (map[string]plugins.PluginConfig, error) {
	return f.configs, f.err
}
func (f *bridgeTestFetcher) ReportPluginConfigApplied(_ context.Context, name string, _ plugins.PluginConfig, _ error) error {
	f.reported = name
	return f.err
}

func TestDockerBridgeFetcherRefreshesWithoutMutatingSnapshot(t *testing.T) {
	spec := map[string]interface{}{dockerBridgeAddressesKey: []string{"203.0.113.1"}, "extra_attrs": map[string]string{"env": "test"}}
	base := &bridgeTestFetcher{configs: map[string]plugins.PluginConfig{Name: {Enabled: true, Spec: spec}}}
	addresses := []string{"172.17.0.1"}
	var discoveryErr error
	fetcher := &dockerBridgeRuntime{ConfigFetcher: base, log: slog.New(slog.NewTextHandler(io.Discard, nil)), discover: func(context.Context) ([]string, error) { return addresses, discoveryErr }, probe: func(context.Context, string) error { return nil }}
	first, err := fetcher.Fetch(t.Context())
	if err != nil || !reflect.DeepEqual(first[Name].Spec[dockerBridgeAddressesKey], addresses) {
		t.Fatalf("first Fetch: %v, %v", first, err)
	}
	addresses = []string{"172.18.0.1"}
	second, err := fetcher.Fetch(t.Context())
	if err != nil || !reflect.DeepEqual(second[Name].Spec[dockerBridgeAddressesKey], addresses) {
		t.Fatalf("second Fetch: %v, %v", second, err)
	}
	if !reflect.DeepEqual(first[Name].Spec[dockerBridgeAddressesKey], []string{"172.17.0.1"}) || !reflect.DeepEqual(spec[dockerBridgeAddressesKey], []string{"203.0.113.1"}) {
		t.Fatal("cached snapshot was mutated")
	}
	discoveryErr = errors.New("Docker unavailable")
	got, err := fetcher.Fetch(t.Context())
	if err != nil || got[Name].Spec[dockerBridgeAddressesKey] != nil {
		t.Fatalf("failed discovery must fall back to localhost: %v, %v", got, err)
	}
	if err := fetcher.ReportPluginConfigApplied(t.Context(), "logs", plugins.PluginConfig{}, nil); err != nil || base.reported != "logs" {
		t.Fatalf("apply acknowledgement was not forwarded: %q, %v", base.reported, err)
	}
	base.err = errors.New("fetch failed")
	if _, err := fetcher.Fetch(t.Context()); !errors.Is(err, base.err) {
		t.Fatalf("base error not preserved: %v", err)
	}
}

func TestDockerBridgeFetcherSkipsDisabledAndExplicitReceivers(t *testing.T) {
	for _, cfg := range []plugins.PluginConfig{
		{Enabled: false},
		{Enabled: true, Spec: map[string]interface{}{"grpc_endpoint": "127.0.0.1:14317", "http_endpoint": "127.0.0.1:14318"}},
	} {
		base := &bridgeTestFetcher{configs: map[string]plugins.PluginConfig{Name: cfg}}
		fetcher := &dockerBridgeRuntime{ConfigFetcher: base, discover: func(context.Context) ([]string, error) { t.Fatal("unexpected discovery"); return nil, nil }}
		if _, err := fetcher.Fetch(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDockerBridgeDiscoveryUsesOnlyLocalSocket(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://remote-docker.example:2375")
	if _, err := discoverDockerBridgeAddresses(t.Context()); err == nil {
		t.Fatal("remote Docker endpoint was accepted")
	}
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "missing.sock"))
	if addresses, err := discoverDockerBridgeAddresses(t.Context()); err != nil || len(addresses) != 0 {
		t.Fatalf("Docker-free host: %v, %v", addresses, err)
	}
}

func TestRenderedDockerBridgeConfigAcceptedByCollector(t *testing.T) {
	binary := os.Getenv("ONGRID_TEST_OTELCOL_BINARY")
	if binary == "" {
		t.Skip("ONGRID_TEST_OTELCOL_BINARY is not set")
	}
	raw, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example.com/v1/traces", Spec: map[string]interface{}{
		dockerBridgeAddressesKey: []string{"172.17.0.1", "172.18.0.1", "fd00::1"},
		"enable_metrics":         true, "metrics_export_endpoint": "127.0.0.1:9464",
		"enable_logs": true, "logs_endpoint": "https://manager.example.com/loki/api/v1/push",
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "otelcol.yaml")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, binary, "validate", "--config="+path).CombinedOutput(); err != nil {
		t.Fatalf("Collector rejected bridge listeners: %v\n%s", err, output)
	}
}

func TestRenderDockerBridgeReceivers(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec map[string]interface{}
		grpc bool
		http bool
	}{
		{"defaults", nil, true, true},
		{"explicit grpc", map[string]interface{}{"grpc_endpoint": "127.0.0.1:14317"}, false, true},
		{"explicit http", map[string]interface{}{"http_endpoint": "0.0.0.0:4318"}, true, false},
		{"gateway", map[string]interface{}{"grpc_endpoint": "0.0.0.0:4317", "http_endpoint": "0.0.0.0:4318"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.spec == nil {
				tc.spec = make(map[string]interface{})
			}
			tc.spec[dockerBridgeAddressesKey] = []string{"172.17.0.1", "fd00::1"}
			tc.spec["enable_logs"] = true
			tc.spec["logs_endpoint"] = "https://manager.example.com/loki/api/v1/push"
			tc.spec["enable_metrics"] = true
			tc.spec["metrics_export_endpoint"] = "127.0.0.1:9464"
			raw, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example.com/v1/traces", Spec: tc.spec})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Receivers map[string]struct {
					Protocols map[string]struct{ Endpoint string }
				}
				Service struct {
					Pipelines map[string]struct{ Receivers []string }
				}
			}
			if err := yaml.Unmarshal(raw, &config); err != nil {
				t.Fatal(err)
			}
			wantNames := []string{"otlp"}
			if tc.grpc || tc.http {
				wantNames = append(wantNames, "otlp/docker_0", "otlp/docker_1")
				for i, host := range []string{"172.17.0.1", "fd00::1"} {
					protocols := config.Receivers[wantNames[i+1]].Protocols
					for protocol, enabled := range map[string]bool{"grpc": tc.grpc, "http": tc.http} {
						p, present := protocols[protocol]
						if present != enabled {
							t.Fatalf("%s enabled=%v, want %v", protocol, present, enabled)
						}
						if enabled {
							port := "4317"
							if protocol == "http" {
								port = "4318"
							}
							if p.Endpoint != net.JoinHostPort(host, port) {
								t.Fatalf("endpoint = %q", p.Endpoint)
							}
						}
					}
				}
			}
			if len(config.Receivers) != len(wantNames) {
				t.Fatalf("receivers = %+v", config.Receivers)
			}
			for signal, pipeline := range config.Service.Pipelines {
				if !reflect.DeepEqual(pipeline.Receivers, wantNames) {
					t.Fatalf("%s receivers = %v, want %v", signal, pipeline.Receivers, wantNames)
				}
			}
			if tc.name == "defaults" && (!strings.Contains(string(raw), "127.0.0.1:4317") || !strings.Contains(string(raw), "127.0.0.1:4318") || strings.Contains(string(raw), "0.0.0.0")) {
				t.Fatalf("default host listeners changed:\n%s", raw)
			}
		})
	}
	for _, address := range []string{"0.0.0.0", "::", "127.0.0.1", "fe80::1", "not-an-ip"} {
		_, err := render(plugins.PluginConfig{EdgeID: 42, Endpoint: "https://manager.example.com/v1/traces", Spec: map[string]interface{}{dockerBridgeAddressesKey: []string{address}}})
		if err == nil {
			t.Fatalf("accepted invalid bridge address %q", address)
		}
	}
}

func cidr(t *testing.T, value string) net.Addr {
	t.Helper()
	ip, network, err := net.ParseCIDR(value)
	if err != nil {
		t.Fatal(err)
	}
	network.IP = ip
	return network
}
