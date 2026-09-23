package edge

import (
	"context"
	"encoding/json"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

func TestHostServiceLogsFollowTargetsAndPreserveDeviceSettings(t *testing.T) {
	ctx := context.Background()
	repo := newFakePluginConfigRepo()
	uc := NewPluginConfigUC(repo, nil, fakeEndpointResolver{}, nil)
	if enabled, err := uc.IsEnabled(ctx, 1, "traces"); err != nil || !enabled {
		t.Fatalf("SDK ingestion default: %v %v", enabled, err)
	}
	uc.SetAutoAPMEnvironmentProvider(func(context.Context, uint64) (string, string, error) { return "production", "hosts", nil })
	original := `{"enable_journald":true,"file_paths":["/var/log/existing.log"]}`
	for _, deviceLogs := range []bool{false, true} {
		repo.rows["logs"] = &model.PluginConfig{EdgeID: 1, PluginName: "logs", Enabled: deviceLogs, SpecJSON: original}
		for _, path := range []string{"/var/log/orders/*.log", ""} {
			target := autoapm.Target{Executable: "/opt/orders", Port: 8080, ServiceName: "orders", ServiceNamespace: "shop", LogPath: path}
			if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Spec: autoapm.Spec{Environment: "legacy-collector", Targets: []autoapm.Target{target}}.Map()}); err != nil {
				t.Fatal(err)
			}
			wire, err := uc.FetchForEdge(ctx, 1, false)
			if err != nil {
				t.Fatal(err)
			}
			want := deviceLogs || path != ""
			cfg := wire.Configs["logs"]
			if cfg.Enabled != want {
				t.Fatalf("enabled: %+v", cfg)
			}
			if path != "" {
				capture, err := autoapm.Parse(cfg.Spec["service_capture"].(map[string]interface{}))
				if err != nil || capture.Environment != "production" || capture.Targets[0].LogPath != path {
					t.Fatalf("capture: %+v %v", capture, err)
				}
				if !deviceLogs && cfg.Spec["enable_journald"] != false {
					t.Fatal("re-enabled device logs")
				}
			} else if cfg.Spec["service_capture"] != nil {
				t.Fatal("cleared path still captured")
			}
			body, err := json.Marshal(wire.Configs["autoapm"].Spec)
			if err != nil {
				t.Fatal(err)
			}
			var raw map[string]interface{}
			if err := json.Unmarshal(body, &raw); err != nil {
				t.Fatal(err)
			}
			if _, ok := raw["targets"].([]interface{})[0].(map[string]interface{})["log_path"]; ok {
				t.Fatal("log path sent to legacy OBI parser")
			}
			enabled, err := uc.IsEnabled(ctx, 1, "logs")
			if err != nil || enabled != want {
				t.Fatalf("ingestion: %v %v", enabled, err)
			}
			rows, err := uc.ListForUI(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.PluginName == "logs" && (row.Enabled != want || row.Spec["service_capture"] != nil) {
					t.Fatalf("UI: %+v", row)
				}
			}
			if repo.rows["logs"].Enabled != deviceLogs || repo.rows["logs"].SpecJSON != original {
				t.Fatal("device config mutated")
			}
		}
		if _, err := uc.Set(ctx, 1, "autoapm", SetInput{Spec: autoapm.Spec{}.Map()}); err != nil {
			t.Fatal(err)
		}
		wire, err := uc.FetchForEdge(ctx, 1, false)
		if err != nil || wire.Configs["logs"].Enabled != deviceLogs || wire.Configs["logs"].Spec["service_capture"] != nil {
			t.Fatalf("removal: %+v %v", wire, err)
		}
	}
}
