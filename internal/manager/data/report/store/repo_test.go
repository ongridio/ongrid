package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func newReportTestRepo(t *testing.T) *Repo {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open sqlite :memory:: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return NewRepo(db)
}

func sampleReport(id string, scheduleID uint64, periodStart time.Time) *model.Report {
	return &model.Report{
		ID:           id,
		ScheduleID:   &scheduleID,
		CreatedBy:    1,
		Title:        "daily report",
		Kind:         model.KindDaily,
		PeriodStart:  periodStart,
		PeriodEnd:    periodStart.Add(24 * time.Hour),
		Timezone:     "UTC",
		ScopeJSON:    `{}`,
		Status:       model.StatusReady,
		ErrorMsg:     "",
		ContentJSON:  `{}`,
		ContentMD:    "# report",
		DeliveryJSON: `[]`,
	}
}

// TestDueSchedules_UTCStoredIsDue is the regression for the non-UTC
// timezone firing bug. next_fire_at is persisted in UTC (the schedule
// timezone is Asia/Shanghai here, so 17:40 CST = 09:40 UTC). DueSchedules
// compares next_fire_at <= now against a UTC clock, so only a UTC-stored
// value sorts correctly; a +08:00-stored value would compare as
// "17:40..." > "09:4x..." and never be selected.
func TestDueSchedules_UTCStoredIsDue(t *testing.T) {
	repo := newReportTestRepo(t)
	ctx := context.Background()

	// 2026-06-08 17:40 Asia/Shanghai == 09:40 UTC. Store the UTC instant.
	due := time.Date(2026, 6, 8, 9, 40, 0, 0, time.UTC)
	future := due.Add(24 * time.Hour)

	if err := repo.CreateSchedule(ctx, &model.ReportSchedule{
		ID:         1,
		CreatedBy:  1,
		Name:       "due-utc",
		Kind:       model.KindCustom,
		CronSpec:   "40 17 * * 3",
		Timezone:   "Asia/Shanghai",
		ScopeJSON:  `{}`,
		Enabled:    true,
		NextFireAt: &due,
	}); err != nil {
		t.Fatalf("create due schedule: %v", err)
	}
	if err := repo.CreateSchedule(ctx, &model.ReportSchedule{
		ID:         2,
		CreatedBy:  1,
		Name:       "future",
		Kind:       model.KindCustom,
		CronSpec:   "40 17 * * 3",
		Timezone:   "Asia/Shanghai",
		ScopeJSON:  `{}`,
		Enabled:    true,
		NextFireAt: &future,
	}); err != nil {
		t.Fatalf("create future schedule: %v", err)
	}

	// Scheduler clock runs UTC; at 10:00 UTC the 09:40 UTC (17:40 CST)
	// schedule is due, the next-day one is not.
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	rows, err := repo.DueSchedules(ctx, now)
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("DueSchedules returned %d rows, want 1", len(rows))
	}
	if rows[0].ID != 1 {
		t.Errorf("due schedule id = %d, want 1", rows[0].ID)
	}
}

func TestReportSoftDeleteAllowsSchedulePeriodReuse(t *testing.T) {
	repo := newReportTestRepo(t)
	ctx := context.Background()
	periodStart := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)

	if err := repo.CreateReport(ctx, sampleReport("report-a", 42, periodStart)); err != nil {
		t.Fatalf("first CreateReport: %v", err)
	}
	err := repo.CreateReport(ctx, sampleReport("report-b", 42, periodStart))
	if !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("active duplicate CreateReport err = %v, want ErrConflict", err)
	}
	if err := repo.DeleteReport(ctx, "report-a"); err != nil {
		t.Fatalf("DeleteReport: %v", err)
	}
	if err := repo.CreateReport(ctx, sampleReport("report-c", 42, periodStart)); err != nil {
		t.Fatalf("recreate after soft delete: %v", err)
	}
	got, err := repo.GetReport(ctx, "report-c")
	if err != nil {
		t.Fatalf("GetReport recreated: %v", err)
	}
	if got.ID != "report-c" {
		t.Fatalf("GetReport id = %q, want report-c", got.ID)
	}
}
