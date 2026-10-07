// Package store is the data layer for the report sub-domain
// (report_schedules + reports). See HLD-014.
package store

import (
	"log/slog"
	"time"

	"gorm.io/gorm"

	biz "github.com/ongridio/ongrid/internal/manager/biz/report"
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
		&model.Task{}, // HLD-022 Phase 2: unified task spine (oneoff rows)
	); err != nil {
		return err
	}
	if err := dbx.BackfillDeleteMarkerWithValue(db, model.Report{}.TableName(), "1"); err != nil {
		return err
	}
	// HLD-022 backfill: stamp the owning-task back-ref on existing scheduled
	// reports. Idempotent (only fills empty task_id), additive (a new column),
	// safe to re-run every boot.
	if err := db.Exec(
		"UPDATE reports SET task_id = CONCAT('report-schedule:', schedule_id) WHERE schedule_id IS NOT NULL AND (task_id IS NULL OR task_id = '')",
	).Error; err != nil {
		return err
	}
	return backfillNextFireUTC(db, time.Now().UTC())
}

// backfillNextFireUTC re-arms next_fire_at for schedules written before the
// column carried a UTC convention.
//
// Under SQLite a time.Time is stored as ISO-8601 text *including its
// offset*, and DueSchedules' `next_fire_at <= now` degrades to a lexical
// compare: a row persisted as '…17:40:00+08:00' never sorts below a UTC
// now, so the schedule is never selected, never reaches FireSchedule, and
// so can never re-arm itself out of the broken state. (MySQL stores
// DATETIME(3) and compares numerically, so rows there were never stuck —
// re-arming them is a no-op that keeps the two backends on one path.)
//
// next_fire_at is recomputed from cron_spec and the schedule's IANA
// timezone rather than converted from the stored value, so the result
// does not depend on which of the two on-disk formats a row happens to
// carry. Rows the database already sees as due are skipped: those fire on
// the next tick and FireSchedule normalizes them, whereas re-arming would
// push them past a pending fire. That skip is also what makes this cheap
// to re-run on every boot — a healthy future row recomputes to the value
// it already holds.
func backfillNextFireUTC(db *gorm.DB, now time.Time) error {
	var rows []*model.ReportSchedule
	if err := db.
		Where("enabled = ? AND (next_fire_at IS NULL OR next_fire_at > ?)", true, now).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, s := range rows {
		loc, err := biz.LoadLocation(s.Timezone)
		if err != nil {
			// Unresolvable tz (no system tzdata, or a bad value): leave the
			// row untouched rather than re-arm it against the wrong zone.
			slog.Warn("report schedule next_fire_at backfill skipped: bad timezone",
				"schedule_id", s.ID, "timezone", s.Timezone, "err", err)
			continue
		}
		next, err := biz.CronNext(s.CronSpec, loc, now)
		if err != nil {
			slog.Warn("report schedule next_fire_at backfill skipped: bad cron spec",
				"schedule_id", s.ID, "cron_spec", s.CronSpec, "err", err)
			continue
		}
		// UpdateColumn, not Update: re-arming is migration bookkeeping and
		// must not bump updated_at as though an operator had edited the
		// schedule.
		if err := db.Model(&model.ReportSchedule{}).
			Where("id = ?", s.ID).
			UpdateColumn("next_fire_at", next.UTC()).Error; err != nil {
			return err
		}
	}
	return nil
}
