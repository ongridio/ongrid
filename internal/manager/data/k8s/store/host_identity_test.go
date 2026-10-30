package store

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	topologystore "github.com/ongridio/ongrid/internal/manager/data/topology/store"
	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	topologymodel "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func legacyNodeHost(t *testing.T) (*gorm.DB, *Repo, model.Node, devicemodel.Device) {
	t.Helper()
	db, repo := newTestRepo(t)
	return seedLegacyNodeHost(t, db, repo)
}

func seedLegacyNodeHost(t *testing.T, db *gorm.DB, repo *Repo) (*gorm.DB, *Repo, model.Node, devicemodel.Device) {
	t.Helper()
	if err := db.AutoMigrate(&edgemodel.Edge{}, &devicemodel.Device{}, &devicemodel.EdgeDevice{}, &edgemodel.HostPluginConfig{}, &topologymodel.Node{}); err != nil {
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
		&topologymodel.Node{ID: topologyID, Type: "device", Name: host.Name, PropsJSON: `{"owner":"ops"}`},
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
	for _, name := range []string{"first upgrade", "retry after failed upgrade", "retry after Manager restart"} {
		t.Run(name, func(t *testing.T) {
			db, repo, node, before := legacyNodeHost(t)
			var duplicate devicemodel.Device
			if name != "first upgrade" {
				duplicate = devicemodel.Device{Fingerprint: nodeHostFingerprint(node.ClusterID, node.NodeUID), Name: node.NodeName, Hostname: node.NodeName}
				if err := db.Create(&duplicate).Error; err != nil {
					t.Fatal(err)
				}
			}
			if name == "retry after Manager restart" {
				if err := topologystore.Migrate(db); err != nil {
					t.Fatal(err)
				}
				if err := db.First(&duplicate, duplicate.ID).Error; err != nil || duplicate.NodeID == nil {
					t.Fatalf("startup did not backfill the unused duplicate's topology: %v", err)
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
			var topology topologymodel.Node
			if err := db.First(&topology, *before.NodeID).Error; err != nil || topology.PropsJSON != `{"owner":"ops"}` {
				t.Fatalf("original topology changed: %+v %v", topology, err)
			}
			if duplicate.ID != 0 {
				if err := db.First(&devicemodel.Device{}, duplicate.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("failed-registration orphan is still active: %v", err)
				}
				if err := db.Unscoped().First(&duplicate, duplicate.ID).Error; err != nil || duplicate.DeletedAt == nil {
					t.Fatalf("orphan was not retained as a soft-deleted row: %v", err)
				}
				if duplicate.NodeID != nil {
					if err := db.Unscoped().First(&topologymodel.Node{}, *duplicate.NodeID).Error; err != nil {
						t.Fatalf("orphan topology was not retained: %v", err)
					}
					if err := db.First(&topologymodel.Node{}, *duplicate.NodeID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("orphan topology is still active: %v", err)
					}
				}
			}
		})
	}
}

func TestMigrateNodeHostFingerprintRejectsConflictingBindings(t *testing.T) {
	for _, state := range []string{"edge host mismatch", "superseded edge", "missing host link", "changed node UID", "used fingerprint", "edited orphan", "orphan plugin configuration", "historical orphan link", "topology name", "topology properties", "topology type", "previously edited topology", "topology relation", "historical topology relation", "shared topology", "cluster topology", "topology delete failure", "write failure"} {
		t.Run(state, func(t *testing.T) {
			db, repo, node, host := legacyNodeHost(t)
			duplicate := devicemodel.Device{Fingerprint: nodeHostFingerprint(node.ClusterID, node.NodeUID), Name: node.NodeName, Hostname: node.NodeName}
			if err := db.Create(&duplicate).Error; err != nil {
				t.Fatal(err)
			}
			if err := topologystore.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&duplicate, duplicate.ID).Error; err != nil || duplicate.NodeID == nil {
				t.Fatalf("load backfilled orphan: %v", err)
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
			case "topology name":
				err = db.Model(&topologymodel.Node{}).Where("id = ?", *duplicate.NodeID).UpdateColumn("name", "user name").Error
			case "topology properties":
				err = db.Model(&topologymodel.Node{}).Where("id = ?", *duplicate.NodeID).UpdateColumn("props_jsonb", `{"owner":"ops"}`).Error
			case "topology type":
				err = db.Model(&topologymodel.Node{}).Where("id = ?", *duplicate.NodeID).UpdateColumn("type", "cluster").Error
			case "previously edited topology":
				err = db.Model(&topologymodel.Node{}).Where("id = ?", *duplicate.NodeID).Update("updated_at", time.Now().Add(time.Minute)).Error
			case "topology relation", "historical topology relation":
				relation := topologymodel.Relation{SrcID: *duplicate.NodeID, DstID: *host.NodeID, Type: "depends_on"}
				if err = db.Create(&relation).Error; err == nil && state == "historical topology relation" {
					err = db.Delete(&relation).Error
				}
			case "shared topology":
				// A retained historical Host can still refer to this topology node.
				err = db.Create(&devicemodel.Device{Fingerprint: "historical-host", NodeID: duplicate.NodeID, DeleteMarker: 1}).Error
			case "cluster topology":
				err = db.Create(&model.Cluster{Name: "referencing-cluster", NodeID: duplicate.NodeID}).Error
			case "topology delete failure":
				err = db.Exec(`CREATE TRIGGER reject_topology_delete BEFORE UPDATE OF deleted_at ON nodes BEGIN SELECT RAISE(ABORT, 'topology delete failed'); END`).Error
			case "write failure":
				err = db.Exec(`CREATE TRIGGER reject_host_migration BEFORE UPDATE OF fingerprint ON devices WHEN OLD.id = 41 BEGIN SELECT RAISE(ABORT, 'migration write failed'); END`).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := repo.MigrateNodeHostFingerprint(t.Context(), node.ID, *node.EdgeID, node.NodeUID); err == nil {
				t.Fatal("conflicting identity was migrated")
			} else if state != "changed node UID" && state != "missing host link" && state != "write failure" && state != "topology delete failure" && !errors.Is(err, errs.ErrConflict) {
				t.Fatalf("expected identity conflict: %v", err)
			}
			var saved devicemodel.Device
			if err := db.First(&saved, host.ID).Error; err != nil || saved.Fingerprint != host.Fingerprint {
				t.Fatalf("failed migration changed original host: %v", err)
			}
			if err := db.First(&devicemodel.Device{}, duplicate.ID).Error; err != nil {
				t.Fatalf("failed migration deleted conflicting host: %v", err)
			}
			if err := db.First(&topologymodel.Node{}, *duplicate.NodeID).Error; err != nil {
				t.Fatalf("failed migration deleted orphan topology: %v", err)
			}
		})
	}
}
