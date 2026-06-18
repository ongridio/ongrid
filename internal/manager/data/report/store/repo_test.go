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
