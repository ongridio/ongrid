package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	devicestore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

func TestQueryEdgesTool_Info(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewQueryEdgesTool(nil, uc, nil)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameQueryEdges {
		t.Errorf("Name = %q", info.Name)
	}
	if !strings.Contains(strings.ToLower(info.WhenToUse), "not") {
		t.Errorf("WhenToUse needs reverse guard")
	}
	if !strings.Contains(string(info.Parameters), `"gpu"`) {
		t.Errorf("query_devices schema does not advertise GPU role")
	}
}

func TestQueryEdgesTool_LegacyEdgeFallback(t *testing.T) {
	// devices nil → falls back to edges path.
	e1 := &edgemodel.Edge{ID: 1, Name: "alpha", Status: "online"}
	e2 := &edgemodel.Edge{ID: 2, Name: "beta", Status: "offline"}
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(e1, e2), nil, nil, slog.Default())
	tool := NewQueryEdgesTool(nil, uc, nil)

	out, err := tool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	devs, _ := got["devices"].([]any)
	if len(devs) == 0 {
		t.Errorf("expected devices in response")
	}
}

func TestQueryEdgesTool_BadArgs(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	tool := NewQueryEdgesTool(nil, uc, nil)
	if _, err := tool.InvokableRun(context.Background(), `not json`); err == nil {
		t.Errorf("expected error for non-JSON")
	}
}

func TestQueryEdgesTool_NilDeps(t *testing.T) {
	tool := NewQueryEdgesTool(nil, nil, nil)
	_, err := tool.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Errorf("expected error when both devices and edges are nil")
	}
}

func TestQueryEdgesTool_GPUFilter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := devicestore.Migrate(db); err != nil {
		t.Fatalf("migrate devices: %v", err)
	}
	for _, device := range []*devicemodel.Device{
		{Fingerprint: "gpu", Name: "gpu-host", Hostname: "gpu-host", OS: "linux", Arch: "amd64", KernelVersion: "6.8", CPUCount: 8, MemTotalBytes: 1, Roles: devicemodel.RoleBitGPU},
		{Fingerprint: "server", Name: "server-host", Hostname: "server-host", OS: "linux", Arch: "amd64", KernelVersion: "6.8", CPUCount: 8, MemTotalBytes: 1, Roles: devicemodel.RoleBitServer},
	} {
		if err := db.Create(device).Error; err != nil {
			t.Fatalf("create device %q: %v", device.Name, err)
		}
	}

	uc := devicebiz.NewUsecase(devicestore.NewRepo(db), nil, slog.Default())
	tool := NewQueryEdgesTool(uc, nil, nil)
	out, err := tool.InvokableRun(context.Background(), `{"role":"gpu"}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var got struct {
		Devices []struct {
			Name  string   `json:"name"`
			Roles []string `json:"roles"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(got.Devices) != 1 || got.Devices[0].Name != "gpu-host" {
		t.Fatalf("GPU filter returned %#v, want gpu-host only", got.Devices)
	}
	if len(got.Devices[0].Roles) != 1 || got.Devices[0].Roles[0] != devicemodel.RoleGPU {
		t.Fatalf("GPU roles = %v, want [%q]", got.Devices[0].Roles, devicemodel.RoleGPU)
	}
}
