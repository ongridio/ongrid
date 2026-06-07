package report

import (
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load loc %q: %v", name, err)
	}
	return loc
}

func TestPeriodFor_Daily(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// Fire Mon 2026-06-08 09:00 +08 → covers all of Sun 2026-06-07.
	fire := time.Date(2026, 6, 8, 9, 0, 0, 0, loc)
	p, err := PeriodFor(model.KindDaily, fire, loc, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 6, 7, 0, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 6, 8, 0, 0, 0, 0, loc)
	if !p.Start.Equal(wantStart) || !p.End.Equal(wantEnd) {
		t.Errorf("daily = [%s, %s), want [%s, %s)", p.Start, p.End, wantStart, wantEnd)
	}
}

func TestPeriodFor_Weekly(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// Fire Mon 2026-06-08 09:00 → previous ISO week = Mon 6/1 → Mon 6/8.
	fire := time.Date(2026, 6, 8, 9, 0, 0, 0, loc)
	p, err := PeriodFor(model.KindWeekly, fire, loc, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 6, 8, 0, 0, 0, 0, loc)
	if !p.Start.Equal(wantStart) || !p.End.Equal(wantEnd) {
		t.Errorf("weekly = [%s, %s), want [%s, %s)", p.Start, p.End, wantStart, wantEnd)
	}
	// Weekday sanity: start is a Monday.
	if p.Start.Weekday() != time.Monday {
		t.Errorf("weekly start weekday = %v, want Monday", p.Start.Weekday())
	}
}

func TestPeriodFor_Weekly_MidWeekFire(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// Fire Thu 2026-06-11 → "this week" Monday = 6/8, previous = 6/1..6/8.
	fire := time.Date(2026, 6, 11, 9, 0, 0, 0, loc)
	p, _ := PeriodFor(model.KindWeekly, fire, loc, time.Time{})
	wantStart := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	if !p.Start.Equal(wantStart) {
		t.Errorf("weekly mid-week start = %s, want %s", p.Start, wantStart)
	}
}

func TestPeriodFor_Monthly(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	// Fire 2026-06-01 → previous month = May.
	fire := time.Date(2026, 6, 1, 9, 0, 0, 0, loc)
	p, err := PeriodFor(model.KindMonthly, fire, loc, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 5, 1, 0, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 6, 1, 0, 0, 0, 0, loc)
	if !p.Start.Equal(wantStart) || !p.End.Equal(wantEnd) {
		t.Errorf("monthly = [%s, %s), want [%s, %s)", p.Start, p.End, wantStart, wantEnd)
	}
}

func TestPeriodFor_Monthly_JanCrossesYear(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	fire := time.Date(2026, 1, 1, 9, 0, 0, 0, loc)
	p, _ := PeriodFor(model.KindMonthly, fire, loc, time.Time{})
	wantStart := time.Date(2025, 12, 1, 0, 0, 0, 0, loc)
	if !p.Start.Equal(wantStart) {
		t.Errorf("monthly Jan start = %s, want 2025-12-01", p.Start)
	}
}

func TestPeriodFor_Custom_WithPrevFire(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	prev := time.Date(2026, 6, 8, 9, 0, 0, 0, loc)
	fire := time.Date(2026, 6, 8, 15, 0, 0, 0, loc)
	p, err := PeriodFor(model.KindCustom, fire, loc, prev)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Start.Equal(prev) || !p.End.Equal(fire) {
		t.Errorf("custom = [%s, %s), want [%s, %s)", p.Start, p.End, prev, fire)
	}
}

func TestPeriodFor_Custom_FirstRunFallsBack24h(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	fire := time.Date(2026, 6, 8, 15, 0, 0, 0, loc)
	// Zero prevFire → trailing 24h.
	p, _ := PeriodFor(model.KindCustom, fire, loc, time.Time{})
	wantStart := fire.AddDate(0, 0, -1)
	if !p.Start.Equal(wantStart) || !p.End.Equal(fire) {
		t.Errorf("custom first-run = [%s, %s), want [%s, %s)", p.Start, p.End, wantStart, fire)
	}
}

func TestPeriodFor_UnknownKind(t *testing.T) {
	if _, err := PeriodFor("hourly", time.Now(), time.UTC, time.Time{}); err == nil {
		t.Error("expected error for unknown kind")
	}
}

func TestPeriodFor_NilLocDefaultsUTC(t *testing.T) {
	fire := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC)
	p, err := PeriodFor(model.KindDaily, fire, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Start.Location() != time.UTC {
		t.Errorf("nil loc should default to UTC, got %v", p.Start.Location())
	}
}

func TestTitleFor(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	weekly := Period{
		Start: time.Date(2026, 6, 1, 0, 0, 0, 0, loc),
		End:   time.Date(2026, 6, 8, 0, 0, 0, 0, loc),
	}
	got := TitleFor(model.KindWeekly, weekly, "zh")
	// 2026-06-01 is ISO week 23.
	if got != "周报 · 2026 W23 (06-01 – 06-07)" {
		t.Errorf("weekly title = %q", got)
	}
}
