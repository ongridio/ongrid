// scheduler.go — the trigger.cron driver. One ticker scans enabled
// flows for trigger.cron nodes and fires those whose next-fire time has
// passed. Schedules live in memory (re-derived from each cron spec on
// boot / first sighting), same model as the report scheduler — in-flight
// runs don't survive a restart. Cron specs are evaluated in UTC.
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// cronTriggerConfig is the trigger.cron node's config.
type cronTriggerConfig struct {
	Cron string `json:"cron"` // standard 5-field cron, e.g. "0 8 * * *" (UTC)
}

// Scheduler drives trigger.cron.
type Scheduler struct {
	uc        *Usecase
	log       *slog.Logger
	interval  time.Duration
	mu        sync.Mutex
	next      map[string]time.Time // "flowID:nodeID:spec" → next fire (UTC)
	lastPrune time.Time            // last run-retention sweep (UTC)
}

// NewScheduler builds the cron driver (30s tick).
func NewScheduler(uc *Usecase, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{uc: uc, log: log, interval: 30 * time.Second, next: map[string]time.Time{}}
}

// Start launches the tick loop until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.uc == nil {
		return
	}
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			now := time.Now().UTC()
			s.tick(ctx, now)
			// Run retention sweep at most hourly — piggybacks the cron tick
			// so there's no second goroutine to manage.
			if now.Sub(s.lastPrune) >= time.Hour {
				s.lastPrune = now
				s.uc.PruneOldRuns(ctx)
			}
		}
	}
}

func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	flows, err := s.uc.ListEnabledFlows(ctx)
	if err != nil {
		s.log.Warn("flow cron: list enabled failed", slog.Any("err", err))
		return
	}
	live := map[string]bool{}
	for _, f := range flows {
		g, err := ParseGraph(f.GraphJSON)
		if err != nil {
			continue
		}
		for _, node := range g.Triggers() {
			if node.Type != NodeTriggerCron {
				continue
			}
			spec := parseCronSpec(node.Config)
			if spec == "" {
				continue
			}
			sched, err := cron.ParseStandard(spec)
			if err != nil {
				continue // invalid cron — UI validates; skip silently here
			}
			// The spec is part of the key: an edited cron gets a new key that
			// is armed from the new spec, and the old key (still holding the
			// old spec's next fire) is swept below. An unchanged spec keeps
			// its key, so unrelated graph edits don't shift the schedule.
			key := fmt.Sprintf("%d:%s:%s", f.ID, node.ID, spec)
			live[key] = true

			s.mu.Lock()
			nf, seen := s.next[key]
			if !seen {
				// First sighting: arm for the next occurrence, don't fire now.
				s.next[key] = sched.Next(now)
				s.mu.Unlock()
				continue
			}
			if now.Before(nf) {
				s.mu.Unlock()
				continue
			}
			s.next[key] = sched.Next(now)
			s.mu.Unlock()

			payload := map[string]any{"fired_at": now.Format(time.RFC3339), "cron": spec}
			if _, err := s.uc.TriggerEvent(ctx, f.ID, NodeTriggerCron, payload); err != nil {
				s.log.Warn("flow cron trigger failed", slog.Uint64("flow_id", f.ID), slog.Any("err", err))
			} else {
				s.log.Info("flow triggered by cron", slog.Uint64("flow_id", f.ID), slog.String("cron", spec))
			}
		}
	}
	// Forget schedules whose flow/trigger/spec vanished (disabled, deleted,
	// or cron edited) so a re-enabled or edited trigger re-arms cleanly.
	s.mu.Lock()
	for k := range s.next {
		if !live[k] {
			delete(s.next, k)
		}
	}
	s.mu.Unlock()
}

func parseCronSpec(cfgRaw json.RawMessage) string {
	var cfg cronTriggerConfig
	if len(cfgRaw) > 0 {
		_ = json.Unmarshal(cfgRaw, &cfg)
	}
	return strings.TrimSpace(cfg.Cron)
}
