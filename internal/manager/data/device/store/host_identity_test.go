package store

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
)

// 回归：未注册 Edge 的编号与已注册主机 Device 编号相同。
func TestMigrationDoesNotGuessHostIdentityFromEdgeID(t *testing.T) {
	db := newDeviceTestDB(t)
	if err := db.AutoMigrate(&edgemodel.Edge{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	dev := sampleDevice("real-host")
	dev.ID = 1
	if err := db.Create(dev).Error; err != nil {
		t.Fatal(err)
	}
	edges := []edgemodel.Edge{
		{ID: 1, AccessKeyID: "unused-test-key", Status: edgemodel.StatusOffline},
		{ID: 2, AccessKeyID: "active-test-key", Name: "real-host", DeviceID: &dev.ID, Status: edgemodel.StatusOnline},
	}
	if err := db.Create(&edges).Error; err != nil {
		t.Fatal(err)
	}
	links := NewEdgeDeviceRepo(db)
	ctx := context.Background()
	if err := links.Link(ctx, 2, 1, model.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	before, err := links.LookupEdgeForDevice(ctx, 1, model.EdgeDeviceRelationHost)
	if err != nil || before != 2 {
		t.Fatalf("before=%d err=%v", before, err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	after, err := links.LookupEdgeForDevice(ctx, 1, model.EdgeDeviceRelationHost)
	if err != nil {
		t.Fatal(err)
	}
	var unused edgemodel.Edge
	if err := db.First(&unused, 1).Error; err != nil {
		t.Fatal(err)
	}
	if unused.DeviceID != nil {
		t.Fatalf("migration invented a host association: %d", *unused.DeviceID)
	}
	if err := links.Link(ctx, 2, 1, model.EdgeDeviceRelationHost); err != nil {
		t.Fatal(err)
	}
	reconnected, err := links.LookupEdgeForDevice(ctx, 1, model.EdgeDeviceRelationHost)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after active edge registers again: edge=%d", reconnected)
	if after != before {
		t.Fatalf("migration rerouted existing host from edge %d to edge %d", before, after)
	}
}

// 重装保留 Host，旧实例迟到不能夺回通道或覆盖在线状态。
func TestHostReinstallKeepsIdentityAndRejectsStalePresence(t *testing.T) {
	db := newDeviceTestDB(t)
	if err := db.AutoMigrate(&edgemodel.Edge{}); err != nil {
		t.Fatal(err)
	}
	host := sampleDevice("persistent-host")
	host.ID = 41
	if err := db.Create(host).Error; err != nil {
		t.Fatal(err)
	}
	for _, e := range []edgemodel.Edge{{ID: 7, AccessKeyID: "old-install"}, {ID: 23, AccessKeyID: "new-install"}} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	links := NewEdgeDeviceRepo(db)
	repo := NewRepo(db)
	for _, id := range []uint64{7, 23, 7} {
		if err := links.Link(ctx, id, 41, model.EdgeDeviceRelationHost); err != nil {
			t.Fatal(err)
		}
	}
	current, err := links.LookupEdgeForDevice(ctx, 41, model.EdgeDeviceRelationHost)
	if err != nil || current != 23 {
		t.Fatalf("current=%d err=%v", current, err)
	}
	for _, id := range []uint64{7, 23} {
		got, err := links.LookupHostDevice(ctx, id)
		if err != nil || got != 41 {
			t.Fatalf("edge %d host=%d err=%v", id, got, err)
		}
	}
	if err := repo.UpdateHostPresence(ctx, 41, 23, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateHostPresence(ctx, 41, 7, false); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, 41)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Online {
		t.Fatal("old edge disconnected current host")
	}
	if err := repo.UpdateHostPresence(ctx, 41, 23, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateHostPresence(ctx, 41, 7, true); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, 41)
	if err != nil {
		t.Fatal(err)
	}
	if got.Online {
		t.Fatal("old heartbeat revived current host")
	}
}
