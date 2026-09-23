package autoapm

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	contract "github.com/ongridio/ongrid/internal/pkg/autoapm"
	"gopkg.in/yaml.v3"
)

type fakeChild struct{ starts, stops atomic.Int32 }

func TestCapturePreflightFailureAndRecovery(t *testing.T) {
	denied := errors.New("perf_event_open(uprobe): permission denied")
	children := []*fakeChild{{}, {}, {}}
	p := &Plugin{selected: true, kubernetes: true, obi: children[0], collector: children[1], scraper: children[2], preflight: func(context.Context) error { return denied }}
	ctx := context.Background()
	if err := p.Start(ctx); !errors.Is(err, denied) {
		t.Fatalf("missing permission failure: %v", err)
	}
	if h := p.HealthSnapshot(); h.State != plugins.StateCrashed || h.LastError != denied.Error() {
		t.Fatalf("failed capture reported healthy: %+v", h)
	}
	for _, child := range children {
		if child.starts.Load() != 0 {
			t.Fatal("started capture before passing permission check")
		}
	}
	p.preflight = func(context.Context) error { return nil }
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if h := p.HealthSnapshot(); h.State != plugins.StateRunning || h.LastError != "" {
		t.Fatalf("capture did not recover after permissions repaired: %+v", h)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeChild) Name() string                         { return "fake" }
func (f *fakeChild) Configure(plugins.PluginConfig) error { return nil }
func (f *fakeChild) Start(context.Context) error          { f.starts.Add(1); return nil }
func (f *fakeChild) Stop(context.Context) error           { f.stops.Add(1); return nil }
func (f *fakeChild) HealthSnapshot() plugins.PluginHealth {
	return plugins.PluginHealth{State: plugins.StateRunning}
}

func TestDiscoveryOnlyAndOff(t *testing.T) {
	children := []*fakeChild{{}, {}, {}}
	discovered := make(chan struct{}, 2)
	p := &Plugin{obi: children[0], collector: children[1], scraper: children[2], discover: func(context.Context) ([]contract.Candidate, error) {
		discovered <- struct{}{}
		return []contract.Candidate{{Executable: "/opt/orders", Port: 8080}}, nil
	}}
	p.preflight = func(context.Context) error { t.Fatal("discovery requires no capture permissions"); return nil }
	if err := p.Configure(plugins.PluginConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-discovered:
	case <-time.After(time.Second):
		t.Fatal("discovery did not start")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		if c.starts.Load() != 0 {
			t.Fatal("empty targets started capture")
		}
	}
	if h := p.HealthSnapshot(); h.State != plugins.StateStopped || len(h.Candidates) > 0 {
		t.Fatalf("off retained live candidates: %+v", h)
	}
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-discovered:
	case <-time.After(time.Second):
		t.Fatal("discovery did not resume")
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestSelectedCaptureStopsAllChildren(t *testing.T) {
	children := []*fakeChild{{}, {}, {}}
	p := &Plugin{selected: true, obi: children[0], collector: children[1], scraper: children[2], discover: func(ctx context.Context) ([]contract.Candidate, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		if c.starts.Load() != 1 || c.stops.Load() != 1 {
			t.Fatal("capture lifecycle incomplete")
		}
	}
}
func TestRenderLiteralSelectionAndIndependentSampling(t *testing.T) {
	if _, err := render(plugins.PluginConfig{}); err == nil {
		t.Fatal("empty capture configuration accepted")
	}
	cfg := plugins.PluginConfig{Spec: map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/order.v1", "port": 8080, "service_name": "order"}}, "sample_ratio": 0.01}}
	body, err := render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err := yaml.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/obi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]interface{}
	if err := yaml.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, expected) {
		t.Fatalf("native-validation fixture differs from generated config:\n%s", body)
	}
	rule := v["discovery"].(map[string]interface{})["services"].([]interface{})[0].(map[string]interface{})
	if rule["exe_path"] != `^/opt/order\.v1$` || rule["open_ports"] != "8080" {
		t.Fatalf("selector broadened: %+v", rule)
	}
	if strings.Contains(string(body), "application_span") {
		t.Fatal("duplicate spanmetrics enabled")
	}
	t.Setenv("OTEL_EBPF_OPEN_PORT", "1-65535")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://unrelated")
	for _, entry := range obiEnvironment(cfg) {
		if strings.HasPrefix(entry, "OTEL_") {
			t.Fatal("inherited OTel config can override selection")
		}
	}
}

func TestCollectorReceivesDefaultAndServiceEnvironment(t *testing.T) {
	cfg := collectorConfig(plugins.PluginConfig{}, contract.Spec{Environment: "production", Targets: []contract.Target{{ServiceName: "orders", ServiceNamespace: "shop", Environment: "test"}, {ServiceName: "inventory"}}})
	if cfg.Spec["extra_attrs"].(map[string]interface{})["deployment.environment.name"] != "production" {
		t.Fatal("default missing")
	}
	rules := cfg.Spec["service_environments"].([]map[string]string)
	if len(rules) != 1 || rules[0]["environment"] != "test" || rules[0]["service_namespace"] != "shop" {
		t.Fatalf("overrides: %+v", rules)
	}
}

func TestKubernetesRulesUseMetadataAndSkipHostDiscovery(t *testing.T) {
	spec := contract.Spec{Environment: "production", ClusterID: 132, K8sClusterID: 50, Kubernetes: &contract.Kubernetes{Rules: []contract.KubernetesRule{
		{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "api.v1", Container: "app"},
		{Namespace: "future"},
	}}}
	body, err := render(plugins.PluginConfig{Spec: spec.Map()})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]interface{}
	if err := yaml.Unmarshal(body, &config); err != nil {
		t.Fatal(err)
	}
	rules := config["discovery"].(map[string]interface{})["services"].([]interface{})
	if config["ebpf"].(map[string]interface{})["bpf_fs_path"] != "/sys/fs/bpf/ongrid" {
		t.Fatal("Kubernetes OBI must use its owned bpffs directory")
	}
	first := rules[0].(map[string]interface{})
	if first["k8s_deployment_name"] != `^api\.v1$` || first["k8s_namespace"] != "^shop$" || first["k8s_container_name"] != nil || first["name"] != nil || first["exe_path"] != nil {
		t.Fatalf("invalid selector: %#v", first)
	}
	if len(rules[1].(map[string]interface{})) != 1 {
		t.Fatal("namespace policy must preserve service identities")
	}
	collector := collectorConfig(plugins.PluginConfig{}, spec)
	if collector.Spec["kubernetes_service_namespace"] != true || collector.Spec["extra_attrs"].(map[string]interface{})["deployment.environment.name"] != "production" {
		t.Fatal("missing inherited metadata")
	}
	p := &Plugin{kubernetes: true, discover: func(context.Context) ([]contract.Candidate, error) {
		t.Fatal("Kubernetes node scanned host processes")
		return nil, nil
	}}
	p.refresh(context.Background())
	if p.health.UpdatedAt.IsZero() {
		t.Fatal("missing heartbeat")
	}
}

func TestCollectorUsesMappedIdentityInsteadOfBootstrap(t *testing.T) {
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "50")
	spec := contract.Spec{ClusterID: 132, K8sClusterID: 50, Kubernetes: &contract.Kubernetes{Rules: []contract.KubernetesRule{{Namespace: "shop"}}}}
	attrs := collectorConfig(plugins.PluginConfig{}, spec).Spec["extra_attrs"].(map[string]interface{})
	if attrs["cluster_id"] != "132" || attrs["k8s_cluster_id"] != "50" {
		t.Fatalf("identity: %v", attrs)
	}
	spec.ClusterID, spec.K8sClusterID = 0, 0
	if _, err := render(plugins.PluginConfig{Spec: spec.Map()}); err != nil {
		t.Fatalf("old Manager config rejected: %v", err)
	}
	attrs = collectorConfig(plugins.PluginConfig{}, spec).Spec["extra_attrs"].(map[string]interface{})
	if attrs["cluster_id"] != "50" || attrs["k8s_cluster_id"] != nil {
		t.Fatalf("legacy ID was marked unified: %v", attrs)
	}
	attrs = collectorConfig(plugins.PluginConfig{}, contract.Spec{}).Spec["extra_attrs"].(map[string]interface{})
	if attrs["cluster_id"] != nil {
		t.Fatal("host inherited stale bootstrap environment")
	}
	t.Setenv("ONGRID_K8S_CLUSTER_ID", "")
	if _, err := render(plugins.PluginConfig{Spec: spec.Map()}); err == nil {
		t.Fatal("capture accepted missing cluster identity")
	}
}
