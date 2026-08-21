package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate creates the additive log backend control-plane tables for local and
// fresh installations. Production MySQL upgrades use db/migrations as well.
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
	// Selection is the only persisted backend state. Unknown values are not
	// interpreted as lifecycle states; they are simply not selected.
	if err := db.Model(&model.Backend{}).
		Where("status NOT IN ?", []model.BackendStatus{model.BackendStatusSelected, model.BackendStatusUnselected}).
		Update("status", model.BackendStatusUnselected).Error; err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarker(db, model.Backend{}.TableName()); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarker(db, model.BackendAssignment{}.TableName())
}
