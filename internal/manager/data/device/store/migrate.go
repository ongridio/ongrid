// Package store persists durable Host identity and explicit Edge associations.
package store

import (
	"encoding/json"
	"fmt"
	"strings"

	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate updates schema and initializes current bindings from verified registrations.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Device{}.TableName()) {
		if err := dbx.DropIndexes(
			db,
			&model.Device{},
			"idx_devices_fingerprint",
			"idx_devices_node_id",
		); err != nil {
			return err
		}
	}
	if dbx.NeedsDeleteMarkerMigration(db, model.EdgeDevice{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.EdgeDevice{}, "idx_edge_device_unique"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(
		&model.Device{},
		&model.EdgeDevice{},
		&model.DeviceNetwork{},
		&model.NetworkDiscoveryCandidate{},
		&model.NetworkIdentifier{},
		&model.NetworkInterface{},
		&model.NetworkLink{},
	); err != nil {
		return err
	}
	if err := refreshDeviceRolesConstraint(db); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Device{}.TableName()); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.EdgeDevice{}.TableName()); err != nil {
		return err
	}
	if err := detachKubernetesControllerHosts(db); err != nil {
		return err
	}
	if err := initializeCurrentHostEdges(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&edgemodel.HostPluginConfig{}); err != nil {
		return err
	}
	if err := initializeHostPluginConfigs(db); err != nil {
		return err
	}
	return backfillDeviceEnvironments(db)
}

// refreshDeviceRolesConstraint reapplies the current model constraint for
// existing databases. GORM creates CHECK constraints for new tables,
// but does not alter an existing constraint when the allowed role bits grow.
func refreshDeviceRolesConstraint(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" && db.Dialector.Name() != "mysql" {
		return nil
	}
	const constraintName = "chk_devices_roles"
	if !db.Migrator().HasConstraint(&model.Device{}, constraintName) {
		return nil
	}
	if db.Dialector.Name() == "mysql" {
		var clause string
		if err := db.Raw("SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = ?", constraintName).Scan(&clause).Error; err != nil {
			return fmt.Errorf("inspect device roles constraint: %w", err)
		}
		if strings.Contains(strings.ToUpper(strings.ReplaceAll(clause, "`", "")), "ROLES BETWEEN 0 AND 31") {
			return nil
		}
		// Replace atomically so a failed ALTER keeps the existing constraint.
		if err := db.Exec("ALTER TABLE devices DROP CHECK chk_devices_roles, ADD CONSTRAINT chk_devices_roles CHECK (roles BETWEEN 0 AND 31)").Error; err != nil {
			return fmt.Errorf("update device roles constraint: %w", err)
		}
		return nil
	}
	var createSQL string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = ? AND name = ?", "table", model.Device{}.TableName()).Scan(&createSQL).Error; err != nil {
		return fmt.Errorf("inspect device roles constraint: %w", err)
	}
	if strings.Contains(strings.ToUpper(createSQL), "ROLES BETWEEN 0 AND 31") {
		return nil
	}
	if err := db.Migrator().DropConstraint(&model.Device{}, constraintName); err != nil {
		return fmt.Errorf("drop device roles constraint: %w", err)
	}
	if err := db.Migrator().CreateConstraint(&model.Device{}, constraintName); err != nil {
		return fmt.Errorf("create device roles constraint: %w", err)
	}
	if err := db.AutoMigrate(&model.Device{}); err != nil {
		return fmt.Errorf("restore device indexes: %w", err)
	}
	return nil
}

