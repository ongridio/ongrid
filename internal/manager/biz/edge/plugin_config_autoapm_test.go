package edge

import (
	"context"
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
