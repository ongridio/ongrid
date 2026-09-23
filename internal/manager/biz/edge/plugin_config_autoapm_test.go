package edge

import (
	"bytes"
	"context"
	"encoding/json"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"testing"
)

func TestDiscoveryAlwaysOnForNewAndLegacyDisabledEdges(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)
	for _, configured := range []bool{false, true} {
		if configured {
			repo.rows["autoapm"] = &model.PluginConfig{EdgeID: 1, PluginName: "autoapm", Enabled: false, SpecJSON: `{"targets":[{"executable":"/opt/orders","port":8080,"service_name":"orders"}]}`}
		}
		wire, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		cfg := wire.Configs["autoapm"]
		if !cfg.Enabled || cfg.Endpoint != wire.Configs["traces"].Endpoint {
			t.Fatalf("wire: %+v", cfg)
		}
		spec, err := autoapm.Parse(cfg.Spec)
		if err != nil || spec.Selected() != configured {
			t.Fatalf("selection: %+v %v", spec, err)
		}
		enabled, err := uc.IsEnabled(ctx, 1, "autoapm")
		if err != nil || !enabled {
			t.Fatalf("gate: %v %v", enabled, err)
		}
		rows, err := uc.ListForUI(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.PluginName == "autoapm" && !row.Enabled {
				t.Fatal("UI disabled discovery")
			}
		}
	}
	row, err := uc.Set(ctx, 1, "autoapm", SetInput{Enabled: false})
	if err != nil || !row.Enabled || len(row.Spec["targets"].([]interface{})) != 1 {
		t.Fatalf("legacy write lost selection: %+v %v", row, err)
	}
	row, err = uc.Set(ctx, 1, "autoapm", SetInput{Spec: autoapm.Spec{}.Map()})
	if err != nil || !row.Enabled {
		t.Fatalf("empty selection stopped discovery: %+v %v", row, err)
	}
}

