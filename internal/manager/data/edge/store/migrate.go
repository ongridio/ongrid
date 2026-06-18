package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate registers the manager/edge model with gorm's AutoMigrate. It is
// dialect-agnostic and suitable for both MySQL and SQLite; cmd/ongrid wires
// it through dbx.RunMigrations at startup.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Edge{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.Edge{}, "idx_edges_access_key_id"); err != nil {
			return err
		}
	}
	if dbx.NeedsDeleteMarkerMigration(db, model.PluginConfig{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.PluginConfig{}, "uk_edge_plugin"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&model.Edge{}, &model.PluginConfig{}); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Edge{}.TableName()); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarker(db, model.PluginConfig{}.TableName())
}
