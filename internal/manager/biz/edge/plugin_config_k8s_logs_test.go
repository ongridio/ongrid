package edge

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestKubernetesLogsUseServiceSelectionAcrossAllConfigViews(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	// A legacy full-node setting must never widen the cluster selection.
	repo.rows["logs"] = &model.PluginConfig{EdgeID: 1, PluginName: "logs", Enabled: true, SpecJSON: `{"mode":"host","enable_journald":false,"pod_log_path":"/var/log/pods/*/*/*.log"}`}
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)
	var paths []string
	uc.SetKubernetesAutoAPMProvider(func(_ context.Context, id uint64) (*autoapm.Spec, bool, error) {
		if id == 1 {
			return &autoapm.Spec{Kubernetes: &autoapm.Kubernetes{}}, true, nil
		}
		return nil, id == 3, nil
	}, nil)
	uc.SetKubernetesLogPathsProvider(func(_ context.Context, id uint64) ([]string, bool, error) {
		if id == 3 {
			return nil, true, nil
		}
		return paths, id == 1, nil
	})
	for _, step := range []struct {
		paths []string
		want  bool
	}{
		{nil, true},
		{[]string{"/var/log/pods/shop_*_*/*/*.log"}, true},
		{nil, true},
	} {
		paths = step.paths
		wire, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		cfg := wire.Configs["logs"]
		if cfg.Spec["enable_journald"] != true || cfg.Enabled != step.want || cfg.Spec["mode"] != "kubernetes" || cfg.Spec["pod_log_path"] == "/var/log/pods/*/*/*.log" {
			t.Fatalf("wire: %+v", cfg)
		}
		rows, err := uc.ListForUI(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.PluginName == "logs" && row.Enabled != step.want {
				t.Fatalf("UI: %+v", row)
			}
		}
		enabled, err := uc.IsEnabled(ctx, 1, "logs")
		if err != nil || enabled != step.want {
			t.Fatalf("ingestion: %v %v", enabled, err)
		}
		if _, err := uc.Set(ctx, 1, "logs", SetInput{Enabled: true}); err == nil {
			t.Fatal("node override accepted")
		}
		controller, err := uc.FetchForEdge(ctx, 3, false)
		if err != nil || controller.Configs["logs"].Enabled {
			t.Fatalf("controller started node logs: %+v %v", controller, err)
		}
		if !wire.Configs["traces"].Enabled {
			t.Fatal("SDK ingestion changed")
		}
		host, err := uc.FetchForEdge(ctx, 2, false)
		if err != nil || !host.Configs["logs"].Enabled || host.Configs["logs"].Spec["pod_log_paths"] != nil {
			t.Fatal("ordinary host logging changed")
		}
	}
}