func TestAutoAPMEnvironmentInheritanceAndSavedOptions(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	environment := "production"
	uc.SetAutoAPMEnvironmentProvider(func(context.Context, uint64) (string, string, error) { return environment, "cluster-a", nil })
	spec := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/orders", "port": 8080, "service_name": "orders", "service_namespace": "commerce", "environment": "test"}}}
	if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"production", "staging", ""} {
		environment = env
		snapshot, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		cfg := snapshot.Configs["autoapm"]
		got, _ := cfg.Spec["environment"].(string)
		if got != env || cfg.Spec["targets"].([]interface{})[0].(map[string]interface{})["environment"] != "test" {
			t.Fatalf("resolved: %+v", cfg.Spec)
		}
		rows, err := uc.ListForUI(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.PluginName == "autoapm" && (row.Spec["environment"] != nil || row.Defaults.Environment != env || row.Defaults.ClusterName != "cluster-a") {
				t.Fatalf("defaults must stay out of saved spec: %+v", row)
			}
		}
	}
	environment = "production"
	spec["environment"] = "development"
	if _, err := uc.Set(ctx, 2, "autoapm", SetInput{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := uc.FetchForEdge(ctx, 2, false)
	if err != nil || snapshot.Configs["autoapm"].Spec["environment"] != "production" {
		t.Fatalf("device default did not replace legacy collector value: %+v %v", snapshot, err)
	}
	options, err := uc.AutoAPMOptions(ctx)
	if err != nil || len(options.Environments) != 2 || options.Environments[0] != "development" || options.Environments[1] != "test" || len(options.Namespaces) != 1 || options.Namespaces[0] != "commerce" {
		t.Fatalf("saved options: %+v %v", options, err)
	}
	uc.SetKubernetesAutoAPMProvider(nil, func(context.Context) ([]string, error) {
		return []string{`{"environment":"production","kubernetes":{"rules":[{"namespace":"kube-system"},{"namespace":"commerce"}]}}`}, nil
	})
	options, err = uc.AutoAPMOptions(ctx)
	if err != nil || len(options.Namespaces) != 1 || options.Namespaces[0] != "commerce" {
		t.Fatalf("host namespaces must exclude Kubernetes-only values and retain shared names: %+v %v", options, err)
	}
	if len(options.Environments) != 3 || options.Environments[1] != "production" {
		t.Fatalf("cluster environments should remain reusable: %+v", options)
	}
}

func TestOldEdgeDiscoveryFiltersSystemdWithoutChangingStoredHeartbeat(t *testing.T) {
	uc := &Usecase{}
	items := []PluginHealth{{Name: "autoapm", Candidates: []autoapm.Candidate{{Executable: "/usr/lib/systemd/systemd", PID: 1, Port: 22}, {Executable: "/opt/orders", PID: 10, Port: 8080}}}}
	uc.RecordPluginHealth(1, items)
	health := uc.PluginHealth(1)
	if len(health[0].Candidates) != 1 || health[0].Candidates[0].Executable != "/opt/orders" {
		t.Fatalf("candidates: %+v", health)
	}
	if len(items[0].Candidates) != 2 {
		t.Fatal("mutated stored candidates")
	}
}

func TestClusterCaptureReplacesNodeTargetsAndDisablesController(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	uc.SetKubernetesAutoAPMProvider(func(_ context.Context, id uint64) (*autoapm.Spec, bool, error) {
		if id == 1 {
			return &autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop"}}}}, true, nil
		}
		if id == 2 {
			return nil, true, nil
		}
		return nil, false, nil
	}, nil)
	{
		snapshot, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		if !snapshot.Configs["autoapm"].Enabled || snapshot.Configs["autoapm"].Spec["kubernetes"] == nil {
			t.Fatal("cluster policy lost")
		}
		snapshot, err = uc.FetchForEdge(ctx, 2, false)
		if err != nil || snapshot.Configs["autoapm"].Enabled {
			t.Fatal("controller was enabled")
		}
	}
	if _, err := uc.Set(ctx, 1, "autoapm", SetInput{}); err == nil {
		t.Fatal("node-local capture settings accepted")
	}
	if _, err := uc.Set(ctx, 3, "autoapm", SetInput{Spec: autoapm.Spec{Kubernetes: &autoapm.Kubernetes{}}.Map()}); err == nil {
		t.Fatal("host accepted Kubernetes settings")
	}
}

func TestCaptureUsesFixedSamplingAndTLSForHostsAndKubernetes(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	repo.rows["autoapm"] = &model.PluginConfig{EdgeID: 1, PluginName: "autoapm", Enabled: true, SpecJSON: `{"sample_ratio":0.1,"tls_insecure_skip_verify":false,"targets":[{"executable":"/opt/orders","port":8080,"service_name":"orders"}]}`}
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)
	for _, kubernetes := range []bool{false, true} {
		if kubernetes {
			uc.SetKubernetesAutoAPMProvider(func(context.Context, uint64) (*autoapm.Spec, bool, error) {
				ratio := 0.25
				return &autoapm.Spec{SampleRatio: &ratio, Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop"}}}}, true, nil
			}, nil)
		}
		snapshot, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		spec := snapshot.Configs["autoapm"].Spec
		if spec["sample_ratio"] != float64(1) || spec["tls_insecure_skip_verify"] != true {
			t.Fatalf("kubernetes=%v: unexpected capture defaults: %+v", kubernetes, spec)
		}
		parsed, err := autoapm.Parse(spec)
		if err != nil || !parsed.Selected() || (parsed.Kubernetes != nil) != kubernetes {
			t.Fatalf("capture scope changed: %+v %v", parsed, err)
		}
	}
	if repo.rows["autoapm"].SpecJSON != `{"sample_ratio":0.1,"tls_insecure_skip_verify":false,"targets":[{"executable":"/opt/orders","port":8080,"service_name":"orders"}]}` {
		t.Fatal("runtime defaults mutated stored configuration")
	}
}

func TestKubernetesEnvironmentIgnoresDeviceOverride(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	uc.SetAutoAPMEnvironmentProvider(func(context.Context, uint64) (string, string, error) { return "device-override", "host-cluster", nil })
	for _, env := range []string{"production", ""} {
		uc.SetKubernetesAutoAPMProvider(func(context.Context, uint64) (*autoapm.Spec, bool, error) {
			return &autoapm.Spec{Environment: env, Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop"}}}}, true, nil
		}, nil)
		snapshot, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		actual, _ := snapshot.Configs["autoapm"].Spec["environment"].(string)
		if actual != env {
			t.Fatalf("Kubernetes environment = %q, want %q", actual, env)
		}
	}
}

func TestKubernetesTelemetryIdentityIsManagerOwned(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	repo.rows["traces"] = &model.PluginConfig{PluginName: "traces", Enabled: true, SpecJSON: `{"extra_attrs":{"cluster_id":"50","team":"payments"}}`}
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)
	uc.SetKubernetesTelemetryProvider(func(_ context.Context, id uint64) (uint64, uint64, error) {
		if id == 3 {
			return 0, 0, nil
		}
		return 132, 50, nil
	})
	uc.SetKubernetesAutoAPMProvider(func(_ context.Context, id uint64) (*autoapm.Spec, bool, error) {
		if id == 1 {
			return &autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop"}}}}, true, nil
		}
		return nil, id == 2, nil
	}, nil)
	for _, id := range []uint64{1, 2} {
		repo.rows["traces"].EdgeID = id
		snapshot, err := uc.FetchForEdge(ctx, id, true)
		if err != nil {
			t.Fatal(err)
		}
		extra := snapshot.Configs["traces"].Spec["extra_attrs"].(map[string]interface{})
		if extra["cluster_id"] != "132" || extra["k8s_cluster_id"] != "50" || extra["team"] != "payments" || snapshot.Configs["logs"].Spec["cluster_id"] != "132" {
			t.Fatalf("inconsistent identity: %+v", snapshot)
		}
		spec, err := autoapm.Parse(snapshot.Configs["autoapm"].Spec)
		if err != nil {
			t.Fatal(err)
		}
		if id == 1 && (spec.ClusterID != 132 || spec.K8sClusterID != 50) {
			t.Fatalf("OBI identity: %+v", spec)
		}
		if id == 2 && snapshot.Configs["autoapm"].Enabled {
			t.Fatal("controller capture enabled")
		}
	}
	host, err := uc.FetchForEdge(ctx, 3, false)
	if err != nil || host.Configs["autoapm"].Spec["cluster_id"] != nil {
		t.Fatalf("host identity changed: %v %v", host, err)
	}
	if repo.rows["traces"].SpecJSON != `{"extra_attrs":{"cluster_id":"50","team":"payments"}}` {
		t.Fatal("mutated stored settings")
	}
	// Freeze the pre-unification strict decoder: new fields would reject the
	// whole plugin config, including a cold start after upgrading only Manager.
	for _, unified := range []bool{false, true, false} {
		snapshot, err := uc.FetchForEdge(ctx, 1, unified)
		if err != nil {
			t.Fatal(err)
		}
		var legacy struct {
			Kubernetes            *autoapm.Kubernetes `json:"kubernetes,omitempty"`
			TLSInsecureSkipVerify bool                `json:"tls_insecure_skip_verify,omitempty"`
			Environment           string              `json:"environment,omitempty"`
			SampleRatio           *float64            `json:"sample_ratio,omitempty"`
			Targets               []autoapm.Target    `json:"targets,omitempty"`
		}
		raw, err := json.Marshal(snapshot.Configs["autoapm"].Spec)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&legacy)
		if (err == nil) == unified || (!unified && (legacy.Kubernetes == nil || len(legacy.Kubernetes.Rules) != 1 || legacy.Kubernetes.Rules[0].Namespace != "shop")) {
			t.Fatalf("unified=%v: old decoder=%v config=%s", unified, err, raw)
		}
		extra := snapshot.Configs["traces"].Spec["extra_attrs"].(map[string]interface{})
		if !unified && (extra["cluster_id"] != "50" || extra["k8s_cluster_id"] != nil) {
			t.Fatalf("legacy trace identity changed: %v", extra)
		}
		if snapshot.Configs["logs"].Spec["cluster_id"] != "132" {
			t.Fatal("container logs lost their existing unified identity")
		}
	}
}
