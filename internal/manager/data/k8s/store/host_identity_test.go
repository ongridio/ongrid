package store

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func legacyNodeHost(t *testing.T) (*gorm.DB, *Repo, model.Node, devicemodel.Device) {
	t.Helper()
	db, repo := newTestRepo(t)
	if err := db.AutoMigrate(&edgemodel.Edge{}, &devicemodel.Device{}, &devicemodel.EdgeDevice{}, &edgemodel.HostPluginConfig{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	edgeID, hostID, topologyID := uint64(7), uint64(41), uint64(100)
	currentEdgeID := edgeID
	node := model.Node{ID: 5, ClusterID: 3, NodeName: "node-a", NodeUID: "uid-a", EdgeID: &edgeID, DeviceID: &hostID}
	host := devicemodel.Device{
		ID: hostID, CurrentEdgeID: &currentEdgeID, Fingerprint: nodeHostFingerprint(node.ClusterID, "name:"+node.NodeName),
		Hostname: node.NodeName, Name: "custom host name", Description: "keep history", Roles: devicemodel.RoleBitServer, NodeID: &topologyID,
	}
	for _, row := range []any{
		&host, &edgemodel.Edge{ID: edgeID, AccessKeyID: "node-ak", DeviceID: &hostID}, &node,
		&devicemodel.EdgeDevice{EdgeID: edgeID, DeviceID: hostID, Type: devicemodel.EdgeDeviceRelationHost},
		&edgemodel.HostPluginConfig{DeviceID: hostID, PluginName: "logs", Enabled: true, SpecJSON: `{"journald_units":["sshd"]}`},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db, repo, node, host
}

func TestMigrateNodeHostFingerprintPreservesHost(t *testing.T) {
	for _, orphan := range []bool{false, true} {
		name := "first upgrade"
		if orphan {
			name = "retry after failed upgrade"
		}
		t.Run(name, func(t *testing.T) {
			db, repo, node, before := legacyNodeHost(t)
			var duplicate devicemodel.Device
			if orphan {
				duplicate = devicemodel.Device{Fingerprint: nodeHostFingerprint(node.ClusterID, node.NodeUID), Name: node.NodeName, Hostname: node.NodeName}
				if err := db.Create(&duplicate).Error; err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := repo.MigrateNodeHostFingerprint(t.Context(), node.ID, *node.EdgeID, node.NodeUID); err != nil {
					t.Fatal(err)
				}
			}
			var after devicemodel.Device
			if err := db.First(&after, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			if after.Fingerprint != nodeHostFingerprint(node.ClusterID, node.NodeUID) ||
				after.Name != before.Name || after.Description != before.Description || after.Roles != before.Roles ||
				*after.NodeID != *before.NodeID || *after.CurrentEdgeID != *before.CurrentEdgeID {
				t.Fatalf("host identity or metadata lost: %+v", after)
			}
			var config edgemodel.HostPluginConfig
			if err := db.Where("device_id = ?", before.ID).First(&config).Error; err != nil || config.SpecJSON != `{"journald_units":["sshd"]}` || !config.Enabled {
				t.Fatalf("host plugin configuration changed: %+v %v", config, err)
			}
			var count int64
			if err := db.Model(&devicemodel.Device{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("duplicate hosts: count=%d error=%v", count, err)
			}
			if orphan {
				if err := db.First(&devicemodel.Device{}, duplicate.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("failed-registration orphan is still active: %v", err)
				}
				if err := db.Unscoped().First(&duplicate, duplicate.ID).Error; err != nil || duplicate.DeletedAt == nil {
					t.Fatalf("orphan was not retained as a soft-deleted row: %v", err)
				}
			}
		})
	}
}

func TestMigrateNodeHostFingerprintRejectsConflictingBindings(t *testing.T) {
	for _, state := range []string{"edge host mismatch", "superseded edge", "missing host link", "changed node UID", "used fingerprint", "edited orphan", "orphan plugin configuration", "historical orphan link", "write failure"} {
		t.Run(state, func(t *testing.T) {
			db, repo, node, host := legacyNodeHost(t)
			duplicate := devicemodel.Device{Fingerprint: nodeHostFingerprint(node.ClusterID, node.NodeUID), Name: node.NodeName, Hostname: node.NodeName}
			if err := db.Create(&duplicate).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			switch state {
			case "edge host mismatch":
				err = db.Model(&edgemodel.Edge{}).Where("id = ?", *node.EdgeID).Update("device_id", duplicate.ID).Error
			case "superseded edge":
				err = db.Model(&host).Update("current_edge_id", 99).Error
			case "missing host link":
				err = db.Where("edge_id = ?", *node.EdgeID).Delete(&devicemodel.EdgeDevice{}).Error
			case "changed node UID":
				err = db.Model(&model.Node{}).Where("id = ?", node.ID).Update("node_uid", "replacement-uid").Error
			case "used fingerprint":
				err = db.Model(&duplicate).Update("current_edge_id", 99).Error
			case "edited orphan":
				err = db.Model(&duplicate).Update("description", "user data").Error
			case "orphan plugin configuration":
				err = db.Create(&edgemodel.HostPluginConfig{DeviceID: duplicate.ID, PluginName: "logs", SpecJSON: `{}`}).Error
			case "historical orphan link":
				link := devicemodel.EdgeDevice{EdgeID: 99, DeviceID: duplicate.ID, Type: devicemodel.EdgeDeviceRelationHost}
				if err = db.Create(&link).Error; err == nil {
					err = db.Delete(&link).Error
				}
			case "write failure":
				err = db.Exec(`CREATE TRIGGER reject_host_migration BEFORE UPDATE OF fingerprint ON devices WHEN OLD.id = 41 BEGIN SELECT RAISE(ABORT, 'migration write failed'); END`).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.MigrateNodeHostFingerprint(t.Context(), node.ID, *node.EdgeID, node.NodeUID); err == nil {
				t.Fatal("conflicting identity was migrated")
			} else if state != "changed node UID" && state != "missing host link" && state != "write failure" && !errors.Is(err, errs.ErrConflict) {
				t.Fatalf("expected identity conflict: %v", err)
			}
			var saved devicemodel.Device
			if err := db.First(&saved, host.ID).Error; err != nil || saved.Fingerprint != host.Fingerprint {
				t.Fatalf("failed migration changed original host: %v", err)
			}
			if err := db.First(&devicemodel.Device{}, duplicate.ID).Error; err != nil {
				t.Fatalf("failed migration deleted conflicting host: %v", err)
			}
		})
	}
}
