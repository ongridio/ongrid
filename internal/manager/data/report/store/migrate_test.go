package store

import (
	"context"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// legacySchedule mimics a row written before next_fire_at carried a UTC
// convention: the store persists whatever zone the caller hands it, so a
// non-UTC time.Time lands in SQLite as '…+08:00' text.
func legacySchedule(t *testing.T, id uint64, name string, enabled bool, nextFire time.Time) *model.ReportSchedule {
	t.Helper()
	return &model.ReportSchedule{
		ID:         id,
		CreatedBy:  1,
		Name:       name,
		Kind:       model.KindCustom,
		CronSpec:   "40 17 * * *",
		Timezone:   "Asia/Shanghai",
		ScopeJSON:  `{}`,
		Enabled:    enabled,
		NextFireAt: &nextFire,
	}
}

// TestBackfillNextFireUTC_UnsticksLegacyOffsetRow covers the upgrade path
// the UTC convention alone does not fix: a schedule created before the
// change still holds a schedule-timezone next_fire_at, which DueSchedules'
// lexical compare never selects, so the row cannot re-arm itself.
func TestBackfillNextFireUTC_UnsticksLegacyOffsetRow(t *testing.T) {
	repo := newReportTestRepo(t)
	ctx := context.Background()

	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation Asia/Shanghai: %v", err)
	}
	// 17:40 CST == 09:40 UTC. Stored in the schedule timezone, as the
	// pre-fix write path did.
	legacy := time.Date(2026, 6, 8, 17, 40, 0, 0, shanghai)
	if err := repo.CreateSchedule(ctx, legacySchedule(t, 1, "legacy-cst", true, legacy)); err != nil {
		t.Fatalf("create legacy schedule: %v", err)
	}

	// The instant has passed, yet the row is invisible to the scheduler —
	// '2026-06-08 17:40:00+08:00' does not sort below a UTC now.
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	stuck, err := repo.DueSchedules(ctx, now)
	if err != nil {
		t.Fatalf("DueSchedules before backfill: %v", err)
	}
	if len(stuck) != 0 {
		t.Fatalf("DueSchedules before backfill returned %d rows, want 0 (stuck row precondition)", len(stuck))
	}

	if err := backfillNextFireUTC(repo.db, now); err != nil {
		t.Fatalf("backfillNextFireUTC: %v", err)
	}

	// Re-armed from cron_spec + Asia/Shanghai: the next 17:40 CST after
	// 10:00 UTC is 2026-06-09 17:40 CST == 09:40 UTC.
	want := time.Date(2026, 6, 9, 9, 40, 0, 0, time.UTC)
	got, err := repo.GetSchedule(ctx, 1)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.NextFireAt == nil || !got.NextFireAt.Equal(want) {
		t.Fatalf("next_fire_at = %v, want %v", got.NextFireAt, want)
	}

	// The point of the re-arm: the database can now select the row.
	due, err := repo.DueSchedules(ctx, want.Add(time.Minute))
	if err != nil {
		t.Fatalf("DueSchedules after backfill: %v", err)
	}
	if len(due) != 1 || due[0].ID != 1 {
		t.Fatalf("DueSchedules after backfill returned %v, want the re-armed schedule 1", due)
	}
}

// TestBackfillNextFireUTC_SkipsDueAndDisabled guards the every-boot cost of
// the backfill: a schedule the database already sees as due must keep its
// pending fire, and a disabled schedule must not be re-armed behind the
// operator's back.
func TestBackfillNextFireUTC_SkipsDueAndDisabled(t *testing.T) {
	repo := newReportTestRepo(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	pendingFire := now.Add(-30 * time.Second)
	disabledFire := now.Add(72 * time.Hour)

	if err := repo.CreateSchedule(ctx, legacySchedule(t, 1, "due-now", true, pendingFire)); err != nil {
		t.Fatalf("create due schedule: %v", err)
	}
	if err := repo.CreateSchedule(ctx, legacySchedule(t, 2, "disabled", false, disabledFire)); err != nil {
		t.Fatalf("create disabled schedule: %v", err)
	}
	// The Enabled column carries a `default:true` tag, so GORM substitutes
	// the default for a zero-value bool on insert — disable it explicitly.
	if err := repo.db.Model(&model.ReportSchedule{}).
		Where("id = ?", 2).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable schedule 2: %v", err)
	}

	if err := backfillNextFireUTC(repo.db, now); err != nil {
		t.Fatalf("backfillNextFireUTC: %v", err)
	}

	due, err := repo.GetSchedule(ctx, 1)
	if err != nil {
		t.Fatalf("GetSchedule 1: %v", err)
	}
	if due.NextFireAt == nil || !due.NextFireAt.Equal(pendingFire) {
		t.Errorf("due schedule next_fire_at = %v, want %v (pending fire must survive)", due.NextFireAt, pendingFire)
	}

	disabled, err := repo.GetSchedule(ctx, 2)
	if err != nil {
		t.Fatalf("GetSchedule 2: %v", err)
	}
	if disabled.NextFireAt == nil || !disabled.NextFireAt.Equal(disabledFire) {
		t.Errorf("disabled schedule next_fire_at = %v, want %v (untouched)", disabled.NextFireAt, disabledFire)
	}
}

// TestBackfillNextFireUTC_Idempotent — the backfill runs on every boot, so
// a second pass over an already-normalized row must be a no-op.
func TestBackfillNextFireUTC_Idempotent(t *testing.T) {
	repo := newReportTestRepo(t)
	ctx := context.Background()

	now := time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	future := time.Date(2026, 6, 9, 9, 40, 0, 0, time.UTC)
	if err := repo.CreateSchedule(ctx, legacySchedule(t, 1, "healthy-utc", true, future)); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	for i := range 2 {
		if err := backfillNextFireUTC(repo.db, now); err != nil {
			t.Fatalf("backfillNextFireUTC pass %d: %v", i+1, err)
		}
		got, err := repo.GetSchedule(ctx, 1)
		if err != nil {
			t.Fatalf("GetSchedule pass %d: %v", i+1, err)
		}
		if got.NextFireAt == nil || !got.NextFireAt.Equal(future) {
			t.Fatalf("pass %d: next_fire_at = %v, want %v", i+1, got.NextFireAt, future)
		}
	}
}
