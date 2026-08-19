// Package report is the biz layer for scheduled operational reports
// (HLD-014). This PR (PR-1) lands the Usecase skeleton: schedule CRUD
// passthrough, the period calculation, and the dedup-protected report
// row creation that the cron evaluator (PR-3) and the manual
// "generate now" API (PR-4) both call. The actual content generation
// (ReportFacts collection + reporter worker) is PR-2 and is left as a
// Generator seam here.
package report

import (
	"context"
	"errors"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the storage surface the Usecase needs. Implemented by
// data/report/store.Repo. Kept minimal for PR-1 — read paths the API
// layer needs (List/Get) arrive with PR-4.
type Repo interface {
	// CreateReport inserts a report row. Implementations MUST surface a
	// unique-constraint violation on (schedule_id, period_start) as
	// errs.ErrConflict so the Usecase can treat a duplicate fire as a
	// no-op rather than a hard error.
	CreateReport(ctx context.Context, r *model.Report) error
	GetReport(ctx context.Context, id string) (*model.Report, error)
	UpdateReport(ctx context.Context, r *model.Report) error

	CreateSchedule(ctx context.Context, s *model.ReportSchedule) error
	GetSchedule(ctx context.Context, id uint64) (*model.ReportSchedule, error)
	UpdateSchedule(ctx context.Context, s *model.ReportSchedule) error
	// DueSchedules returns enabled schedules whose next_fire_at <= now.
	// The evaluator (PR-3) drives generation off this.
	DueSchedules(ctx context.Context, now time.Time) ([]*model.ReportSchedule, error)
}

// IDGen mints report UUIDs. Injected so tests get deterministic ids and
// so we don't hard-depend on a uuid lib at this layer.
type IDGen func() string

// Generator turns a created (pending) report into a finished one:
// collects ReportFacts, runs the reporter worker, writes ContentJSON,
// flips status to ready/failed. PR-1 ships a no-op default; PR-2
// replaces it. The Usecase calls it asynchronously after the row is
// committed so a slow LLM never blocks the evaluator tick.
type Generator interface {
	Generate(ctx context.Context, reportID string)
}

type readyChecker interface {
	Ready(ctx context.Context) error
}

// nopGenerator leaves the report in pending — used until PR-2 wires the
// real worker. Surfaced explicitly (not nil) so callers don't have to
// nil-check on every fire.
type nopGenerator struct{}

func (nopGenerator) Generate(context.Context, string) {}

type unavailableGenerator struct {
	err error
}

// NewUnavailableGenerator returns a Generator that keeps report routes
// mounted while generation is unavailable, for example when no LLM
// provider is configured at startup.
func NewUnavailableGenerator(reason string) Generator {
	if reason == "" {
		reason = "report generator unavailable"
	}
	return unavailableGenerator{err: fmt.Errorf("%w: %s", errs.ErrNotWiredYet, reason)}
}

func (g unavailableGenerator) Generate(context.Context, string) {}

func (g unavailableGenerator) Ready(context.Context) error { return g.err }

// Usecase is the report business logic.
type Usecase struct {
	repo  Repo
	read  ReadRepo // attached via WithReadRepo for the API path; nil on the scheduler-only path
	gen   Generator
	idGen IDGen
	// defaultLocale stamps scheduled (headless) reports' title + content
	// language; manual generate/run-now override it with the requester's
	// Accept-Language. Empty → Chinese title default. Set via
	// WithDefaultLocale in main.go (ONGRID_DEFAULT_LOCALE).
	defaultLocale string
}

// WithDefaultLocale sets the headless/scheduled report locale (the
// generator's DefaultLocale). Builder-style so the test callsites stay
// on the 3-arg NewUsecase.
func (u *Usecase) WithDefaultLocale(locale string) *Usecase {
	u.defaultLocale = locale
	return u
}

// errExpiredShare is returned when a share token has passed its TTL.
var errExpiredShare = errors.Join(errs.ErrNotFound, errors.New("share link expired"))

// NewUsecase builds the Usecase. A nil Generator falls back to the
// no-op (report stays pending — useful in PR-1 tests and before PR-2
// wires the worker). A nil IDGen is a programming error and panics —
// the caller in main.go always supplies uuid.NewString.
func NewUsecase(repo Repo, gen Generator, idGen IDGen) *Usecase {
	if repo == nil {
		panic("report: nil Repo")
	}
	if idGen == nil {
		panic("report: nil IDGen")
	}
	if gen == nil {
		gen = nopGenerator{}
	}
	return &Usecase{repo: repo, gen: gen, idGen: idGen}
}

// CreateSchedule validates a new schedule and arms its initial
// next_fire_at, then persists it. Used by the API (PR-4) and the chat
// tool. For preset kinds with an empty CronSpec, the default cron for
// that kind is filled in; custom kind requires an explicit spec.
//
// `now` is the reference time the first next_fire_at is computed after
// (injected so tests are deterministic; callers pass time.Now().UTC()).
func (u *Usecase) CreateSchedule(ctx context.Context, s *model.ReportSchedule, now time.Time) error {
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
	if s.ScopeJSON == "" {
		s.ScopeJSON = "{}"
	}
	if s.ChannelIDsJSON == "" {
		s.ChannelIDsJSON = "[]"
	}
	if s.AgentPersona == "" {
		s.AgentPersona = model.DefaultReporterPersona
	}
	// Store next_fire_at in UTC (see CronNext): the scheduler evaluates
	// against a UTC clock, so persisting a schedule-timezone value would
	// break the `next_fire_at <= now` string comparison in DueSchedules
	// for any non-UTC timezone (the +08:00 > +00:00 text sort makes a
	// due schedule look not-due). Display layers localize from the UTC
	// instant on read.
	utc := next.UTC()
	s.NextFireAt = &utc
	return u.repo.CreateSchedule(ctx, s)
}

// FireSchedule generates one report for a due schedule and re-arms its
// next_fire_at. Called by the evaluator (PR-3). Idempotent on the
// (schedule_id, period_start) unique key: if two evaluator ticks race
// the same schedule, the second CreateReport hits ErrConflict and we
// skip generation (but still re-arm so the schedule keeps advancing).
//
// nextFireAt is computed by the caller (it owns the cron parser); we
// take it as an argument so this layer stays free of the cron lib.
func (u *Usecase) FireSchedule(ctx context.Context, s *model.ReportSchedule, fireAt, nextFireAt time.Time) (*model.Report, error) {
	if err := u.ensureGeneratorReady(ctx); err != nil {
		return nil, err
	}
	loc, err := loadLocation(s.Timezone)
	if err != nil {
		return nil, err
	}
	var prevFire time.Time
	if s.LastFireAt != nil {
		prevFire = *s.LastFireAt
	}
	period, err := PeriodFor(s.Kind, fireAt, loc, prevFire)
	if err != nil {
		return nil, err
	}

	rpt := u.buildPendingReport(s, period)
	createErr := u.repo.CreateReport(ctx, rpt)

	// Re-arm the schedule regardless of whether this fire produced a new
	// report — a duplicate (ErrConflict) still means "this window is
	// handled", and we must advance next_fire_at or the evaluator will
	// re-select this row forever.
	now := fireAt
	s.LastFireAt = &now
	// Normalize to UTC for the same reason as CreateSchedule — the
	// scheduler computes nextFireAt in the schedule timezone, but the
	// DueSchedules comparison (next_fire_at <= now, both UTC) requires a
	// UTC representation.
	utcNext := nextFireAt.UTC()
	s.NextFireAt = &utcNext
	if rpt.Status == model.StatusPending {
		s.LastReportID = &rpt.ID
	}
	if uerr := u.repo.UpdateSchedule(ctx, s); uerr != nil {
		// Surface the re-arm failure — leaving next_fire_at stale would
		// cause repeated re-fires. The report (if created) is fine.
		return nil, uerr
	}

	if createErr != nil {
		if errors.Is(createErr, errs.ErrConflict) {
			// Duplicate window — already generated. Not an error.
			return nil, nil
		}
		return nil, createErr
	}

	go u.gen.Generate(context.WithoutCancel(ctx), rpt.ID)
	return rpt, nil
}

// GenerateNow creates an ad-hoc report not bound to a schedule (manual
// "generate now", API PR-4). scheduleID is nil so the dedup unique key
// doesn't apply — every manual trigger produces a fresh row.
// taskRef is the owning-task back-ref (HLD-022), e.g. "report-schedule:42" when
// invoked via a schedule's run-now; "" for a truly ad-hoc generate.
func (u *Usecase) GenerateNow(ctx context.Context, createdBy uint64, kind, tz, scopeJSON, locale, taskRef string, period Period) (*model.Report, error) {
	if _, err := loadLocation(tz); err != nil {
		return nil, err
	}
	if err := u.ensureGeneratorReady(ctx); err != nil {
		return nil, err
	}
	if locale == "" {
		locale = u.defaultLocale
	}
	rpt := &model.Report{
		ID:          u.idGen(),
		ScheduleID:  nil,
		TaskID:      taskRef,
		RunID:       u.idGen(), // each run-now / manual generate is its own run
		CreatedBy:   createdBy,
		Title:       TitleFor(kind, period, locale),
		Kind:        kind,
		PeriodStart: period.Start,
		PeriodEnd:   period.End,
		Timezone:    tz,
		Locale:      locale,
		Status:      model.StatusPending,
		ErrorMsg:    "",
		ContentJSON: "",
		ContentMD:   "",
		ScopeJSON:   scopeJSON,
	}
	if err := u.repo.CreateReport(ctx, rpt); err != nil {
		return nil, err
	}
	go u.gen.Generate(context.WithoutCancel(ctx), rpt.ID)
	return rpt, nil
}

// buildPendingReport assembles the pending row for a scheduled fire.
func (u *Usecase) buildPendingReport(s *model.ReportSchedule, p Period) *model.Report {
	id := s.ID
	return &model.Report{
		ID:          u.idGen(),
		ScheduleID:  &id,
		TaskID:      fmt.Sprintf("report-schedule:%d", id),
		RunID:       u.idGen(), // each cron fire is its own run
		CreatedBy:   s.CreatedBy,
		Title:       TitleFor(s.Kind, p, u.defaultLocale),
		Kind:        s.Kind,
		PeriodStart: p.Start,
		PeriodEnd:   p.End,
		Timezone:    s.Timezone,
		Locale:      u.defaultLocale,
		Status:      model.StatusPending,
		ErrorMsg:    "",
		ContentJSON: "",
		ContentMD:   "",
		ScopeJSON:   s.ScopeJSON,
	}
}

func (u *Usecase) ensureGeneratorReady(ctx context.Context) error {
	if g, ok := u.gen.(readyChecker); ok {
		return g.Ready(ctx)
	}
	return nil
}

// loadLocation resolves a schedule timezone, defaulting to UTC on empty.
// An unparseable tz is a config error surfaced as ErrInvalid.
func loadLocation(tz string) (*time.Location, error) {
	if tz == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, errors.Join(errs.ErrInvalid, err)
	}
	return loc, nil
}
