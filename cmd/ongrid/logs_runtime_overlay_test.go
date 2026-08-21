package main

import (
	"context"
	"errors"
	"testing"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type fakeLogsRuntimeBase struct {
	overlay map[string]interface{}
	err     error
}

func (f fakeLogsRuntimeBase) PluginRuntimeOverlay(context.Context, uint64, string) (map[string]interface{}, error) {
	return f.overlay, f.err
}

type fakeLogsRuntimeHosts struct {
	deviceID uint64
	err      error
}

func (f fakeLogsRuntimeHosts) LookupHostDevice(context.Context, uint64) (uint64, error) {
	return f.deviceID, f.err
}

type fakeLogsRuntimeDevices struct {
	device *devicemodel.Device
	err    error
}

func (f fakeLogsRuntimeDevices) Get(context.Context, uint64) (*devicemodel.Device, error) {
	return f.device, f.err
}

type fakeLogsRuntimeClusters struct {
	id   uint64
	name string
	err  error
}

func (f fakeLogsRuntimeClusters) ResolveDeviceCluster(context.Context, uint64) (uint64, string, error) {
	return f.id, f.name, f.err
}

func TestLogsRuntimeOverlayAddsHostClusterWithoutMutatingBase(t *testing.T) {
	nodeID := uint64(91)
	base := map[string]interface{}{"backend": "external_elasticsearch"}
	provider := logsRuntimeOverlayProvider{
		base:     fakeLogsRuntimeBase{overlay: base},
		hosts:    fakeLogsRuntimeHosts{deviceID: 42},
		devices:  fakeLogsRuntimeDevices{device: &devicemodel.Device{ID: 42, NodeID: &nodeID}},
		clusters: fakeLogsRuntimeClusters{id: 7, name: "edge-fleet-a"},
	}

	overlay, err := provider.PluginRuntimeOverlay(context.Background(), 12, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay: %v", err)
	}
	if overlay["backend"] != "external_elasticsearch" || overlay["cluster_id"] != "7" || overlay["cluster_name"] != "edge-fleet-a" {
		t.Fatalf("overlay = %#v", overlay)
	}
	if _, exists := base["cluster_id"]; exists {
		t.Fatal("base overlay was mutated")
	}
}

func TestLogsRuntimeOverlayAllowsUnboundAndControlOnlyEdges(t *testing.T) {
	base := map[string]interface{}{"backend": "builtin_loki"}
	for name, hosts := range map[string]fakeLogsRuntimeHosts{
		"control only": {err: errs.ErrNotFound},
		"no device":    {deviceID: 0},
	} {
		t.Run(name, func(t *testing.T) {
			provider := logsRuntimeOverlayProvider{
				base: fakeLogsRuntimeBase{overlay: base}, hosts: hosts,
				devices: fakeLogsRuntimeDevices{}, clusters: fakeLogsRuntimeClusters{},
			}
			overlay, err := provider.PluginRuntimeOverlay(context.Background(), 12, "logs")
			if err != nil {
				t.Fatalf("PluginRuntimeOverlay: %v", err)
			}
			if overlay["backend"] != "builtin_loki" || len(overlay) != 1 {
				t.Fatalf("overlay = %#v", overlay)
			}
		})
	}
}

func TestLogsRuntimeOverlaySkipsTopologyForOtherPlugins(t *testing.T) {
	provider := logsRuntimeOverlayProvider{
		base:     fakeLogsRuntimeBase{overlay: map[string]interface{}{"trace": true}},
		hosts:    fakeLogsRuntimeHosts{err: errors.New("must not be called")},
		devices:  fakeLogsRuntimeDevices{},
		clusters: fakeLogsRuntimeClusters{},
	}
	overlay, err := provider.PluginRuntimeOverlay(context.Background(), 12, "traces")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay: %v", err)
	}
	if overlay["trace"] != true || len(overlay) != 1 {
		t.Fatalf("overlay = %#v", overlay)
	}
}

func TestLogsRuntimeOverlayPropagatesHostResolutionFailure(t *testing.T) {
	wantErr := errors.New("database unavailable")
	provider := logsRuntimeOverlayProvider{
		base:     fakeLogsRuntimeBase{overlay: map[string]interface{}{"backend": "builtin_loki"}},
		hosts:    fakeLogsRuntimeHosts{err: wantErr},
		devices:  fakeLogsRuntimeDevices{},
		clusters: fakeLogsRuntimeClusters{},
	}

	_, err := provider.PluginRuntimeOverlay(context.Background(), 12, "logs")
	if !errors.Is(err, wantErr) {
		t.Fatalf("PluginRuntimeOverlay error = %v, want %v", err, wantErr)
	}
}
