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
	row, err = uc.Set(ctx, 1, "autoapm", SetInput{Enabled: true})
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
