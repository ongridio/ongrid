// Package monitor is the biz layer for user-managed Monitor panels.
// Operators create / edit / delete panels through the SPA; this package
// owns persistence (via store.Repo) and asynchronously mirrors every
// change into a single ongrid-managed Grafana dashboard.
//
// Sync is one-way (ongrid → Grafana). Edits made directly in Grafana are
// NOT pulled back; on the next mirror push from ongrid they get
// overwritten. This is intentional — it keeps the data model simple
// (one source of truth, no merge conflicts) and matches the operator
// mental model: "build it in ongrid, view it in Grafana".
//
// API contract (server/monitor/http.go):
//
//	GET    /v1/monitor/panels         — list, ordered by ordinal asc
//	POST   /v1/monitor/panels         — create
//	PATCH  /v1/monitor/panels/{id}    — update title / promql / legend / unit / type / ordinal
//	DELETE /v1/monitor/panels/{id}    — delete
//
// Sync invariant: API calls return 200 as soon as the row is persisted.
// Grafana mirroring is fire-and-forget on a background goroutine; a
// failed mirror is recorded in last_sync_error so the UI can surface
// "synced / failed" status without ever failing the operator action.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/monitor"
	"github.com/ongridio/ongrid/internal/pkg/dbx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the narrow persistence contract this service depends on. The
// concrete implementation lives in internal/manager/data/monitor/store;
// the interface lets tests substitute an in-memory fake.
type Repo interface {
	List(ctx context.Context) ([]*model.Panel, error)
	Get(ctx context.Context, id uint64) (*model.Panel, error)
	MaxOrdinal(ctx context.Context) (int, error)
	Create(ctx context.Context, p *model.Panel) (*model.Panel, error)
	Update(ctx context.Context, id uint64, fields map[string]any) (*model.Panel, error)
	SetSyncResult(ctx context.Context, id uint64, errMsg string) error
	Delete(ctx context.Context, id uint64) error
}

// GrafanaSyncer is the narrow surface the service uses to mirror
// dashboards. *biz/grafana.Service satisfies it via the SyncMonitorPanels
// method added alongside this package. Optional: when nil, the service
// degrades to "persist only" with no sync — useful in tests and for
// deployments that disable Grafana entirely.
type GrafanaSyncer interface {
	SyncMonitorPanels(ctx context.Context, panels []*model.Panel) error
}

// Service is the biz-layer orchestrator.
type Service struct {
	repo   Repo
	syncer GrafanaSyncer
	log    *slog.Logger
	syncTO time.Duration
}

// New builds the service. syncer may be nil. log may be nil (defaults
// to slog.Default).
func New(repo Repo, syncer GrafanaSyncer, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:   repo,
		syncer: syncer,
		log:    log,
		// 30s is generous for a single Grafana POST /api/dashboards/db.
		// Going beyond that almost certainly means Grafana is down; we
		// don't want a slow upstream pinning a goroutine forever.
		syncTO: 30 * time.Second,
	}
}

// CreateInput is the wire-shape for POST /v1/monitor/panels. Ordinal is
// optional; empty falls back to max(ordinal)+1 so new panels land at
// the end.
type CreateInput struct {
	Title   string `json:"title"`
	Type    string `json:"type"`
	PromQL  string `json:"promql"`
	Legend  string `json:"legend"`
	Unit    string `json:"unit"`
	Ordinal *int   `json:"ordinal,omitempty"`
}

// UpdateInput is the wire-shape for PATCH /v1/monitor/panels/{id}. Every
// field is optional; nil means "leave column alone". The pointer-field
// shape lets the wire distinguish "set to empty string" from "don't
// touch", which a plain string can't do.
type UpdateInput struct {
	Title   *string `json:"title,omitempty"`
	Type    *string `json:"type,omitempty"`
	PromQL  *string `json:"promql,omitempty"`
	Legend  *string `json:"legend,omitempty"`
	Unit    *string `json:"unit,omitempty"`
	Ordinal *int    `json:"ordinal,omitempty"`
}

// List returns all panels ordered by ordinal asc.
func (s *Service) List(ctx context.Context) ([]*model.Panel, error) {
	return s.repo.List(ctx)
}

// Get returns one panel.
func (s *Service) Get(ctx context.Context, id uint64) (*model.Panel, error) {
	return s.repo.Get(ctx, id)
}

// Create persists the new panel and kicks off an async Grafana mirror.
// The mirror runs on a detached goroutine — it must not block the API.
func (s *Service) Create(ctx context.Context, in CreateInput) (*model.Panel, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.PromQL = strings.TrimSpace(in.PromQL)
	in.Type = strings.TrimSpace(in.Type)
	in.Legend = strings.TrimSpace(in.Legend)
	in.Unit = strings.TrimSpace(in.Unit)
	if in.Title == "" {
		return nil, fmt.Errorf("%w: title required", errs.ErrInvalid)
	}
	if in.PromQL == "" {
		return nil, fmt.Errorf("%w: promql required", errs.ErrInvalid)
	}
	if in.Type == "" {
		in.Type = model.PanelTypeTimeseries
	}
	if !model.ValidPanelType(in.Type) {
		return nil, fmt.Errorf("%w: invalid type %q", errs.ErrInvalid, in.Type)
	}

	ord := 0
	if in.Ordinal != nil {
		ord = *in.Ordinal
	} else {
		max, err := s.repo.MaxOrdinal(ctx)
		if err != nil {
			return nil, fmt.Errorf("max ordinal: %w", err)
		}
		ord = max + 1
	}

	row := &model.Panel{
		Title:   in.Title,
		Type:    in.Type,
		PromQL:  in.PromQL,
		Legend:  in.Legend,
		Unit:    in.Unit,
		Ordinal: ord,
	}
	saved, err := s.repo.Create(ctx, row)
	if err != nil {
		return nil, err
	}
	dbx.AfterCommit(ctx, func() { s.kickSync("create", saved.ID) })
	return saved, nil
}

