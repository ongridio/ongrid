// Package store implements the cmdb/biz/meta persistence port on GORM.
//
// The package is named `store` rather than `sqlite` because, like every
// other data package in this repo, it is dialect-agnostic: AutoMigrate
// picks BIGINT UNSIGNED AUTO_INCREMENT on MySQL and INTEGER AUTOINCREMENT
// on SQLite.
package store

import (
	"gorm.io/gorm"

	"github.com/ongridio/ongrid/internal/cmdb/model/meta"
)

// Migrate registers the CMDB schema-provenance table.
//
// It must run first among the CMDB migrators: the adoption path uses this
// table to decide whether the cantor compatibility steps are needed.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&meta.SchemaMeta{})
}
