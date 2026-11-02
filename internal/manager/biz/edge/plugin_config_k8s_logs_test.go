package edge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func (r *fakePluginConfigRepo) UpdateNodeLogSpec(ctx context.Context, edgeID, _ uint64, patch func(string) (string, error)) (*model.PluginConfig, error) {
	row, err := r.Get(ctx, edgeID, model.PluginNameLogs)
	if errors.Is(err, errs.ErrNotFound) {
		row = &model.PluginConfig{EdgeID: edgeID, PluginName: model.PluginNameLogs, Enabled: true}
	} else if err != nil {
		return nil, err
	}
	row.SpecJSON, err = patch(row.SpecJSON)
	if err != nil {
		return nil, err
	}
	return r.Upsert(ctx, row)
}

func TestKubernetesLogsUseServiceSelectionAcrossAllConfigViews(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	// A legacy full-node setting must never widen the cluster selection.
	repo.rows["logs"] = &model.PluginConfig{EdgeID: 1, PluginName: "logs", Enabled: false, SpecJSON: `{"mode":"host","enable_journald":false,"pod_log_path":"/var/log/pods/*/*/*.log"}`}
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
	units := []interface{}{"kubelet.service", "containerd.service"}
	update := SetInput{ClusterManaged: true, Enabled: true, Spec: map[string]interface{}{"journald_units": units}}
	if _, err := uc.Set(ctx, 2, "logs", update); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("stale node form overwrote host settings: %v", err)
	}
	saved, err := uc.Set(ctx, 1, "logs", update)
	if err != nil || !saved.Enabled || !saved.ClusterManaged || saved.Spec["mode"] != "kubernetes" || !reflect.DeepEqual(saved.Spec["journald_units"], units) {
		t.Fatalf("save node units: %+v %v", saved, err)
	}
	stored := repo.rows["logs"]
	if stored.Enabled || decodeSpec(stored.SpecJSON)["mode"] != "host" || strings.Contains(stored.SpecJSON, "pod_log_paths") {
		t.Fatalf("save changed host intent: %+v", stored)
	}
	if _, err := uc.Set(ctx, 3, "logs", update); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("controller accepted journal units: %v", err)
	}
	for _, invalid := range []SetInput{
		{Spec: update.Spec},
		{Enabled: true, Spec: map[string]interface{}{"journald_units": units, "pod_log_paths": []string{"/var/log/pods/*/*/*.log"}}},
		{Enabled: true, Spec: map[string]interface{}{"journald_units": "kubelet.service"}},
		{Enabled: true, Spec: map[string]interface{}{"journald_units": []string{"bad\nunit"}}},
		{Enabled: true, Spec: map[string]interface{}{"journald_units": strings.Fields(strings.Repeat("x.service ", 65))}},
	} {
		if _, err := uc.Set(ctx, 1, "logs", invalid); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("invalid override accepted: %+v, %v", invalid, err)
		}
	}
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
		if !reflect.DeepEqual(cfg.Spec["journald_units"], units) || !reflect.DeepEqual(cfg.Spec["pod_log_paths"], paths) {
			t.Fatalf("lost node units or cluster selection: %+v", cfg.Spec)
		}
		if cfg.Spec["enable_journald"] != true || cfg.Enabled != step.want || cfg.Spec["mode"] != "kubernetes" || cfg.Spec["pod_log_path"] == "/var/log/pods/*/*/*.log" {
			t.Fatalf("wire: %+v", cfg)
		}
		rows, err := uc.ListForUI(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.PluginName == "logs" && (row.Enabled != step.want || !row.ClusterManaged || !reflect.DeepEqual(row.Spec["journald_units"], units)) {
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
	update.Spec = map[string]interface{}{"journald_units": []string{}}
	if _, err := uc.Set(ctx, 1, "logs", update); err != nil {
		t.Fatal(err)
	}
	wire, err := uc.FetchForEdge(ctx, 1, false)
	if err != nil || !reflect.DeepEqual(wire.Configs["logs"].Spec["journald_units"], []interface{}{}) {
		t.Fatalf("cleared unit filter: %+v %v", wire, err)
	}
}
