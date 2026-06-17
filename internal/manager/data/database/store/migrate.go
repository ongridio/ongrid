package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/database"
)

// Migrate registers the database instance model with gorm's AutoMigrate.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&model.DatabaseInstance{})
}
