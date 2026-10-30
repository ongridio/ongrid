package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// MigrateNodeHostFingerprint preserves a legacy node's Host when it first reports its UID.
func (r *Repo) MigrateNodeHostFingerprint(ctx context.Context, nodeID, edgeID uint64, nodeUID string) error {
	if nodeID == 0 || edgeID == 0 || nodeUID == "" || strings.HasPrefix(nodeUID, "name:") {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var node model.Node
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND edge_id = ? AND node_uid = ?", nodeID, edgeID, nodeUID).First(&node).Error; err != nil {
			return err
		}
		if node.DeviceID == nil {
			return nil
		}
		var edge edgemodel.Edge
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&edge, edgeID).Error; err != nil {
			return err
		}
		if edge.DeviceID == nil || *edge.DeviceID != *node.DeviceID {
			return fmt.Errorf("%w: node and edge host bindings differ", errs.ErrConflict)
		}
		var host devicemodel.Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&host, *node.DeviceID).Error; err != nil {
			return err
		}
		if host.CurrentEdgeID == nil || *host.CurrentEdgeID != edgeID {
			return fmt.Errorf("%w: node edge is not the current host installation", errs.ErrConflict)
		}
		var link devicemodel.EdgeDevice
		if err := tx.Where("edge_id = ? AND device_id = ? AND type = ?", edgeID, host.ID, devicemodel.EdgeDeviceRelationHost).First(&link).Error; err != nil {
			return err
		}
		legacy := nodeHostFingerprint(node.ClusterID, "name:"+node.NodeName)
		if host.Fingerprint != legacy {
			return nil
		}
		fingerprint := nodeHostFingerprint(node.ClusterID, nodeUID)
		var duplicate devicemodel.Device
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("fingerprint = ?", fingerprint).First(&duplicate).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			// Earlier failed registrations can leave an unused row; never merge used Hosts.
			if duplicate.CurrentEdgeID != nil || duplicate.NodeID != nil || duplicate.Hostname != node.NodeName ||
				duplicate.Name != node.NodeName || duplicate.Description != "" || duplicate.Environment != nil || duplicate.Roles != 0 ||
				!sameUserID(duplicate.UserID, host.UserID) {
				return fmt.Errorf("%w: node UID fingerprint belongs to another host", errs.ErrConflict)
			}
			for _, table := range []string{"edges", "edge_devices", "k8s_nodes", "host_plugin_configs"} {
				var count int64
				if err := tx.Table(table).Where("device_id = ?", duplicate.ID).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("%w: node UID fingerprint host has %s references", errs.ErrConflict, table)
				}
			}
			if err := tx.Delete(&duplicate).Error; err != nil {
				return err
			}
		}
		return tx.Model(&host).Update("fingerprint", fingerprint).Error
	})
}

func nodeHostFingerprint(clusterID uint64, identity string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("hw:k8s-node:%d:%s", clusterID, identity)))
	return fmt.Sprintf("fp_%x", hash[:16])
}

func sameUserID(a, b *uint64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
