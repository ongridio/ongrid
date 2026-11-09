// Package store implements CMDB persistence for CI instances, their
// relations, their history, and the EAV value tables.
//
// Named `store` rather than `sqlite` because, like every other data package
// in this repo, it is dialect-agnostic: AutoMigrate picks
// BIGINT UNSIGNED AUTO_INCREMENT on MySQL and INTEGER AUTOINCREMENT on SQLite.
package store

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	valuemodel "github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// Migrate registers the CI-side schema: the CI row, the relation edge, the
// history tables, and the EAV value/choice tables.
//
// Ordering matters only for readability — AutoMigrate is per-table and
// CMDB declares no database-level foreign keys (see MigrateCI for why), so
// there is no dependency to satisfy.
//
// The three legacy value tables (c_value_integers, c_value_floats,
// c_value_datetime) are intentionally NOT migrated. cantor's own model marks
// them deprecated and its TableMap can never select them, so creating them in
// a fresh database would add tables nothing writes to. They are likewise left
// alone on an adopted cantor database — AutoMigrate never drops tables.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errNilDB
	}
	if err := db.AutoMigrate(
		// Instance side.
		&cimodel.CI{},
		&cimodel.Relation{},
		// History.
		&cimodel.OperationRecord{},
		&cimodel.AttributeHistory{},
		&cimodel.RelationHistory{},
		&cimodel.TypeHistory{},
		&cimodel.TriggerHistory{},
		// The data centre's operation log. Append-only, like the rest of the
		// history tables — see the model.
		&cimodel.DCIMOperationHistory{},
		// IP address management: the scan configuration and the two logs.
		&cimodel.IPAMSubnetScan{},
		&cimodel.IPAMSubnetScanHistory{},
		&cimodel.IPAMOperationHistory{},
		// EAV value tables — the six cantor ever writes.
		&valuemodel.ValueIndexInt{},
		&valuemodel.ValueIndexFloat{},
		&valuemodel.ValueIndexText{},
		&valuemodel.ValueIndexDateTime{},
		&valuemodel.ValueText{},
		&valuemodel.ValueJSON{},
		// Choice tables.
		&valuemodel.ChoiceInt{},
		&valuemodel.ChoiceFloat{},
		&valuemodel.ChoiceText{},
	); err != nil {
		return err
	}
	return ensureIndexedTextCollation(db)
}

// ensureIndexedTextCollation re-creates the indexed-text value index on SQLite
// with a NOCASE collation on the value column.
//
// This is a deliberate, measured fix rather than a stylistic choice. SQLite's
// LIKE is case-insensitive by default, and the query planner cannot use a
// BINARY-collated index to serve it, so a wildcard search over
// c_value_index_texts degrades to a full scan. Measured on 200k rows with
// hilbert's own driver (SQLite 3.50.4 via glebarez/sqlite v1.11.0):
//
//	LIKE 'web-017777'  on a BINARY index   36.46 ms   (scan)
//	LIKE 'web-0177%'   on a BINARY index   36.46 ms   (scan)
//	LIKE 'web-017777'  on a NOCASE index   42.29 us   (index)
//	LIKE 'web-0177%'   on a NOCASE index   53.99 us   (index)
//
// That is an ~860x regression on every wildcard term, which the CMDB query DSL
// emits for `attr:value*`. It would be silent: the results are correct.
//
// Why explicit DDL rather than a struct tag: GORM's `collate:nocase` tag is
// silently ignored on BOTH dialects (verified — SQLite emits no COLLATE clause
// and MySQL emits a plain varchar(128)), so a tag would look like a fix while
// doing nothing.
//
// Why index-level rather than column-level: an index-level collation needs no
// change to the column definition, so it cannot disturb anything already
// stored. The trade-off is that `=` remains case-SENSITIVE on SQLite (only
// LIKE becomes case-insensitive), whereas MySQL's table collation makes both
// case-insensitive. That divergence is recorded in the plan and must be
// resolved when the query compiler is written; it does not affect this index.
//
// MySQL needs no equivalent: its utf8mb4_0900_ai_ci table collation already
// makes LIKE case-insensitive, and the same benchmark showed MySQL serving
// LIKE prefix terms from a range scan on the existing index.
//
// It is idempotent: an index whose definition already carries NOCASE is left
// alone, so a second boot writes nothing.
func ensureIndexedTextCollation(db *gorm.DB) error {
	if db.Dialector.Name() != "sqlite" {
		return nil
	}
	const indexName = "text_attr_value_index"
	table := valuemodel.ValueIndexText{}.TableName()

	var existing string
	if err := db.Raw(
		"SELECT COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name = ?",
		indexName,
	).Scan(&existing).Error; err != nil {
		return fmt.Errorf("cmdb ci store: inspect %s: %w", indexName, err)
	}
	if strings.Contains(strings.ToUpper(existing), "NOCASE") {
		return nil
	}

	// DROP + CREATE rather than CREATE ... IF NOT EXISTS: AutoMigrate has
	// already created the BINARY version, and rebuilding is the only way to
	// change an index's collation.
	if err := db.Exec("DROP INDEX IF EXISTS " + indexName).Error; err != nil {
		return fmt.Errorf("cmdb ci store: drop %s: %w", indexName, err)
	}
	if err := db.Exec(fmt.Sprintf(
		"CREATE INDEX %s ON %s(attr_id, value COLLATE NOCASE)", indexName, table,
	)).Error; err != nil {
		return fmt.Errorf("cmdb ci store: recreate %s with NOCASE: %w", indexName, err)
	}
	return nil
}
