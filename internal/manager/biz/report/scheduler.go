package report

import (
	"context"
	"log/slog"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// defaultTick is how often the evaluator scans for due schedules. 1
// minute matches cron's minimum granularity — a schedule never fires
// later than ~1 min past its cron time.
const defaultTick = time.Minute

// Scheduler is the cron evaluator (HLD-014 §调度器). It owns its own
// ticker — robfig/cron is used only to compute next-fire times, not to
// schedule. On each tick it selects due schedules and fires them through
// the Usecase, which handles dedup + re-arm.
//
// Concurrency: one Scheduler per manager process. A single goroutine
// drives the ticks; each fire is synchronous within the tick so a slow
// CreateReport/UpdateSchedule serialises rather than stampeding — the
// report Generator already runs the LLM work in its own goroutine
// (FireSchedule spawns it), so the tick loop never blocks on an LLM.
type Scheduler struct {
	uc   *Usecase
	tick time.Duration
	log  *slog.Logger
}

// NewScheduler builds the evaluator. A zero tick uses defaultTick.
func NewScheduler(uc *Usecase, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{uc: uc, tick: defaultTick, log: log.With(slog.String("comp", "report-scheduler"))}
}

// Start launches the evaluator goroutine. It returns immediately; the
// loop runs until ctx is cancelled (manager shutdown). Safe to call
// once at startup.
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.log.Info("report scheduler started", slog.Duration("tick", s.tick))
	for {
		select {
		case <-ctx.Done():
			s.log.Info("report scheduler stopped")
			return
		case now := <-t.C:
			s.runOnce(ctx, now.UTC())
		}
	}
}

// runOnce fires every due schedule. A panic or error in one schedule is
// contained so the rest of the batch still fires and the loop survives.
func (s *Scheduler) runOnce(ctx context.Context, now time.Time) {
	due, err := s.uc.repo.DueSchedules(ctx, now)
	if err != nil {
		s.log.Warn("due schedules query failed", slog.Any("err", err))
		return
	}
	for _, sched := range due {
		s.fireOne(ctx, sched, now)
	}
}

func (s *Scheduler) fireOne(ctx context.Context, sched *model.ReportSchedule, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("panic firing schedule",
				slog.Uint64("schedule_id", sched.ID), slog.Any("panic", r))
		}
	}()

	loc, err := LoadLocation(sched.Timezone)
	if err != nil {
		s.log.Warn("bad schedule timezone — disabling",
			slog.Uint64("schedule_id", sched.ID), slog.String("tz", sched.Timezone))
		s.disable(ctx, sched)
		return
	}
	next, err := CronNext(sched.CronSpec, loc, now)
	if err != nil {
		s.log.Warn("bad cron spec — disabling",
			slog.Uint64("schedule_id", sched.ID), slog.String("spec", sched.CronSpec))
		s.disable(ctx, sched)
		return
	}
	if _, err := s.uc.FireSchedule(ctx, sched, now, next); err != nil {
		// FireSchedule already re-armed where it could; log and move on.
		s.log.Warn("fire schedule failed",
			slog.Uint64("schedule_id", sched.ID), slog.Any("err", err))
	}
}

// disable parks a schedule whose config can no longer be fired (bad tz /
// cron). Better than re-selecting it every minute forever. The operator
// sees enabled=false in the UI and fixes the spec.
func (s *Scheduler) disable(ctx context.Context, sched *model.ReportSchedule) {
	sched.Enabled = false
	sched.NextFireAt = nil
	if err := s.uc.repo.UpdateSchedule(ctx, sched); err != nil {
		s.log.Warn("disable bad schedule failed",
			slog.Uint64("schedule_id", sched.ID), slog.Any("err", err))
	}
}
