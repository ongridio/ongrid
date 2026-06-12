package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
)

// Migrate registers the flow tables with gorm AutoMigrate. AutoMigrate
// is additive-only (columns/indexes), same caveats as the sibling
// domains.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&model.Flow{},
		&model.FlowRun{},
		&model.FlowRunNode{},
	)
}
