// Package flow is the workflow-orchestration biz tier (HLD-016).
// Definitions are user-authored DAGs (graph.go); the engine
// (engine.go) executes them through seams over the existing agent /
// tool / notify subsystems (nodes.go).
package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the flow-definition persistence contract.
type Repo interface {
	Create(ctx context.Context, f *model.Flow) error
	Update(ctx context.Context, f *model.Flow) error
	Get(ctx context.Context, id uint64) (*model.Flow, error)
	List(ctx context.Context, limit, offset int) ([]*model.Flow, int64, error)
	Delete(ctx context.Context, id uint64) error
}

// RunRepo persists runs + their node rows.
type RunRepo interface {
	CreateRun(ctx context.Context, r *model.FlowRun) error
	UpdateRun(ctx context.Context, r *model.FlowRun) error
	GetRun(ctx context.Context, id string) (*model.FlowRun, error)
	ListRuns(ctx context.Context, flowID uint64, limit int) ([]*model.FlowRun, error)
	CreateNode(ctx context.Context, n *model.FlowRunNode) error
	UpdateNode(ctx context.Context, n *model.FlowRunNode) error
	ListNodes(ctx context.Context, runID string) ([]*model.FlowRunNode, error)
	// SweepStaleRunning flips running/pending rows to failed — called
	// once at boot; the engine is in-process so runs don't survive a
	// restart.
	SweepStaleRunning(ctx context.Context, reason string) (int64, error)
}

// Usecase wires definitions, runs and the engine.
type Usecase struct {
	repo   Repo
	runs   RunRepo
	engine *Engine
	log    *slog.Logger
}

// NewUsecase constructs the biz facade. engine may be nil in tests
// that only exercise CRUD.
func NewUsecase(repo Repo, runs RunRepo, engine *Engine, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, runs: runs, engine: engine, log: log}
}

// CreateInput / UpdateInput are the write payloads.
type CreateInput struct {
	Name        string
	Description string
	GraphJSON   string
	CreatedBy   *uint64
}

// Create validates the graph and inserts the definition.
func (u *Usecase) Create(ctx context.Context, in CreateInput) (*model.Flow, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name required", errs.ErrInvalid)
	}
	graph := strings.TrimSpace(in.GraphJSON)
	if graph == "" {
		graph = "{}"
	}
	if _, err := ParseGraph(graph); err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	f := &model.Flow{
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		GraphJSON:   graph,
		Enabled:     true,
		Version:     1,
		CreatedBy:   in.CreatedBy,
	}
	if err := u.repo.Create(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// Update replaces name/description/graph and bumps Version when the
// graph actually changed.
func (u *Usecase) Update(ctx context.Context, id uint64, in CreateInput) (*model.Flow, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	f, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		f.Name = name
	}
	f.Description = strings.TrimSpace(in.Description)
	if graph := strings.TrimSpace(in.GraphJSON); graph != "" && graph != f.GraphJSON {
		if _, err := ParseGraph(graph); err != nil {
			return nil, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
		}
		f.GraphJSON = graph
		f.Version++
	}
	if err := u.repo.Update(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// SetEnabled toggles a flow.
func (u *Usecase) SetEnabled(ctx context.Context, id uint64, enabled bool) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	f, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	f.Enabled = enabled
	return u.repo.Update(ctx, f)
}

// Get / List / Delete are thin passthroughs.
func (u *Usecase) Get(ctx context.Context, id uint64) (*model.Flow, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.Get(ctx, id)
}

func (u *Usecase) List(ctx context.Context, limit, offset int) ([]*model.Flow, int64, error) {
	if u.repo == nil {
		return nil, 0, errs.ErrNotWiredYet
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return u.repo.List(ctx, limit, offset)
}

func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	return u.repo.Delete(ctx, id)
}

// Trigger starts a manual run. The engine executes on a background
// goroutine; the returned run row is already persisted with
// status=running so the UI can poll immediately.
func (u *Usecase) Trigger(ctx context.Context, id uint64, input map[string]any, by *uint64) (*model.FlowRun, error) {
	if u.repo == nil || u.runs == nil || u.engine == nil {
		return nil, errs.ErrNotWiredYet
	}
	f, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !f.Enabled {
		return nil, fmt.Errorf("%w: flow disabled", errs.ErrInvalid)
	}
	g, err := ParseGraph(f.GraphJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	if len(g.Triggers()) == 0 {
		return nil, fmt.Errorf("%w: graph has no trigger node", errs.ErrInvalid)
	}
	tb, _ := json.Marshal(input)
	if input == nil {
		tb = []byte("{}")
	}
	now := time.Now().UTC()
	run := &model.FlowRun{
		ID:          uuid.NewString(),
		FlowID:      f.ID,
		FlowVersion: f.Version,
		Status:      model.RunStatusRunning,
		TriggerType: model.TriggerManual,
		TriggerJSON: string(tb),
		CreatedBy:   by,
		StartedAt:   &now,
	}
	if err := u.runs.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	// Detach from the HTTP request context — the run must outlive it
	// (same rationale as the chat workCtx fix: a closed connection
	// must not cancel in-flight work).
	go func() {
		bg := context.Background()
		status, execErr := u.engine.Execute(bg, run, g)
		fin := time.Now().UTC()
		run.Status = status
		run.FinishedAt = &fin
		if execErr != nil {
			run.Error = truncate(execErr.Error(), 2000)
		}
		if err := u.runs.UpdateRun(bg, run); err != nil {
			u.log.Warn("flow run finalize failed", slog.String("run_id", run.ID), slog.Any("err", err))
		}
	}()
	return run, nil
}

// GetRun returns a run plus its node rows.
func (u *Usecase) GetRun(ctx context.Context, runID string) (*model.FlowRun, []*model.FlowRunNode, error) {
	if u.runs == nil {
		return nil, nil, errs.ErrNotWiredYet
	}
	run, err := u.runs.GetRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := u.runs.ListNodes(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	return run, nodes, nil
}

// ListRuns returns the latest runs of one flow.
func (u *Usecase) ListRuns(ctx context.Context, flowID uint64, limit int) ([]*model.FlowRun, error) {
	if u.runs == nil {
		return nil, errs.ErrNotWiredYet
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return u.runs.ListRuns(ctx, flowID, limit)
}

// HealStaleRuns sweeps running rows left by a previous process. Call
// once from main after migration.
func (u *Usecase) HealStaleRuns(ctx context.Context) {
	if u.runs == nil {
		return
	}
	n, err := u.runs.SweepStaleRunning(ctx, "manager restarted while run was in flight")
	if err != nil {
		u.log.Warn("flow stale-run sweep failed", slog.Any("err", err))
		return
	}
	if n > 0 {
		u.log.Info("flow stale runs swept", slog.Int64("count", n))
	}
}
