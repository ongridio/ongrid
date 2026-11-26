// Package model holds the CMDB persisted entities.
//
// The entities mirror nexus-cantor's schema table-for-table, including the
// c_ prefix and — importantly — cantor's index names. Reusing cantor's index
// names is not cosmetic: the CMDB tables may be pointed at an existing cantor
// database, and AutoMigrate only adds missing objects. Declaring a differently
// named index over the same columns would leave the database with two
// equivalent indexes, doubling write cost for no benefit.
package model

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

// Base is the CMDB soft-delete + timestamp convention, shared by every CMDB
// entity that cantor declared with its `Model` base class (id, created_at,
// updated_at, deleted_at, deleted).
//
// Two deliberate divergences from cantor:
//
//   - Soft deletion uses Ongrid's delete_marker + deleted_at pair instead of
//     cantor's `deleted` boolean, matching internal/manager/model/topology.
//     Adopting a cantor database therefore needs a one-time backfill of
//     delete_marker and a normalisation of `deleted = 1` into deleted_at;
//     see internal/cmdb/data.
//   - cantor's `deleted` column is left unmapped. GORM never drops columns, so
//     on an adopted database it survives as an inert orphan. Mapping it would
//     create a second, disagreeing source of truth for "is this row live".
//
// The `deleted_at` index name matches what GORM generates for an embedded
// field so a fresh database and an adopted one converge on the same shape.
type Base struct {
	ID           ID                    `gorm:"primaryKey;autoIncrement"`
	CreatedAt    time.Time             `gorm:"column:created_at"`
	UpdatedAt    time.Time             `gorm:"column:updated_at"`
	DeletedAt    *time.Time            `gorm:"column:deleted_at;index"`
	DeleteMarker soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt"`
}

// AppendOnlyBase is for entities cantor declared with its `Model2` base class:
// id + an indexed created_at, and NO soft delete. Operation records are the
// only such entities, and they must stay immutable — an audit row that could
// be soft-deleted is not an audit row.
type AppendOnlyBase struct {
	ID        ID        `gorm:"primaryKey;autoIncrement"`
	CreatedAt time.Time `gorm:"column:created_at;index"`
}
