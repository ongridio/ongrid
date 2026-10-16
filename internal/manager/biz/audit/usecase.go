// Package audit is the BC-level seam for HLD-010 audit logging. It
// exposes a single Emit method that callers (middleware, handlers,
// the retention goroutine) use to record observations. Failure to
// write is logged but never returned — audit must not block business.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/audit"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
)

// Repo is the persistence seam the usecase consumes. Implemented by
// data/audit/store.Repo.
type Repo interface {
	Insert(ctx context.Context, log *model.Log) error
	List(ctx context.Context, f ListFilters) ([]model.Log, int64, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// ListFilters shares the model contract without depending on persistence.
type ListFilters = model.ListFilters

// Event is the input shape for Emit. Caller fills what it knows; the
// usecase stamps OccurredAt and serialises Payload.
type Event = auditctx.Event

// Usecase is the BC façade.
type Usecase struct {
	repo Repo
	log  *slog.Logger
}

// New builds a Usecase. log is mandatory for the warn-on-failure path.
func New(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log}
}

// Emit persists one Event. Returns nothing — failures are warn-logged
// (HLD-010 "audit write failure must never block business").
func (u *Usecase) Emit(ctx context.Context, ev Event) {
	if u == nil || u.repo == nil {
		return
	}
	if ev.Action == "" || ev.Status == "" {
		u.log.Warn("audit: dropped event with empty action or status",
			slog.String("action", ev.Action),
			slog.String("status", ev.Status))
		return
	}
	row := &model.Log{
		OccurredAt:   time.Now().UTC(),
		UserID:       ev.UserID,
		UserEmail:    ev.UserEmail,
		Role:         ev.Role,
		IP:           ev.IP,
		UserAgent:    ev.UserAgent,
		Action:       ev.Action,
		ResourceType: ev.ResourceType,
		ResourceID:   ev.ResourceID,
		ResourceName: ev.ResourceName,
		Status:       ev.Status,
		ErrorCode:    ev.ErrorCode,
		ErrorMessage: truncate(ev.ErrorMessage, 512),
		RequestID:    ev.RequestID,
	}
	if ev.Payload != nil {
		if b, err := json.Marshal(ev.Payload); err == nil {
			row.PayloadJSON = string(b)
		} else {
			u.log.Warn("audit: payload marshal failed; storing empty",
				slog.String("action", ev.Action),
				slog.Any("err", err))
		}
	}
	if err := u.repo.Insert(ctx, row); err != nil {
		u.log.Warn("audit: insert failed; observation lost",
			slog.String("action", ev.Action),
			slog.String("resource_type", ev.ResourceType),
			slog.String("resource_id", ev.ResourceID),
			slog.Any("err", err))
	}
}

// List is the read path for the admin UI.
func (u *Usecase) List(ctx context.Context, f ListFilters) ([]model.Log, int64, error) {
	if u == nil || u.repo == nil {
		return nil, 0, nil
	}
	return u.repo.List(ctx, f)
}

// ListChanges is the RCA-facing convenience over List (HLD-013 Phase 2):
// returns the mutating audit rows in [from, to], optionally narrowed to a
// resource type and/or action, capped at limit. Backs the
// query_change_events AIOps tool's "what changed near the incident" step.
// Failures (status=failure/denied) are intentionally included — "someone
// tried to change X right before the symptom" is itself a root-cause lead.
func (u *Usecase) ListChanges(ctx context.Context, from, to time.Time, resourceType, action string, limit int) ([]model.Log, error) {
	if u == nil || u.repo == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	logs, _, err := u.List(ctx, ListFilters{
		From:         from,
		To:           to,
		ResourceType: resourceType,
		Action:       action,
		Limit:        limit,
	})
	return logs, err
}

// RunRetention runs the daily cleanup at the next 03:00 wall clock and
// every 24h thereafter. retentionDays <= 0 disables the sweep entirely
// (operator may prefer to manage retention via external archival).
// Blocks until ctx is cancelled.
func (u *Usecase) RunRetention(ctx context.Context, retentionDays int) error {
	if u == nil || u.repo == nil || retentionDays <= 0 {
		<-ctx.Done()
		return nil
	}
	for {
		// Next 03:00 local. Cheap arithmetic; we don't need a cron lib
		// for once-a-day.
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		removed, err := u.repo.DeleteOlderThan(ctx, cutoff)
		if err != nil {
			u.log.Warn("audit retention: delete failed", slog.Any("err", err))
			continue
		}
		u.log.Info("audit retention swept",
			slog.Int("retention_days", retentionDays),
			slog.Int64("rows_removed", removed))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
