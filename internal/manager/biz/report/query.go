package report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// ReportFilter scopes a report list query. Zero value = all reports,
// newest first.
type ReportFilter struct {
	Status     string  // "" = any
	Kind       string  // "" = any
	ScheduleID *uint64 // nil = any; set = only reports from this schedule
	TaskID     string  // "" = any; set = only reports owned by this task (HLD-022)
	Limit      int     // 0 → DefaultListLimit
	Offset     int
}

// DefaultListLimit caps an unbounded list query.
const DefaultListLimit = 50

// ShareTTL is how long a share token stays valid.
const ShareTTL = 30 * 24 * time.Hour

// ReadRepo is the read + delete surface the API needs beyond the core
// Repo. Implemented by data/report/store.Repo. Split out so the
// scheduler-only Repo interface (PR-1) stays minimal.
type ReadRepo interface {
	ListReports(ctx context.Context, f ReportFilter) ([]*model.Report, error)
	CountReports(ctx context.Context, f ReportFilter) (int64, error)
	DeleteReport(ctx context.Context, id string) error
	ListSchedules(ctx context.Context, ownerID uint64, all bool) ([]*model.ReportSchedule, error)
	DeleteSchedule(ctx context.Context, id uint64) error
	GetReportByShareToken(ctx context.Context, token string) (*model.Report, error)
	// HLD-022 Phase 2 — unified-task spine (oneoff rows).
	CreateTask(ctx context.Context, t *model.Task) error
	GetTask(ctx context.Context, id string) (*model.Task, error)
	ListTasks(ctx context.Context) ([]*model.Task, error)
	UpdateTaskStatus(ctx context.Context, id, status string) error
	DeleteTask(ctx context.Context, id string) error
}

// WithReadRepo attaches the read surface to the Usecase. The scheduler
// path (PR-1/3) doesn't need it; the API path (PR-4) does. Returns the
// receiver for chaining.
func (u *Usecase) WithReadRepo(rr ReadRepo) *Usecase {
	u.read = rr
	return u
}

// ListReports returns reports matching the filter.
func (u *Usecase) ListReports(ctx context.Context, f ReportFilter) ([]*model.Report, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = DefaultListLimit
	}
	return u.read.ListReports(ctx, f)
}

func (u *Usecase) CountReports(ctx context.Context, f ReportFilter) (int64, error) {
	return u.read.CountReports(ctx, f)
}

// GetReport returns one report by id.
func (u *Usecase) GetReport(ctx context.Context, id string) (*model.Report, error) {
	return u.repo.GetReport(ctx, id)
}

// DeleteReport removes a report.
func (u *Usecase) DeleteReport(ctx context.Context, id string) error {
	return u.read.DeleteReport(ctx, id)
}

// ListSchedules returns schedules. When all is false only the owner's
// schedules are returned (used for non-admin callers).
func (u *Usecase) ListSchedules(ctx context.Context, ownerID uint64, all bool) ([]*model.ReportSchedule, error) {
	return u.read.ListSchedules(ctx, ownerID, all)
}

// GetSchedule returns one schedule by id.
func (u *Usecase) GetSchedule(ctx context.Context, id uint64) (*model.ReportSchedule, error) {
	return u.repo.GetSchedule(ctx, id)
}

// UpdateSchedule persists edits, re-validating + re-arming next_fire_at
// when the cron / timezone changed. `now` anchors the re-arm.
func (u *Usecase) UpdateSchedule(ctx context.Context, s *model.ReportSchedule, now time.Time) error {
	if s.CronSpec == "" && s.Kind != model.KindCustom {
		spec, err := CronSpecForKind(s.Kind)
		if err != nil {
			return err
		}
		s.CronSpec = spec
	}
	loc, err := loadLocation(s.Timezone)
	if err != nil {
		return err
	}
	next, err := CronNext(s.CronSpec, loc, now)
	if err != nil {
		return err
	}
	// UTC storage convention — see CreateSchedule.
	utc := next.UTC()
	s.NextFireAt = &utc
	return u.repo.UpdateSchedule(ctx, s)
}

// SetScheduleEnabled toggles a schedule. Disabling clears next_fire_at;
// enabling re-arms it from now.
func (u *Usecase) SetScheduleEnabled(ctx context.Context, id uint64, enabled bool, now time.Time) (*model.ReportSchedule, error) {
	s, err := u.repo.GetSchedule(ctx, id)
	if err != nil {
		return nil, err
	}
	s.Enabled = enabled
	if !enabled {
		s.NextFireAt = nil
	} else {
		loc, err := loadLocation(s.Timezone)
		if err != nil {
			return nil, err
		}
		next, err := CronNext(s.CronSpec, loc, now)
		if err != nil {
			return nil, err
		}
		// UTC storage convention — see CreateSchedule.
		utc := next.UTC()
		s.NextFireAt = &utc
	}
	if err := u.repo.UpdateSchedule(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

// DeleteSchedule removes a schedule. Existing reports it produced are
// kept (their schedule_id dangles, harmless — the artifact stands alone).
func (u *Usecase) DeleteSchedule(ctx context.Context, id uint64) error {
	return u.read.DeleteSchedule(ctx, id)
}

// RunNow generates a report immediately from a schedule's config,
// without disturbing its next_fire_at. The report is created as a
// manual (nil schedule_id) artifact so it never collides with the
// scheduled window's dedup key.
func (u *Usecase) RunNow(ctx context.Context, scheduleID uint64, locale string, now time.Time) (*model.Report, error) {
	s, err := u.repo.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	loc, err := loadLocation(s.Timezone)
	if err != nil {
		return nil, err
	}
	var prev time.Time
	if s.LastFireAt != nil {
		prev = *s.LastFireAt
	}
	period, err := PeriodFor(s.Kind, now, loc, prev)
	if err != nil {
		return nil, err
	}
	// run-now is an operator action → narrate in the operator's UI locale
	// (Accept-Language), same as a manual generate.
	// run-now reports belong to the schedule's task even though ScheduleID stays
	// nil (dedup-key avoidance) — stamp the task back-ref so the 任务 lists them.
	return u.GenerateNow(ctx, s.CreatedBy, s.Kind, s.Timezone, s.ScopeJSON, locale, fmt.Sprintf("report-schedule:%d", scheduleID), period)
}

// ShareReport mints (or refreshes) a 30-day share token on a report and
// returns it. The token grants unauthenticated read at /r/{token}.
func (u *Usecase) ShareReport(ctx context.Context, id string, now time.Time) (string, error) {
	rpt, err := u.repo.GetReport(ctx, id)
	if err != nil {
		return "", err
	}
	token := randomToken()
	exp := now.Add(ShareTTL)
	rpt.ShareToken = &token
	rpt.ShareExpiresAt = &exp
	if err := u.repo.UpdateReport(ctx, rpt); err != nil {
		return "", err
	}
	return token, nil
}

// GetSharedReport resolves a report by share token, enforcing the TTL.
// Used by the public /r/{token} route.
func (u *Usecase) GetSharedReport(ctx context.Context, token string, now time.Time) (*model.Report, error) {
	rpt, err := u.read.GetReportByShareToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if rpt.ShareExpiresAt == nil || now.After(*rpt.ShareExpiresAt) {
		return nil, errExpiredShare
	}
	return rpt, nil
}

// randomToken returns a 32-char hex token (16 random bytes).
func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
