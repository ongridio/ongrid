// Package store is the data layer for the report sub-domain
// (report_schedules + reports). See HLD-014.
package store

import (
	"gorm.io/gorm"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
)

// Migrate registers the report tables with gorm AutoMigrate. AutoMigrate
// adds new columns/indexes but never drops or narrows existing ones, so
// re-running on every boot is safe. Wired into the manager startup
// migration list in cmd/ongrid/main.go.
func Migrate(db *gorm.DB) error {
	if dbx.NeedsDeleteMarkerMigration(db, model.Report{}.TableName()) {
		if err := dbx.DropIndexes(
			db,
			&model.Report{},
			"uniq_report_sched_period",
			"idx_report_share",
		); err != nil {
			return err
		}
	}
	if err := db.AutoMigrate(
		&model.ReportSchedule{},
		&model.Report{},
	); err != nil {
		return err
	}
	return dbx.BackfillDeleteMarkerWithValue(db, model.Report{}.TableName(), "1")
}
