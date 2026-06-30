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

	// Enable gpumetrics — should auto-create metrics config with default
	// host/proc URLs plus the GPU exporter.
	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	metricsRow := repo.rows[model.PluginNameMetrics]
	if metricsRow == nil {
		t.Fatal("metrics config not created after enabling gpumetrics")
	}
	var spec map[string]interface{}
	if err := json.Unmarshal([]byte(metricsRow.SpecJSON), &spec); err != nil {
		t.Fatalf("unmarshal metrics spec: %v", err)
	}
	urls, ok := spec["target_urls"].([]interface{})
	if !ok || len(urls) != 3 {
		t.Fatalf("target_urls=%v, want 3 entries (host + proc + gpu)", spec["target_urls"])
	}
	want := []string{
		"http://127.0.0.1:9102/metrics",
		"http://127.0.0.1:9256/metrics",
		"http://127.0.0.1:9835/metrics",
	}
	for i, u := range want {
		if urls[i].(string) != u {
			t.Errorf("target_urls[%d]=%v, want %v", i, urls[i], u)
		}
	}
}

func TestSetGPUMetrics_IdempotentSync(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

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
	if len(urls) != 3 {
		t.Errorf("target_urls length=%d, want 3 (idempotent)", len(urls))
	}
}

func TestSetGPUMetrics_AppendsToExistingTargetURLs(t *testing.T) {
	repo := newFakePluginConfigRepo()
	repo.rows[model.PluginNameMetrics] = &model.PluginConfig{
		EdgeID:     1,
		PluginName: model.PluginNameMetrics,
		Enabled:    true,
		SpecJSON:   `{"target_urls":["http://127.0.0.1:9102/metrics"]}`,
	}
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	var spec map[string]interface{}
	_ = json.Unmarshal([]byte(repo.rows[model.PluginNameMetrics].SpecJSON), &spec)
	urls := spec["target_urls"].([]interface{})
	if len(urls) != 2 {
		t.Fatalf("target_urls length=%d, want 2 (existing + gpu)", len(urls))
	}
	if urls[0].(string) != "http://127.0.0.1:9102/metrics" {
		t.Errorf("target_urls[0]=%v, want host URL preserved", urls[0])
	}
	if urls[1].(string) != "http://127.0.0.1:9835/metrics" {
		t.Errorf("target_urls[1]=%v, want gpu URL appended", urls[1])
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
	if len(urls) != 3 {
		t.Fatalf("target_urls length=%d, want 3", len(urls))
	}
	want := "http://127.0.0.1:9999/metrics"
	if urls[2].(string) != want {
		t.Errorf("target_urls[2]=%v, want %v", urls[2], want)
	}
}

func TestSetGPUMetrics_DisableRemovesTargetURL(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: true,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() enable error = %v", err)
	}

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
	urls, ok := spec["target_urls"].([]interface{})
	if !ok || len(urls) != 2 {
		t.Fatalf("target_urls=%v, want host+proc defaults after gpu disable", spec["target_urls"])
	}
	if urls[0].(string) != "http://127.0.0.1:9102/metrics" || urls[1].(string) != "http://127.0.0.1:9256/metrics" {
		t.Errorf("target_urls=%v, want default host/proc URLs", urls)
	}
}

func TestSetGPUMetrics_RemoveNonexistentURL(t *testing.T) {
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)

	_, err := uc.Set(context.Background(), 1, model.PluginNameGPUMetrics, SetInput{
		Enabled: false,
		Spec:    map[string]interface{}{},
	})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	if repo.rows[model.PluginNameMetrics] != nil {
		t.Error("metrics config was created unexpectedly")
	}
}
