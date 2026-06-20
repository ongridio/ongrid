// Package store is the GORM-backed implementation of the topology
// repos. Works against MySQL and SQLite via the dialect-agnostic GORM
// surface.
package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate registers nodes / relations / relation_types with GORM
// AutoMigrate, seeds the six built-in RelationType rows, and backfills
// any device rows that don't yet point at a node. All
// steps are idempotent; second boot is a no-op past AutoMigrate.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Relation{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.Relation{}, "idx_relations_src_dst_type"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&model.Node{}, &model.Relation{}, &model.RelationType{}, &model.NodeType{}); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Relation{}.TableName()); err != nil {
		return err
	}
	if err := seedBuiltinRelationTypes(db); err != nil {
		return err
	}
	if err := seedBuiltinNodeTypes(db); err != nil {
		return err
	}
	return backfillDeviceNodes(db)
}

func seedBuiltinNodeTypes(db *gorm.DB) error {
	seeds := model.BuiltinNodeTypes()
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"display_name", "display_name_en", "builtin", "tier", "description", "updated_at",
		}),
	}).Create(&seeds).Error
}

// backfillDeviceNodes walks every device row whose `node_id` is NULL
// and creates a paired Node(type='device', name=device.name) row,
// then writes the new node id back to devices.node_id. Idempotent:
// on the next boot every device already has node_id set so the SELECT
// returns no rows.
//
// We intentionally inspect / mutate the `devices` table directly via
// raw SQL rather than importing the device model package — keeps the
// topology data layer free of cross-domain imports. The schema is
// known and stable (id BIGINT, name VARCHAR, node_id BIGINT nullable);
// any schema change there should update this query too.
//
// Cleanly skips when the `devices` table doesn't exist yet (fresh DB
// before device migration has ever run). The cmd/ongrid migration
// order puts device.Migrate before topology.Migrate so on a real
// boot we always see the table.
func backfillDeviceNodes(db *gorm.DB) error {
	if !db.Migrator().HasTable("devices") {
		return nil
	}
	type devRow struct {
		ID   uint64
		Name string
	}
	var rows []devRow
	// Soft-deleted devices are skipped (deleted_at IS NULL). Name
	// can be empty when an edge registered but never sent host_info;
	// we fall back to "device-<id>" so node.name has a usable display.
	if err := db.Raw("SELECT id, name FROM devices WHERE node_id IS NULL AND deleted_at IS NULL").Scan(&rows).Error; err != nil {
		return err
	}
	for _, d := range rows {
		name := d.Name
		if name == "" {
			name = fmt.Sprintf("device-%d", d.ID)
		}
		n := &model.Node{Type: string(model.NodeTypeDevice), Name: name}
		if err := db.Create(n).Error; err != nil {
			return fmt.Errorf("backfill node for device %d: %w", d.ID, err)
		}
		if err := db.Exec("UPDATE devices SET node_id = ? WHERE id = ?", n.ID, d.ID).Error; err != nil {
			return fmt.Errorf("backfill device.node_id for %d: %w", d.ID, err)
		}
	}
	return nil
}

func seedBuiltinRelationTypes(db *gorm.DB) error {
	seeds := model.BuiltinRelationTypes()
	// Upsert by primary key (name). On conflict we refresh
	// display_name(_en) + description + the three semantic fields —
	// these MUST track the in-code definition so a future
	// amendment (say, flipping `monitors` to propagating, or seeding
	// a missing display_name_en) lands on every node without a
	// separate migration.
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"display_name", "display_name_en", "builtin", "propagates_failure",
			"direction", "semantics_tag", "description", "updated_at",
		}),
	}).Create(&seeds).Error
}