// Update writes the changed columns and triggers an async Grafana
// mirror. Empty-update (no fields set) is a no-op aside from re-syncing
// the dashboard, which is harmless.
func (s *Service) Update(ctx context.Context, id uint64, in UpdateInput) (*model.Panel, error) {
	if id == 0 {
		return nil, fmt.Errorf("%w: id required", errs.ErrInvalid)
	}
	fields := map[string]any{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return nil, fmt.Errorf("%w: title cannot be empty", errs.ErrInvalid)
		}
		fields["title"] = t
	}
	if in.Type != nil {
		t := strings.TrimSpace(*in.Type)
		if !model.ValidPanelType(t) {
			return nil, fmt.Errorf("%w: invalid type %q", errs.ErrInvalid, t)
		}
		fields["type"] = t
	}
	if in.PromQL != nil {
		q := strings.TrimSpace(*in.PromQL)
		if q == "" {
			return nil, fmt.Errorf("%w: promql cannot be empty", errs.ErrInvalid)
		}
		fields["promql"] = q
	}
	if in.Legend != nil {
		fields["legend"] = strings.TrimSpace(*in.Legend)
	}
	if in.Unit != nil {
		fields["unit"] = strings.TrimSpace(*in.Unit)
	}
	if in.Ordinal != nil {
		fields["ordinal"] = *in.Ordinal
	}
	updated, err := s.repo.Update(ctx, id, fields)
	if err != nil {
		return nil, err
	}
	dbx.AfterCommit(ctx, func() { s.kickSync("update", id) })
	return updated, nil
}

// Delete removes the panel and kicks off an async Grafana mirror so the
// dashboard catches up. We sync from the post-delete list, which is why
// we re-read after the delete inside the goroutine.
func (s *Service) Delete(ctx context.Context, id uint64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	dbx.AfterCommit(ctx, func() { s.kickSync("delete", id) })
	return nil
}

// SyncNow pushes the Grafana mirror synchronously: lists the current
// user-managed panels and hands them to the syncer (which prepends the
// core fleet panels). Used at boot so the ongrid-monitor dashboard exists
// — populated with at least the core panels — before the operator makes
// the first panel edit. Best-effort: returns the syncer error so the
// caller can log it; a boot-time Grafana hiccup is non-fatal (the next
// panel edit re-syncs).
func (s *Service) SyncNow(ctx context.Context) error {
	if s.syncer == nil {
		return nil
	}
	panels, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	return s.syncer.SyncMonitorPanels(ctx, panels)
}

// kickSync starts the Grafana mirror on a detached goroutine. The
// goroutine carries its own context with a timeout so a stuck Grafana
// can't pin it forever.
//
// We pass `op` and `panelID` purely for logging; the actual sync re-
// reads the full panel list from the DB so the mirror always reflects
// the latest state, even when many edits happen in quick succession.
//
// Sync errors are persisted onto the affected panel's last_sync_error
// column (when panelID > 0) so the SPA can surface "Grafana mirror
// failed" without polling Grafana.
func (s *Service) kickSync(op string, panelID uint64) {
	if s.syncer == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.syncTO)
		defer cancel()
		panels, err := s.repo.List(ctx)
		if err != nil {
			s.log.Warn("monitor sync: list panels failed",
				slog.String("op", op),
				slog.Any("err", err),
			)
			return
		}
		if err := s.syncer.SyncMonitorPanels(ctx, panels); err != nil {
			s.log.Warn("monitor sync: grafana mirror failed",
				slog.String("op", op),
				slog.Uint64("panel_id", panelID),
				slog.Any("err", err),
			)
			if panelID != 0 {
				if perr := s.repo.SetSyncResult(ctx, panelID, truncateErr(err)); perr != nil {
					s.log.Debug("monitor sync: persist err result failed", slog.Any("err", perr))
				}
			}
			return
		}
		// Successful sync — clear last_sync_error on every existing panel.
		// Cheap (one UPDATE per row, but the table is tiny) and gives the
		// UI a deterministic "all green" signal.
		for _, p := range panels {
			if p.LastSyncError == "" {
				continue
			}
			if perr := s.repo.SetSyncResult(ctx, p.ID, ""); perr != nil {
				s.log.Debug("monitor sync: clear err result failed",
					slog.Uint64("panel_id", p.ID),
					slog.Any("err", perr))
			}
		}
		s.log.Debug("monitor sync: grafana mirror ok",
			slog.String("op", op),
			slog.Int("panel_count", len(panels)),
		)
	}()
}

// truncateErr keeps last_sync_error within the column's 512-char limit.
func truncateErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 480 {
		s = s[:480] + "…"
	}
	return s
}

// Compile-time guard: we expect the standard sentinels to remain
// importable from this package's callers.
var _ = errors.Is
