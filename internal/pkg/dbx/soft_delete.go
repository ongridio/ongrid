package dbx

import (
	"fmt"

	"gorm.io/gorm"
)

// DropIndexes drops named indexes when they already exist. It is intended for
// AutoMigrate-era index shape changes where GORM will not rewrite an existing
// index with the same name.
func DropIndexes(db *gorm.DB, model any, names ...string) error {
	if db == nil {
		return fmt.Errorf("dbx.DropIndexes: nil db")
	}
	if model == nil {
		return fmt.Errorf("dbx.DropIndexes: nil model")
	}
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("dbx.DropIndexes: empty index name")
		}
		if db.Migrator().HasIndex(model, name) {
			if err := db.Migrator().DropIndex(model, name); err != nil {
				return fmt.Errorf("dbx.DropIndexes: drop %s: %w", name, err)
			}
		}
	}
	return nil
}

// NeedsDeleteMarkerMigration reports whether a legacy table still needs the
// one-time index rewrite that introduces delete_marker into unique keys.
func NeedsDeleteMarkerMigration(db *gorm.DB, table string) bool {
	if db == nil || !db.Migrator().HasTable(table) {
		return false
	}
	return !db.Migrator().HasColumn(table, "delete_marker")
}

// BackfillDeleteMarker moves legacy soft-deleted rows out of the active
// delete_marker=0 slot. New deletes are handled by gorm.io/plugin/soft_delete;
// this function only protects rows created before delete_marker existed.
func BackfillDeleteMarker(db *gorm.DB, table string) error {
	return BackfillDeleteMarkerWithValue(db, table, "id")
}

// BackfillDeleteMarkerWithValue is BackfillDeleteMarker with an explicit SQL
// value expression. Use "id" for numeric primary-key tables and "1" for tables
// where legacy uniqueness already guarantees at most one deleted row per key.
func BackfillDeleteMarkerWithValue(db *gorm.DB, table, valueExpr string) error {
	if db == nil {
		return fmt.Errorf("dbx.BackfillDeleteMarker: nil db")
	}
	if !db.Migrator().HasTable(table) {
		return nil
	}
	if !db.Migrator().HasColumn(table, "delete_marker") || !db.Migrator().HasColumn(table, "deleted_at") {
		return nil
	}
	quotedTable, err := quoteIdentifier(table)
	if err != nil {
		return err
	}
	expr, err := quoteValueExpr(valueExpr)
	if err != nil {
		return err
	}
	sql := fmt.Sprintf(
		"UPDATE %s SET `delete_marker` = %s WHERE `deleted_at` IS NOT NULL AND `delete_marker` = 0",
		quotedTable,
		expr,
	)
	if err := db.Exec(sql).Error; err != nil {
		return fmt.Errorf("dbx.BackfillDeleteMarker: %s: %w", table, err)
	}
	return nil
}

func quoteIdentifier(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("dbx: empty identifier")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return "", fmt.Errorf("dbx: unsafe identifier %q", name)
	}
	return "`" + name + "`", nil
}

func quoteValueExpr(expr string) (string, error) {
	switch expr {
	case "1":
		return "1", nil
	case "id":
		return "`id`", nil
	default:
		return "", fmt.Errorf("dbx: unsafe delete marker expression %q", expr)
	}
}
