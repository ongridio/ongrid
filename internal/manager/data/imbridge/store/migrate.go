package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate creates the IM bridge tables — im_apps for platform bot
// credentials, im_threads for the IM-conversation → ongrid-session
// mapping. cmd/ongrid wires this through dbx.RunMigrations at boot.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.ImApp{}.TableName()) {
		if err := dbx.DropIndexes(db, &model.ImApp{}, "uk_provider_app_id"); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(&model.ImApp{}, &model.ImThread{}); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarker(db, model.ImApp{}.TableName())
}
