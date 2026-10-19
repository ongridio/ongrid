package store

import (
	"context"
	"errors"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	deviceStore "github.com/ongridio/ongrid/internal/manager/data/device/store"
	device "github.com/ongridio/ongrid/internal/manager/model/device"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestHostConfigSurvivesReinstallationAndRejectsOldInstallation(t *testing.T) {
	repo := newTestRepo(t)
	db := repo.db
	ctx := context.Background()
	if err := deviceStore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	host := device.Device{ID: 10, Name: "stable-host", Fingerprint: "host-config-fingerprint"}
	if err := db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	configs := NewPluginConfigRepo(db)
	links := deviceStore.NewEdgeDeviceRepo(db)
	for _, id := range []uint64{41, 42} {
		e := model.Edge{ID: id, AccessKeyID: []string{"old-install", "new-install"}[id-41]}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := configs.Upsert(ctx, &model.PluginConfig{EdgeID: id, PluginName: "autoapm", Enabled: true, SpecJSON: `{"targets":[]}`}); err != nil {
			t.Fatal(err)
		}
	}
	if err := links.Link(ctx, 41, 10, device.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	custom := `{"targets":[{"executable":"/opt/orders","port":8080,"service_name":"orders","log_path":"/var/log/orders.log"}]}`
	if _, err := configs.Upsert(ctx, &model.PluginConfig{EdgeID: 41, PluginName: "autoapm", Enabled: true, SpecJSON: custom}); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Upsert(ctx, &model.PluginConfig{EdgeID: 41, PluginName: "logs", Enabled: false, SpecJSON: `{"file_paths":["/var/log/custom.log"]}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Upsert(ctx, &model.PluginConfig{EdgeID: 41, PluginName: "profiles", Enabled: true, SpecJSON: `{"session_id":"old-session"}`}); err != nil {
		t.Fatal(err)
	}
	if err := links.Unlink(ctx, 41, 10, device.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&model.Edge{}, 41).Error; err != nil {
		t.Fatal(err)
	}
	// 无安装时长期配置仍在 Host，允许直接更新，且不发送到编号相同的 Edge。
	got, err := configs.GetByHost(ctx, 10, "autoapm")
	if err != nil || got.SpecJSON != custom {
		t.Fatalf("host config: %+v %v", got, err)
	}
	if err := links.Link(ctx, 42, 10, device.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	got, err = configs.Get(ctx, 42, "autoapm")
	if err != nil || got.SpecJSON != custom {
		t.Fatalf("reinstall overwrote target: %+v %v", got, err)
	}
	logCfg, err := configs.Get(ctx, 42, "logs")
	if err != nil || logCfg.Enabled {
		t.Fatalf("disabled intent lost: %+v %v", logCfg, err)
	}
	if _, err := configs.Get(ctx, 42, "profiles"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("profile session replayed: %v", err)
	}
	// 旧实例即使没有被删除，也不能通过兼容接口写 Host。
	if err := db.Unscoped().Model(&model.Edge{}).Where("id = ?", 41).Updates(map[string]interface{}{"delete_marker": 0, "deleted_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Upsert(ctx, &model.PluginConfig{EdgeID: 41, PluginName: "autoapm", SpecJSON: `{}`}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("stale write accepted: %v", err)
	}
	if err := deviceStore.Migrate(db); err != nil {
		t.Fatal(err)
	}
	got, err = configs.Get(ctx, 42, "autoapm")
	if err != nil || got.SpecJSON != custom {
		t.Fatalf("restart changed config: %+v %v", got, err)
	}
	if err := configs.DeleteHost(ctx, 10, "autoapm"); err != nil {
		t.Fatal(err)
	}
	if err := links.Link(ctx, 42, 10, device.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Get(ctx, 42, "autoapm"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted intent resurrected: %v", err)
	}
}

func TestHostConfigurationCanBeEditedWithoutInstallation(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	if err := deviceStore.Migrate(repo.db); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Create(&device.Device{ID: 10, Name: "offline", Fingerprint: "offline-host"}).Error; err != nil {
		t.Fatal(err)
	}
	uc := biz.NewPluginConfigUC(NewPluginConfigRepo(repo.db), nil, nil, nil)
	uc.SetHostConfigTarget(func(context.Context, uint64) (uint64, *biz.AutoAPMDefaults, error) {
		return 0, &biz.AutoAPMDefaults{Environment: "production"}, nil
	})
	spec := map[string]interface{}{"targets": []interface{}{map[string]interface{}{"executable": "/opt/orders", "port": 8080, "service_name": "orders"}}}
	if _, err := uc.SetForHost(ctx, 10, "autoapm", biz.SetInput{Spec: spec}); err != nil {
		t.Fatal(err)
	}
	rows, edgeID, err := uc.ListForHost(ctx, 10)
	if err != nil || edgeID != 0 {
		t.Fatalf("offline list %d %v", edgeID, err)
	}
	found := false
	for _, r := range rows {
		if r.PluginName == "autoapm" {
			found = r.Enabled && r.Defaults.Environment == "production"
		}
	}
	if !found {
		t.Fatal("host configuration missing")
	}
	if _, err := uc.SetForHost(ctx, 10, "profiles", biz.SetInput{}); !errors.Is(err, errs.ErrInvalid) {
		t.Fatal("session accepted as durable config")
	}
}

func TestDeletedHostDoesNotContributePluginOptionsOrCounts(t *testing.T) {
	repo := newTestRepo(t)
	if err := deviceStore.Migrate(repo.db); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	host := device.Device{ID: 650, Name: "retired", Fingerprint: "retired-host"}
	if err := repo.db.Create(&host).Error; err != nil {
		t.Fatal(err)
	}
	configs := NewPluginConfigRepo(repo.db)
	if _, err := configs.UpsertHost(ctx, host.ID, &model.PluginConfig{PluginName: "autoapm", Enabled: true, SpecJSON: `{"targets":[]}`}); err != nil {
		t.Fatal(err)
	}
	before, err := configs.CountByPlugin(ctx)
	if err != nil || before["autoapm"] != 1 {
		t.Fatalf("live count: %v %v", before, err)
	}
	if err := repo.db.Delete(&host).Error; err != nil {
		t.Fatal(err)
	}
	after, err := configs.CountByPlugin(ctx)
	if err != nil || after["autoapm"] != 0 {
		t.Fatalf("deleted count: %v %v", after, err)
	}
	specs, err := configs.ListAutoAPMSpecs(ctx)
	if err != nil || len(specs) != 0 {
		t.Fatalf("deleted options: %v %v", specs, err)
	}
}
