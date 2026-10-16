// Package approval is the biz tier for the human propose-confirm inbox
// (HLD-017). Producers (agent cloud-shell, restart_service, flow approval
// nodes) call Propose to queue a dangerous action; a human Approves/Rejects
// in the inbox UI; on Approve the registered executor for that Kind runs the
// action and the result is recorded. Strictly additive — nothing executes
// here unless a producer explicitly proposed it.
package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the persistence contract.
type Repo interface {
	Create(ctx context.Context, a *model.Approval) error
	Get(ctx context.Context, id string) (*model.Approval, error)
	List(ctx context.Context, status string, limit int) ([]*model.Approval, error)
	ListKind(ctx context.Context, kind string, limit, offset int) ([]*model.Approval, error)
	CountPending(ctx context.Context) (int64, error)
	Decide(ctx context.Context, id string, fields map[string]any) error
	SetResult(ctx context.Context, id, status, resultJSON string, executedAt time.Time) error
}

// Executor runs an approved action's payload and returns a result blob.
// Registered per Kind by the producer (e.g. cloud-shell registers
// "shell_command"). Missing executors fail the action explicitly; historical
// approval records never imply that an unavailable action was executed.
type Executor func(ctx context.Context, payloadJSON string) (resultJSON string, err error)

type executorKey struct{ kind, runtimeVersion string }

// Usecase is the inbox facade.
type Usecase struct {
	executorMu sync.RWMutex
	repo       Repo
	log        *slog.Logger
	executors  map[executorKey]Executor
}

// NewUsecase wires the repo.
func NewUsecase(repo Repo, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, log: log, executors: map[executorKey]Executor{}}
}

// RegisterExecutor wires the execute-on-approve handler for a Kind. Called
// at boot by the producer subsystem (e.g. cloud-shell). Idempotent.
func (u *Usecase) RegisterExecutor(kind string, fn Executor) {
	u.RegisterRuntimeExecutor(kind, "", fn)
}

// RegisterRuntimeExecutor registers an exact (kind, runtime) execution route.
// Versioned proposals never fall back to an unversioned or newer executor.
func (u *Usecase) RegisterRuntimeExecutor(kind, runtimeVersion string, fn Executor) {
	u.executorMu.Lock()
	defer u.executorMu.Unlock()
	u.executors[executorKey{kind: kind, runtimeVersion: runtimeVersion}] = fn
}

// ProposeInput is what a producer queues.
type ProposeInput struct {
	RuntimeVersion string
	Kind           string
	Title          string
	Summary        string
	Payload        any    // marshaled to PayloadJSON
	Source         string // SourceAgent / SourceFlow
	SessionID      string
	ProposedBy     uint64
}

// Propose records a pending action. Producer-facing (not admin-gated — the
// producer already ran under the caller's auth).
func (u *Usecase) Propose(ctx context.Context, in ProposeInput) (*model.Approval, error) {
	if strings.TrimSpace(in.Kind) == "" || strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: kind + title required", errs.ErrInvalid)
	}
	if len(in.RuntimeVersion) > 16 {
		return nil, fmt.Errorf("%w: runtime version too long", errs.ErrInvalid)
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, err
	}
	src := in.Source
	if src == "" {
		src = model.SourceAgent
	}
	a := &model.Approval{
		RuntimeVersion: in.RuntimeVersion,
		Kind:           in.Kind, Title: in.Title, Summary: in.Summary,
		PayloadJSON: string(payload), Source: src, SessionID: in.SessionID,
		Status: model.StatusPending, ProposedBy: in.ProposedBy,
	}
	if err := u.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	u.log.Info("approval proposed", slog.String("id", a.ID), slog.String("kind", a.Kind), slog.String("title", a.Title))
	return a, nil
}

// List / Get / CountPending — inbox reads.
func (u *Usecase) List(ctx context.Context, status string, limit int) ([]*model.Approval, error) {
	return u.repo.List(ctx, status, limit)
}
func (u *Usecase) ListKind(ctx context.Context, kind string, limit, offset int) ([]*model.Approval, error) {
	return u.repo.ListKind(ctx, kind, limit, offset)
}
func (u *Usecase) Get(ctx context.Context, id string) (*model.Approval, error) {
	return u.repo.Get(ctx, id)
}
func (u *Usecase) CountPending(ctx context.Context) (int64, error) { return u.repo.CountPending(ctx) }

// Approve marks the proposal approved and, if an executor is registered for
// its Kind, runs the action and records the result. Only a pending row can
// be approved (the repo guards against double-decisions).
func (u *Usecase) Approve(ctx context.Context, approverID uint64, id string) (*model.Approval, error) {
	now := time.Now().UTC()
	if err := u.repo.Decide(ctx, id, map[string]any{
		"status": model.StatusApproved, "approved_by": approverID, "decided_at": now,
	}); err != nil {
		return nil, err
	}
	a, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	u.executorMu.RLock()
	exec := u.executors[executorKey{kind: a.Kind, runtimeVersion: a.RuntimeVersion}]
	u.executorMu.RUnlock()
	var res string
	var runErr error
	if exec == nil {
		runErr = fmt.Errorf("approval executor for %q runtime %q is unavailable; submit a new request", a.Kind, a.RuntimeVersion)
	} else {
		res, runErr = exec(ctx, a.PayloadJSON)
	}
	status := model.StatusExecuted
	if runErr != nil {
		status = model.StatusFailed
		payload, err := json.Marshal(struct {
			Error string `json:"error"`
		}{Error: runErr.Error()})
		if err != nil {
			return nil, fmt.Errorf("encode approval failure: %w", err)
		}
		res = string(payload)
	}
	if err := u.repo.SetResult(ctx, id, status, res, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("persist approval execution result: %w", err)
	}
	a, err = u.repo.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read completed approval: %w", err)
	}
	return a, nil
}

// Reject marks the proposal rejected with a reason. No execution.
func (u *Usecase) Reject(ctx context.Context, approverID uint64, id, reason string) error {
	now := time.Now().UTC()
	return u.repo.Decide(ctx, id, map[string]any{
		"status": model.StatusRejected, "approved_by": approverID,
		"reason": strings.TrimSpace(reason), "decided_at": now,
	})
}

// InvalidatePending retires a proposal whose originating request can no longer
// execute. The conditional decision never overwrites approval or rejection.
func (u *Usecase) InvalidatePending(ctx context.Context, id, reason string) error {
	result, err := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: reason})
	if err != nil {
		return fmt.Errorf("encode invalidated approval: %w", err)
	}
	err = u.repo.Decide(ctx, id, map[string]any{"status": model.StatusFailed, "reason": reason, "result_json": string(result), "decided_at": time.Now().UTC()})
	if errors.Is(err, errs.ErrNotFound) {
		row, readErr := u.repo.Get(ctx, id)
		if readErr != nil {
			return fmt.Errorf("read invalidated approval: %w", readErr)
		}
		if row.Status != model.StatusPending {
			return nil
		}
	}
	if err != nil {
		return fmt.Errorf("invalidate pending approval: %w", err)
	}
	return nil
}
