package edge

import (
	"context"
	"errors"
	"reflect"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestKubernetesLogsUseServiceSelectionAcrossAllConfigViews(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	// An empty seeded row follows service discovery until explicitly configured.
	repo.rows["logs"] = &model.PluginConfig{EdgeID: 1, PluginName: "logs", Enabled: true, SpecJSON: `{}`}
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

func TestKubernetesNodeLogsHonorSavedConfiguration(t *testing.T) {
	ctx := context.Background()
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	uc.SetKubernetesAutoAPMProvider(func(_ context.Context, id uint64) (*autoapm.Spec, bool, error) {
		if id == 1 {
			return &autoapm.Spec{Kubernetes: &autoapm.Kubernetes{}}, true, nil
		}
		return nil, true, nil
	}, nil)
	var paths []string
	uc.SetKubernetesLogPathsProvider(func(_ context.Context, id uint64) ([]string, bool, error) {
		return paths, true, nil
	})
	for _, mode := range []string{"host", "kubernetes"} {
		for _, enabled := range []bool{true, false, true} {
			input := SetInput{Enabled: enabled, Spec: map[string]interface{}{
				"mode": mode, "enable_journald": false,
				"journald_units": []interface{}{"nginx.service"},
				"file_paths":     []interface{}{"/var/log/**/*.log"},
				"pod_log_paths":  []interface{}{"/var/log/pods/default_*/*/*.log"},
				"sources": []interface{}{map[string]interface{}{
					"id": "nginx", "include": []interface{}{"/var/log/nginx/*.log"}, "parser": "json",
				}},
			}}
			saved, err := uc.Set(ctx, 1, "logs", input)
			if err != nil {
				t.Fatalf("save node logs: %v", err)
			}
			if saved.Enabled != enabled || !reflect.DeepEqual(saved.Spec, input.Spec) {
				t.Fatalf("save changed configuration: %+v", saved)
			}
			// A service-discovery refresh must not replace an explicit node config.
			for _, selection := range [][]string{nil, {"/var/log/pods/shop_*_*/*/*.log"}, nil} {
				paths = selection
				wire, err := uc.FetchForEdge(ctx, 1, false)
				if err != nil {
					t.Fatal(err)
				}
				cfg := wire.Configs["logs"]
				if cfg.Enabled != enabled || !reflect.DeepEqual(cfg.Spec, input.Spec) {
					t.Fatalf("runtime changed configuration: %+v", cfg)
				}
				rows, err := uc.ListForUI(ctx, 1)
				if err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row.PluginName == "logs" && (row.Enabled != enabled || !reflect.DeepEqual(row.Spec, input.Spec)) {
						t.Fatalf("UI changed configuration: %+v", row)
					}
				}
				active, err := uc.IsEnabled(ctx, 1, "logs")
				if err != nil || active != enabled {
					t.Fatalf("ingestion enabled=%v err=%v", active, err)
				}
			}
		}
	}
	uc.SetKubernetesLogPathsProvider(func(context.Context, uint64) ([]string, bool, error) {
		return nil, true, errors.New("Pod inventory unavailable")
	})
	if _, err := uc.FetchForEdge(ctx, 1, false); err != nil {
		t.Fatalf("explicit logs still depend on service-discovery paths: %v", err)
	}
	if _, err := uc.Set(ctx, 1, "logs", SetInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if enabled, err := uc.IsEnabled(ctx, 1, "logs"); err != nil || enabled {
		t.Fatalf("empty disabled config: %v %v", enabled, err)
	}
	if _, err := uc.Set(ctx, 2, "logs", SetInput{Enabled: true}); err == nil {
		t.Fatal("controller accepted host collection")
	}
}
