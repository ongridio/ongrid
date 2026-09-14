package report

import (
	"context"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

func TestCreateSchedule_ArmsNextFire(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	loc := mustLoc(t, "Asia/Shanghai")
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)

	s := &model.ReportSchedule{
		ID:        1,
		CreatedBy: 42,
		Kind:      model.KindWeekly,
		Timezone:  "Asia/Shanghai",
	}
	if err := uc.CreateSchedule(context.Background(), s, now); err != nil {
		t.Fatal(err)
	}
	// Preset cron filled in.
	if s.CronSpec != "0 9 * * 1" {
		t.Errorf("cron spec = %q, want weekly default", s.CronSpec)
	}
	// next_fire_at armed to next Monday 9am.
	if s.NextFireAt == nil {
		t.Fatal("next_fire_at not armed")
	}
	want := time.Date(2026, 6, 15, 9, 0, 0, 0, loc)
	if !s.NextFireAt.Equal(want) {
		t.Errorf("next_fire_at = %s, want %s", s.NextFireAt, want)
	}
	// Defaults backfilled.
	if s.ScopeJSON != "{}" || s.ChannelIDsJSON != "[]" || s.AgentPersona != model.DefaultReporterPersona {
		t.Errorf("defaults not backfilled: %+v", s)
	}
}

// TestCreateSchedule_StoresNextFireAsUTC pins the storage convention that
// makes DueSchedules correct for non-UTC timezones. A schedule whose
// timezone is Asia/Shanghai must persist its armed next_fire_at as a UTC
// instant, not a +08:00 wall-clock string — otherwise the SQLite string
// comparison `next_fire_at <= now` (now in UTC) never selects it.
func TestCreateSchedule_StoresNextFireAsUTC(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	loc := mustLoc(t, "Asia/Shanghai")
	now := time.Date(2026, 6, 8, 10, 0, 0, 0, loc)

	s := &model.ReportSchedule{
		ID:        1,
		CreatedBy: 42,
		Kind:      model.KindWeekly,
		Timezone:  "Asia/Shanghai",
	}
	if err := uc.CreateSchedule(context.Background(), s, now); err != nil {
		t.Fatal(err)
	}
	if s.NextFireAt == nil {
		t.Fatal("next_fire_at not armed")
	}
	// The armed instant is the same, but must be represented in UTC.
	if s.NextFireAt.Location() != time.UTC {
		t.Errorf("next_fire_at location = %v, want UTC", s.NextFireAt.Location())
	}
	want := time.Date(2026, 6, 15, 1, 0, 0, 0, time.UTC) // next Mon 09:00 Shanghai == 01:00 UTC
	if !s.NextFireAt.Equal(want) {
		t.Errorf("next_fire_at = %s, want %s", s.NextFireAt, want)
	}
}

func TestCreateSchedule_CustomRequiresSpec(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	s := &model.ReportSchedule{ID: 1, Kind: model.KindCustom, Timezone: "UTC"}
	if err := uc.CreateSchedule(context.Background(), s, time.Now()); err == nil {
		t.Error("custom kind without cron spec should error")
	}
}

func TestScheduler_RunOnce_FiresDue(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	loc := mustLoc(t, "Asia/Shanghai")

	// A schedule due now (next_fire_at in the past).
	past := time.Date(2026, 6, 8, 9, 0, 0, 0, loc)
	s := &model.ReportSchedule{
		ID:         1,
		CreatedBy:  42,
		Kind:       model.KindWeekly,
		CronSpec:   "0 9 * * 1",
		Timezone:   "Asia/Shanghai",
		ScopeJSON:  "{}",
		Enabled:    true,
		NextFireAt: &past,
	}
	_ = repo.CreateSchedule(context.Background(), s)

	sched := NewScheduler(uc, nil)
	now := time.Date(2026, 6, 8, 9, 0, 30, 0, loc) // 30s after due
	sched.runOnce(context.Background(), now)

	// A report was created.
	if len(repo.reports) != 1 {
		t.Fatalf("expected 1 report fired, got %d", len(repo.reports))
	}
	// Schedule re-armed to a future fire.
	got := repo.schedules[1]
	if got.NextFireAt == nil || !got.NextFireAt.After(now) {
		t.Errorf("schedule not re-armed forward: %+v", got.NextFireAt)
	}
}

func TestScheduler_RunOnce_DisablesBadCron(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	past := time.Now().Add(-time.Hour)
	s := &model.ReportSchedule{
		ID:         1,
		Kind:       model.KindCustom,
		CronSpec:   "garbage spec",
		Timezone:   "UTC",
		ScopeJSON:  "{}",
		Enabled:    true,
		NextFireAt: &past,
	}
	_ = repo.CreateSchedule(context.Background(), s)

	sched := NewScheduler(uc, nil)
	sched.runOnce(context.Background(), time.Now())

	got := repo.schedules[1]
	if got.Enabled {
		t.Error("bad-cron schedule should be disabled")
	}
	if got.NextFireAt != nil {
		t.Error("disabled schedule should have nil next_fire_at")
	}
	if len(repo.reports) != 0 {
		t.Error("no report should fire for a bad-cron schedule")
	}
}

func TestScheduler_RunOnce_SkipsDisabled(t *testing.T) {
	repo := newFakeRepo()
	uc := NewUsecase(repo, nopGenerator{}, seqIDGen())
	past := time.Now().Add(-time.Hour)
	s := &model.ReportSchedule{
		ID: 1, Kind: model.KindWeekly, CronSpec: "0 9 * * 1",
		Timezone: "UTC", Enabled: false, NextFireAt: &past,
	}
	_ = repo.CreateSchedule(context.Background(), s)
	sched := NewScheduler(uc, nil)
	sched.runOnce(context.Background(), time.Now())
	if len(repo.reports) != 0 {
		t.Error("disabled schedule should not fire")
	}
}
