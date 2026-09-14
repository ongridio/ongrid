//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	store "github.com/ongridio/ongrid/internal/manager/data/report/store"
	model "github.com/ongridio/ongrid/internal/manager/model/report"
	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

// TestReportScheduleNextFireMySQLRoundTrip pins the MySQL half of the
// next_fire_at storage contract (see dbx.openMySQL).
//
// The SQLite regressions in internal/manager/data/report/store cannot speak
// for MySQL: DATETIME carries no offset and compares numerically, while
// go-sql-driver converts every time.Time through the DSN's `loc` on the way
// in and back out. This test therefore runs the whole thing with the
// process pinned to Asia/Shanghai — the case a UTC-only test suite would
// never catch — and asserts that instants survive the round trip and that
// DueSchedules still selects on the right side of the boundary.
func TestReportScheduleNextFireMySQLRoundTrip(t *testing.T) {
	// The DSN's loc=Local is resolved against time.Local when the driver
	// parses it, so this must be set before the connection is opened.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation Asia/Shanghai: %v", err)
	}
	original := time.Local
	time.Local = shanghai
	t.Cleanup(func() { time.Local = original })

	db, err := gorm.Open(gormmysql.Open(testenv.SharedMySQLDSN(t)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open mysql: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	// The container's schema is shared across e2e tests.
	if err := db.Exec("DELETE FROM report_schedules").Error; err != nil {
		t.Fatalf("clear report_schedules: %v", err)
	}

	repo := store.NewRepo(db)
	ctx := context.Background()

	// 17:40 Asia/Shanghai == 09:40 UTC, written as the fixed write path
	// writes it: a UTC instant, from a process that is not in UTC.
	due := time.Date(2026, 6, 8, 9, 40, 0, 0, time.UTC)
	future := due.Add(24 * time.Hour)
	for id, next := range map[uint64]time.Time{1: due, 2: future} {
		if err := repo.CreateSchedule(ctx, &model.ReportSchedule{
			ID:         id,
			CreatedBy:  1,
			Name:       "mysql-roundtrip",
			Kind:       model.KindCustom,
			CronSpec:   "40 17 * * *",
			Timezone:   "Asia/Shanghai",
			ScopeJSON:  `{}`,
			Enabled:    true,
			NextFireAt: &next,
		}); err != nil {
			t.Fatalf("create schedule %d: %v", id, err)
		}
	}

	// MySQL DATETIME stores driver-local wall clock, so the value read back
	// is tagged Asia/Shanghai rather than UTC — the contract is that the
	// *instant* is preserved, not the zone.
	got, err := repo.GetSchedule(ctx, 1)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(due) {
		t.Fatalf("next_fire_at round-tripped as %v, want the instant %v", got.NextFireAt, due)
	}

	// The scheduler ticks on a UTC clock while the driver binds through
	// Local; the 09:40 UTC schedule is due at 10:00 UTC, the next day's is
	// not.
	rows, err := repo.DueSchedules(ctx, time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != 1 {
		t.Fatalf("DueSchedules returned %v, want only schedule 1", rows)
	}

	// Nothing is due an hour before that.
	none, err := repo.DueSchedules(ctx, time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DueSchedules (before): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("DueSchedules before the fire time returned %v, want none", none)
	}
}
