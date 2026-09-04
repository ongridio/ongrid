package edge

import (
	"context"
	"strings"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestSetProfilesValidatesRuntimeTarget(t *testing.T) {
	uc := NewPluginConfigUC(newFakePluginConfigRepo(), nil, fakeEndpointResolver{}, nil)
	row, err := uc.Set(context.Background(), 7, model.PluginNameProfiles, SetInput{Enabled: true, Spec: map[string]interface{}{
		"mode": "pprof",
		"runtime_target": map[string]interface{}{
			"url": "http://127.0.0.1:6060/debug/pprof/heap", "profile_type": "heap", "collection_interval_seconds": 60,
		},
	}})
	if err != nil || row.PluginName != model.PluginNameProfiles {
		t.Fatalf("row=%#v err=%v", row, err)
	}
	if row.Spec["duration_seconds"] != float64(30) {
		t.Fatalf("duration_seconds=%v, want 30", row.Spec["duration_seconds"])
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, row.Spec["expires_at"].(string))
	if err != nil || time.Until(expiresAt) < 25*time.Second || time.Until(expiresAt) > 31*time.Second {
		t.Fatalf("expires_at=%v err=%v", expiresAt, err)
	}

	_, err = uc.Set(context.Background(), 7, model.PluginNameProfiles, SetInput{Enabled: true, Spec: map[string]interface{}{
		"mode": "pprof", "runtime_target": map[string]interface{}{"url": "file:///etc/passwd", "profile_type": "heap"},
	}})
	if err == nil || !strings.Contains(err.Error(), "http(s) URL") {
		t.Fatalf("err=%v, want URL validation error", err)
	}

	_, err = uc.Set(context.Background(), 7, model.PluginNameProfiles, SetInput{Enabled: true, Spec: map[string]interface{}{"mode": "ebpf"}})
	if err == nil || !strings.Contains(err.Error(), "must be pprof") {
		t.Fatalf("err=%v, want removed ebpf mode rejection", err)
	}
}
