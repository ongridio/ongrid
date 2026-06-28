package edge

import (
	"context"
	"encoding/json"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestSetGPUMetrics_SyncsTargetURL(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	// Enable gpumetrics — should auto-create metrics config with GPU target URL.
	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Verify metrics config was created with GPU target URL.
	metricsRow := repo.rows[model.PluginNameMetrics]
	if metricsRow == nil {
		t.Fatal("metrics config not created after enabling gpumetrics")
	}
	var spec map[string]interface{}
	if err := json.Unmarshal([]byte(metricsRow.SpecJSON), &spec); err != nil {
		t.Fatalf("unmarshal metrics spec: %v", err)
	}
	urls, ok := spec["target_urls"].([]interface{})
	if !ok || len(urls) == 0 {
		t.Fatalf("target_urls=%v, want GPU exporter URL", spec["target_urls"])
	}
	want := "http://127.0.0.1:9835/metrics"
	if urls[0].(string) != want {
		t.Errorf("target_urls[0]=%v, want %v", urls[0], want)
	}
}

func TestSetGPUMetrics_IdempotentSync(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	// Enable gpumetrics twice — should not duplicate the URL.
	for i := 0; i < 2; i++ {
		_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
			Enabled: true,
			Spec:    map[string]interface{}{},
		})
		if err != nil {
			t.Fatalf("Set() #%d error = %v", i, err)
		}
	}

	metricsRow := repo.rows[model.PluginNameMetrics]
	var spec map[string]interface{}
	_ = json.Unmarshal([]byte(metricsRow.SpecJSON), &spec)
	urls := spec["target_urls"].([]interface{})
	if len(urls) != 1 {
		t.Errorf("target_urls length=%d, want 1 (idempotent)", len(urls))
	}
}

func TestSetGPUMetrics_CustomListenAddress(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{"listen_address": ":9999"},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	metricsRow := repo.rows[model.PluginNameMetrics]
	var spec map[string]interface{}
	_ = json.Unmarshal([]byte(metricsRow.SpecJSON), &spec)
	urls := spec["target_urls"].([]interface{})
	want := "http://127.0.0.1:9999/metrics"
	if urls[0].(string) != want {
		t.Errorf("target_urls[0]=%v, want %v", urls[0], want)
	}
}

func TestSetGPUMetrics_DisableRemovesTargetURL(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	// Enable first.
	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() enable error = %v", err)
	}

	// Disable — should remove the GPU target URL from metrics.
	_, err = uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: false,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() disable error = %v", err)
	}

	metricsRow := repo.rows[model.PluginNameMetrics]
	if metricsRow == nil {
		t.Fatal("metrics config should still exist after disabling gpumetrics")
	}
	var spec map[string]interface{}
	_ = json.Unmarshal([]byte(metricsRow.SpecJSON), &spec)
	if urls, ok := spec["target_urls"]; ok {
		arr, _ := urls.([]interface{})
		if len(arr) > 0 {
			t.Errorf("target_urls=%v, want empty after disable", urls)
		}
	}
}

func TestSetGPUMetrics_RemoveNonexistentURL(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	// Disable without ever enabling — should be a no-op.
	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: false,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Metrics config should not have been created.
	if repo.rows[model.PluginNameMetrics] != nil {
		t.Error("metrics config was created unexpectedly")
	}
}
