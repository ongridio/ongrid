package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate creates and updates the log backend control-plane tables at startup.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Backend{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.Backend{}, "uk_log_backend_name"); err != nil {
			return err
		}
	}
	if dbx.NeedsDeleteMarkerMigration(db, model.BackendAssignment{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.BackendAssignment{}, "uk_log_backend_edge"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&model.Backend{}, &model.BackendAssignment{}); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Backend{}.TableName()); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarker(db, model.BackendAssignment{}.TableName())
}