// detachKubernetesControllerHosts removes host associations accidentally
// created for controller-only edges. A Kubernetes controller is a cluster
// access path, not a physical device; node edges remain untouched.
func detachKubernetesControllerHosts(db *gorm.DB) error {
	controllerEdgeIDs, err := kubernetesControllerEdgeIDs(db)
	if err != nil {
		return err
	}
	if len(controllerEdgeIDs) == 0 || !db.Migrator().HasTable("edges") {
		return nil
	}

	return db.Transaction(func(tx *gorm.DB) error {

		if err := tx.Where("edge_id IN ? AND type = ?", controllerEdgeIDs, model.EdgeDeviceRelationHost).
			Delete(&model.EdgeDevice{}).Error; err != nil {
			return fmt.Errorf("detach kubernetes controller host links: %w", err)
		}
		if err := tx.Table("edges").Where("id IN ?", controllerEdgeIDs).Update("device_id", nil).Error; err != nil {
			return fmt.Errorf("clear kubernetes controller device pointers: %w", err)
		}
		// Detaching an invalid controller association does not authorize Host deletion.
		if err := tx.Model(&model.Device{}).Where("current_edge_id IN ?", controllerEdgeIDs).
			Updates(map[string]any{"current_edge_id": nil, "online": false}).Error; err != nil {
			return fmt.Errorf("clear controller host runtime: %w", err)
		}
		return nil
	})
}

func kubernetesControllerEdgeIDs(db *gorm.DB) ([]uint64, error) {
	const table = "k8s_clusters"
	if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, "controller_edge_id") {
		return nil, nil
	}

	query := db.Table(table).
		Where("controller_edge_id IS NOT NULL AND controller_edge_id <> 0")
	if db.Migrator().HasColumn(table, "delete_marker") {
		query = query.Where("delete_marker = 0")
	} else if db.Migrator().HasColumn(table, "deleted_at") {
		query = query.Where("deleted_at IS NULL")
	}

	var edgeIDs []uint64
	if err := query.Distinct().Pluck("controller_edge_id", &edgeIDs).Error; err != nil {
		return nil, fmt.Errorf("list kubernetes controller edges: %w", err)
	}
	return edgeIDs, nil
}

// initializeCurrentHostEdges only adopts verified existing reverse associations.
func initializeCurrentHostEdges(db *gorm.DB) error {
	if !db.Migrator().HasTable("edges") || !db.Migrator().HasColumn("edges", "last_registered_at") {
		return nil
	}
	return db.Exec(`UPDATE devices SET current_edge_id = (
 SELECT MAX(e.id) FROM edges e JOIN edge_devices l ON l.edge_id=e.id
 WHERE l.device_id=devices.id AND l.type=1 AND l.delete_marker=0
 AND e.device_id=devices.id AND e.delete_marker=0 AND e.last_registered_at IS NOT NULL
 ) WHERE current_edge_id IS NULL AND delete_marker=0`).Error
}

func backfillDeviceEnvironments(db *gorm.DB) error {
	if !db.Migrator().HasTable("edges") || !db.Migrator().HasTable("edge_plugin_configs") {
		return nil
	}
	var rows []struct {
		ID       uint64
		SpecJSON string
	}
	err := db.Table("devices d").Select("d.id, COALESCE(p.spec_json, '') AS spec_json").
		Joins("JOIN edge_devices l ON l.device_id = d.id AND l.type = ? AND l.delete_marker = 0", model.EdgeDeviceRelationHost).
		Joins("JOIN edges e ON e.id = l.edge_id AND e.delete_marker = 0").
		Joins("LEFT JOIN edge_plugin_configs p ON p.edge_id = e.id AND p.plugin_name = ? AND p.delete_marker = 0", "autoapm").
		Where("d.environment IS NULL AND d.delete_marker = 0").
		Order("d.id, (e.status = 'online') DESC, e.last_seen_at DESC, e.id DESC").Scan(&rows).Error
	if err != nil {
		return fmt.Errorf("load legacy device environments: %w", err)
	}
	seen := make(map[uint64]bool)
	for _, row := range rows {
		if seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		var spec struct {
			Environment string `json:"environment"`
		}
		if row.SpecJSON != "" {
			if err := json.Unmarshal([]byte(row.SpecJSON), &spec); err != nil {
				return fmt.Errorf("decode device %d legacy environment: %w", row.ID, err)
			}
		}
		if err := db.Model(&model.Device{}).Where("id = ? AND environment IS NULL", row.ID).Update("environment", spec.Environment).Error; err != nil {
			return fmt.Errorf("import device %d environment: %w", row.ID, err)
		}
	}
	return nil
}
