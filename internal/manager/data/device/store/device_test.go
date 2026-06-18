package store

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
)

func newDeviceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func sampleDevice(fingerprint string) *model.Device {
	return &model.Device{
		Fingerprint:    fingerprint,
		Name:           fingerprint,
		Hostname:       fingerprint,
		OS:             "linux",
		Arch:           "amd64",
		KernelVersion:  "6.8.0",
		CPUCount:       2,
		MemTotalBytes:  4096,
		DiskTotalBytes: 8192,
	}
}

func TestFindOrCreateByFingerprintSoftDeleteAllowsReuse(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewRepo(db)
	ctx := context.Background()

	first, err := repo.FindOrCreateByFingerprint(ctx, sampleDevice("host-a"))
	if err != nil {
		t.Fatalf("first FindOrCreateByFingerprint: %v", err)
	}

	again, err := repo.FindOrCreateByFingerprint(ctx, sampleDevice("host-a"))
	if err != nil {
		t.Fatalf("second FindOrCreateByFingerprint: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("active duplicate created id %d, want existing id %d", again.ID, first.ID)
	}

	if err := repo.Delete(ctx, first.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	recreated, err := repo.FindOrCreateByFingerprint(ctx, sampleDevice("host-a"))
	if err != nil {
		t.Fatalf("recreate after soft delete: %v", err)
	}
	if recreated.ID == first.ID {
		t.Fatalf("recreated row reused soft-deleted id %d", first.ID)
	}
	if n, err := repo.Count(ctx); err != nil || n != 1 {
		t.Fatalf("Count after recreate = %d, %v; want 1,nil", n, err)
	}
}

func TestEdgeDeviceLinkSoftDeleteAllowsReuse(t *testing.T) {
	db := newDeviceTestDB(t)
	repo := NewEdgeDeviceRepo(db)
	ctx := context.Background()

	if err := repo.Link(ctx, 1, 2, model.EdgeDeviceRelationHost); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	if err := repo.Link(ctx, 1, 2, model.EdgeDeviceRelationHost); err != nil {
		t.Fatalf("duplicate active Link should be idempotent: %v", err)
	}
	rows, err := repo.ListDevicesForEdge(ctx, 1)
	if err != nil {
		t.Fatalf("ListDevicesForEdge: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("active duplicate link count = %d, want 1", len(rows))
	}

	if err := repo.Unlink(ctx, 1, 2, model.EdgeDeviceRelationHost); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if err := repo.Link(ctx, 1, 2, model.EdgeDeviceRelationHost); err != nil {
		t.Fatalf("relink after soft delete: %v", err)
	}
	rows, err = repo.ListDevicesForEdge(ctx, 1)
	if err != nil {
		t.Fatalf("ListDevicesForEdge after relink: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("active relink count = %d, want 1", len(rows))
	}
}
