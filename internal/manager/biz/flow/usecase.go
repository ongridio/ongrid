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
	"os"
	"strconv"
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
	// ListEnabled returns every enabled flow (no paging) — the alert
	// dispatcher and cron scheduler scan these for matching triggers.
	ListEnabled(ctx context.Context) ([]*model.Flow, error)
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
	// PruneRuns deletes finished runs (and their node rows) created before
	// `before`, bounding history growth.
	PruneRuns(ctx context.Context, before time.Time) (int64, error)
}

// runRetention is how long finished runs are kept before PruneOldRuns
// reaps them. Overridable via ONGRID_FLOW_RUN_RETENTION_DAYS.
const defaultRunRetentionDays = 14

// Usecase wires definitions, runs and the engine.
type Usecase struct {
	repo    Repo
	runs    RunRepo
	engine  *Engine
	catalog ToolCatalog
	llm     GenLLM
	log     *slog.Logger
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

// Trigger starts a manual run (the HTTP "Run" button). Thin wrapper over
// triggerRun scoped to manual trigger nodes.
func (u *Usecase) Trigger(ctx context.Context, id uint64, input map[string]any, by *uint64) (*model.FlowRun, error) {
	return u.triggerRun(ctx, id, NodeTriggerManual, "", input, by, true)
}

// TestNode executes a single node in isolation and returns its output —
// the editor's "test run this node" affordance, so the user sees a node's
// real output (and thus its referenceable fields) before wiring it into
// the flow. Upstream {{nodes.*}} refs resolve against the flow's most
// recent run outputs; triggerInput backs {{trigger.*}}. The error is
// returned in-band (caller surfaces it in the UI, not as an HTTP failure).
func (u *Usecase) TestNode(ctx context.Context, flowID uint64, nodeType string, configJSON json.RawMessage, triggerInput map[string]any) (any, error) {
	if u.engine == nil {
		return nil, errs.ErrNotWiredYet
	}
	// Seed upstream context from the latest run so refs to already-run
	// nodes resolve; best-effort.
	nodesCtx := map[string]any{}
	if u.runs != nil && flowID > 0 {
		if runs, err := u.runs.ListRuns(ctx, flowID, 1); err == nil && len(runs) > 0 {
			if rnodes, err := u.runs.ListNodes(ctx, runs[0].ID); err == nil {
				for _, rn := range rnodes {
					var out any
					if rn.OutputJSON != "" {
						_ = json.Unmarshal([]byte(rn.OutputJSON), &out)
					}
					nodesCtx[rn.NodeID] = out
				}
			}
		}
	}
	if reason := u.testRunSideEffect(nodeType, configJSON); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	rc := &RunContext{Trigger: triggerInput, Nodes: nodesCtx, Vars: map[string]any{}}
	node := GraphNode{ID: "test", Type: nodeType, Config: configJSON}
	res, err := u.engine.RunSingle(ctx, node, rc)
	if err != nil {
		return nil, err
	}
	return res.Output, nil
}

// testRunSideEffect returns a non-empty reason when a node type must NOT
// be test-run because doing so causes a real external side effect (a
// notification actually delivered, a service actually restarted). Test-run
// exists to reveal a node's OUTPUT SHAPE for downstream references, not to
// mutate the world — side-effecting nodes are validated only when the whole
// flow runs. Read-class tool / agent / llm / condition / set / transform
// nodes stay test-runnable.
func (u *Usecase) testRunSideEffect(nodeType string, configJSON json.RawMessage) string {
	switch nodeType {
	case NodeNotify:
		return "notify node delivers a real message and cannot be test-run; run the whole flow to validate it"
	case NodeTool:
		var cfg struct {
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(configJSON, &cfg)
		if cfg.Tool == "" {
			return ""
		}
		for _, t := range u.ListTools() {
			if t.Name != cfg.Tool {
				continue
			}
			if t.Class == "write" || t.Class == "destructive" {
				return fmt.Sprintf("tool %q is %s-class and cannot be test-run because it changes real state; run the whole flow to execute it", cfg.Tool, t.Class)
			}
			break
		}
	}
	return ""
}

// ListEnabledFlows returns every enabled flow — the dispatcher /
// scheduler scan source.
func (u *Usecase) ListEnabledFlows(ctx context.Context) ([]*model.Flow, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.ListEnabled(ctx)
}

// TriggerEvent starts a run from a non-manual source (alert dispatcher /
// cron scheduler). entryType and entryNodeID identify the matched trigger;
// payload becomes {{trigger.*}}.
// Returns ErrInvalid when the selected node was removed or changed after
// the caller scanned it, so another branch is never started instead.
func (u *Usecase) TriggerEvent(ctx context.Context, flowID uint64, entryType, entryNodeID string, payload map[string]any) (*model.FlowRun, error) {
	if entryNodeID == "" {
		return nil, fmt.Errorf("%w: trigger node id required", errs.ErrInvalid)
	}
	return u.triggerRun(ctx, flowID, entryType, entryNodeID, payload, nil, false)
}

// triggerRun is the shared run-launch core. requireEnabled distinguishes
// the manual path (surfaces "flow disabled" as a user error) from event
// paths (caller already filtered to enabled flows; a disabled flow here
// is a benign race → skip).
func (u *Usecase) triggerRun(ctx context.Context, id uint64, entryType, entryNodeID string, input map[string]any, by *uint64, requireEnabled bool) (*model.FlowRun, error) {
	if u.repo == nil || u.runs == nil || u.engine == nil {
		return nil, errs.ErrNotWiredYet
	}
	f, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !f.Enabled {
		if requireEnabled {
			return nil, fmt.Errorf("%w: flow disabled", errs.ErrInvalid)
		}
		return nil, nil // event path: benign skip
	}
	g, err := ParseGraph(f.GraphJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrInvalid, err)
	}
	// Confirm the requested entry trigger exists in this graph.
	hasEntry := false
	for _, t := range g.Triggers() {
		if t.Type == entryType && (entryNodeID == "" || t.ID == entryNodeID) {
			hasEntry = true
			break
		}
	}
	if !hasEntry {
		if entryNodeID == "" {
			return nil, fmt.Errorf("%w: graph has no %s trigger", errs.ErrInvalid, entryType)
		}
		return nil, fmt.Errorf("%w: graph has no %s trigger node %q", errs.ErrInvalid, entryType, entryNodeID)
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
		TriggerType: entryType,
		TriggerJSON: string(tb),
		CreatedBy:   by,
		StartedAt:   &now,
	}
	if err := u.runs.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	// Detach from the request context — the run must outlive it (same
	// rationale as the chat workCtx fix: a closed connection must not
	// cancel in-flight work).
	go func() {
		bg := context.Background()
		status, execErr := u.engine.Execute(bg, run, g, entryType, entryNodeID)
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

// PruneOldRuns reaps finished runs older than the retention window. Safe
// to call periodically (the scheduler does) and at boot.
func (u *Usecase) PruneOldRuns(ctx context.Context) {
	if u.runs == nil {
		return
	}
	days := defaultRunRetentionDays
	if v := strings.TrimSpace(os.Getenv("ONGRID_FLOW_RUN_RETENTION_DAYS")); v != "" {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			days = d
		}
	}
	before := time.Now().UTC().AddDate(0, 0, -days)
	n, err := u.runs.PruneRuns(ctx, before)
	if err != nil {
		u.log.Warn("flow run prune failed", slog.Any("err", err))
		return
	}
	if n > 0 {
		u.log.Info("flow old runs pruned", slog.Int64("count", n), slog.Int("retention_days", days))
	}
}
