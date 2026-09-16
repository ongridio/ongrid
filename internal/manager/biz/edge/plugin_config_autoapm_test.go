package edge

import (
	"context"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"testing"
)

func TestAutoAPMOffPreservesTargetsAndDefaultsOff(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	enabled, err := uc.IsEnabled(ctx, 1, "autoapm")
	if err != nil || enabled {
		t.Fatalf("default enabled: %v %v", enabled, err)
	}
	globalEnabled := false
	uc.SetAutoAPMEnabledProvider(func(context.Context) bool { return globalEnabled })
	spec := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/orders", "port": 8080, "service_name": "orders"}}}
	if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Enabled: true, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	row, err := uc.Set(ctx, 1, "autoapm", SetInput{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if row.Enabled || len(row.Spec["targets"].([]interface{})) != 1 {
		t.Fatalf("off lost targets: %+v", row)
	}
	globalEnabled = true
	row, err = uc.Set(ctx, 1, "autoapm", SetInput{Enabled: false})
	if err != nil || !row.Enabled || len(row.Spec["targets"].([]interface{})) != 1 {
		t.Fatalf("resume failed: %+v %v", row, err)
	}
	snapshot, err := uc.FetchForEdge(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Configs["autoapm"].Endpoint != snapshot.Configs["traces"].Endpoint {
		t.Fatal("auto APM must use existing trace data plane")
	}
	if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Enabled: true, Spec: map[string]interface{}{"targets": []interface{}{map[string]interface{}{}}}}); err == nil {
		t.Fatal("unbounded selector accepted")
	}
}

func TestAutoAPMGlobalGateAppliesToEveryEdgeAndPreservesSelection(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	enabled := true
	uc.SetAutoAPMEnabledProvider(func(context.Context) bool { return enabled })
	spec := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/orders", "port": 8080, "service_name": "orders"}}}
	if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Enabled: false, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	// Edge 2 has no persisted row; the global gate still enables discovery.
	for _, want := range []bool{true, false, true} {
		enabled = want
		for _, id := range []uint64{1, 2} {
			snapshot, err := uc.FetchForEdge(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			cfg := snapshot.Configs["autoapm"]
			if cfg.Enabled != want {
				t.Fatalf("edge %d enabled=%v want %v", id, cfg.Enabled, want)
			}
			if id == 1 && len(cfg.Spec["targets"].([]interface{})) != 1 {
				t.Fatal("lost selected target")
			}
			if id == 2 && len(cfg.Spec) != 0 {
				t.Fatal("fresh edge must only discover")
			}
			if !snapshot.Configs["traces"].Enabled {
				t.Fatal("global APM switch changed SDK ingestion")
			}
			actual, err := uc.IsEnabled(ctx, id, "autoapm")
			if err != nil || actual != want {
				t.Fatalf("ingestion gate: %v %v", actual, err)
			}
			rows, err := uc.ListForUI(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.PluginName == "autoapm" && row.Enabled != want {
					t.Fatal("UI gate mismatch")
				}
			}
		}
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
		snapshot, err := uc.FetchForEdge(ctx, 1)
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
	snapshot, err := uc.FetchForEdge(ctx, 2)
	if err != nil || snapshot.Configs["autoapm"].Spec["environment"] != "development" {
		t.Fatalf("legacy environment changed: %+v %v", snapshot, err)
	}
	options, err := uc.AutoAPMOptions(ctx)
	if err != nil || len(options.Environments) != 2 || options.Environments[0] != "development" || options.Environments[1] != "test" || len(options.Namespaces) != 1 || options.Namespaces[0] != "commerce" {
		t.Fatalf("saved options: %+v %v", options, err)
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

func TestClusterCaptureReplacesNodeTargetsAndHonorsGlobalOff(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	enabled := true
	uc.SetAutoAPMEnabledProvider(func(context.Context) bool { return enabled })
	uc.SetKubernetesAutoAPMProvider(func(_ context.Context, id uint64) (*autoapm.Spec, bool, error) {
		if id == 1 {
			return &autoapm.Spec{Kubernetes: &autoapm.Kubernetes{Rules: []autoapm.KubernetesRule{{Namespace: "shop"}}}}, true, nil
		}
		if id == 2 {
			return nil, true, nil
		}
		return nil, false, nil
	}, nil)
	for _, on := range []bool{true, false} {
		enabled = on
		snapshot, err := uc.FetchForEdge(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Configs["autoapm"].Enabled != on || snapshot.Configs["autoapm"].Spec["kubernetes"] == nil {
			t.Fatal("cluster policy or global gate lost")
		}
		snapshot, err = uc.FetchForEdge(ctx, 2)
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
