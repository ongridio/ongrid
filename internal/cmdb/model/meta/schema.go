// Package meta holds CMDB schema-provenance state. It is the only CMDB
// model that is not a port of a cantor table.
package meta

import (
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// Generation identifies which CMDB schema lineage a database holds.
type Generation string

const (
	// GenerationFresh means the c_* tables were created by this codebase
	// (GORM AutoMigrate on an empty database).
	GenerationFresh Generation = "fresh"
	// GenerationAdoptedCantor means the c_* tables already existed —
	// they came from a nexus-cantor installation and were adopted in
	// place rather than recreated. Adoption requires the extra
	// compatibility steps in the data layer (delete_marker backfill and
	// the `deleted` -> `deleted_at` normalisation), so the distinction
	// must be recorded rather than inferred after the fact.
	GenerationAdoptedCantor Generation = "adopted-cantor"
)

// SchemaMeta records one applied CMDB schema generation.
//
// The CMDB domain is the only Ongrid bounded context whose tables carry a
// `c_` prefix and which may be pointed at a database that predates it.
// nexus-cantor versioned its schema with Alembic; Ongrid has no versioned
// migration runner (see .go-arch-lint.yml / dbx.RunMigrations), so this
// table is the minimum needed to answer "which lineage is this database in?"
// when an operator reports a problem.
//
// Rows are append-only: each boot that changes the generation writes a new
// row. Readers use the newest row by AppliedAt.
type SchemaMeta struct {
	ID         cmdbmodel.ID `gorm:"primaryKey;autoIncrement" json:"id"`
	Generation Generation   `gorm:"size:32;not null;index:idx_c_schema_meta_applied"                      json:"generation"`
	Origin     string       `gorm:"size:64"                                                               json:"origin,omitempty"`
	AppliedAt  time.Time    `gorm:"not null;index:idx_c_schema_meta_applied,sort:desc"                    json:"applied_at"`
}

// TableName pins the table name. The c_ prefix is not cosmetic: Ongrid
// already owns unprefixed `relations` and `relation_types` (manager/topology),
// which collide with nexus-cantor's c_ci_relations / c_relation_types if the
// prefix is dropped. Keeping the prefix also leaves the door open to adopting
// an existing cantor database without a rename.
func (SchemaMeta) TableName() string { return "c_schema_meta" }
