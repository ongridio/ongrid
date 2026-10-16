package aiops

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// WorkerRuntime is the execution contract shared by all worker consumers.
// Persona and tool catalog mutation stay with version-specific assembly.
type WorkerRuntime interface {
	SpawnWorker(context.Context, contract.SpawnRequest) (*contract.Worker, error)
	SendToWorker(context.Context, string, string) error
	StopWorker(context.Context, string) error
	GetWorker(string) (*contract.Worker, bool)
}

type RuntimeEngine interface {
	RuntimeHandler
	WorkerRuntime
	CountWorkersByStatus() (running, pending int)
}

// RuntimeSessionReader supplies durable execution ownership to the router.
type RuntimeSessionReader interface {
	GetSession(context.Context, string) (*model.Session, error)
}

// RuntimeRouter owns execution dispatch, never tool or persona policy.
// Its registry and default are immutable; historical-session classification
// is still required before exposing a live default-version setting.
type RuntimeRouter struct {
	sessions       RuntimeSessionReader
	engines        map[contract.Version]RuntimeEngine
	defaultVersion contract.Version
}

func NewRuntimeRouter(sessions RuntimeSessionReader, defaultVersion contract.Version, engines map[contract.Version]RuntimeEngine) (*RuntimeRouter, error) {
	copy := make(map[contract.Version]RuntimeEngine, len(engines))
	for version, engine := range engines {
		if version != contract.V1 && version != contract.V2 && version != contract.V3 {
			return nil, fmt.Errorf("%w: unknown runtime version %q", errs.ErrInvalid, version)
		}
		if engine == nil || (reflect.ValueOf(engine).Kind() == reflect.Ptr && reflect.ValueOf(engine).IsNil()) {
			return nil, fmt.Errorf("%w: runtime %s is nil", errs.ErrNotWiredYet, version)
		}
		copy[version] = engine
	}
	if copy[defaultVersion] == nil {
		return nil, fmt.Errorf("%w: default runtime %q unavailable", errs.ErrNotWiredYet, defaultVersion)
	}
	return &RuntimeRouter{sessions: sessions, engines: copy, defaultVersion: defaultVersion}, nil
}

func (r *RuntimeRouter) resolve(ctx context.Context) (context.Context, RuntimeEngine, contract.Version, error) {
	if r == nil {
		return ctx, nil, "", errs.ErrNotWiredYet
	}
	version := contract.VersionFromContext(ctx)
	if version == "" {
		version = r.defaultVersion
	}
	engine := r.engines[version]
	if engine == nil {
		return ctx, nil, version, fmt.Errorf("%w: runtime %q unavailable", errs.ErrNotWiredYet, version)
	}
	return contract.WithVersion(ctx, version), engine, version, nil
}

// SessionVersion resolves new-session policy once, before persistence.
func (r *RuntimeRouter) SessionVersion(ctx context.Context) (contract.Version, error) {
	_, _, version, err := r.resolve(ctx)
	return version, err
}

// sessionContext honors durable ownership. Empty historical values retain
// the current single-version behavior until a provenance migration is ready.
func (r *RuntimeRouter) sessionContext(ctx context.Context, id string) (context.Context, error) {
	if r == nil {
		return ctx, errs.ErrNotWiredYet
	}
	if r.sessions == nil || id == "" {
		return ctx, nil
	}
	session, err := r.sessions.GetSession(ctx, id)
	if err != nil {
		return ctx, fmt.Errorf("runtime session: %w", err)
	}
	if session == nil {
		return ctx, errs.ErrNotFound
	}
	if session.RuntimeVersion == "" {
		return ctx, nil
	}
	version := contract.Version(session.RuntimeVersion)
	if requested := contract.VersionFromContext(ctx); requested != "" && requested != version {
		return ctx, fmt.Errorf("%w: session runtime mismatch", errs.ErrInvalid)
	}
	return contract.WithVersion(ctx, version), nil
}

func (r *RuntimeRouter) Handle(ctx context.Context, req *contract.Request) (*contract.Reply, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request required", errs.ErrInvalid)
	}
	ctx, err := r.sessionContext(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	ctx, engine, _, err := r.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return engine.Handle(ctx, req)
}

func (r *RuntimeRouter) SpawnWorker(ctx context.Context, req contract.SpawnRequest) (*contract.Worker, error) {
	ctx, err := r.sessionContext(ctx, req.ParentSession)
	if err != nil {
		return nil, err
	}
	ctx, engine, version, err := r.resolve(ctx)
	if err != nil {
		return nil, err
	}
	worker, err := engine.SpawnWorker(ctx, req)
	if err != nil {
		return nil, err
	}
	if worker == nil || worker.ID == "" {
		return nil, fmt.Errorf("runtime %s returned no worker identity", version)
	}
	return worker, nil
}

func (r *RuntimeRouter) workerEngine(workerID string) (RuntimeEngine, contract.Version, bool) {
	if r == nil {
		return nil, "", false
	}
	var owner RuntimeEngine
	var version contract.Version
	// Resolve against the owning runtime, including workers created by its
	// reviewer path. Do not duplicate its mutable lifecycle registry here.
	for candidate, engine := range r.engines {
		if _, found := engine.GetWorker(workerID); !found {
			continue
		}
		if owner != nil {
			return nil, "", false
		} // ambiguous ID: never pick a version arbitrarily
		owner, version = engine, candidate
	}
	return owner, version, owner != nil
}

func (r *RuntimeRouter) SendToWorker(ctx context.Context, workerID, message string) error {
	engine, version, ok := r.workerEngine(workerID)
	if !ok {
		return fmt.Errorf("%w: worker %q", errs.ErrNotFound, workerID)
	}
	if requested := contract.VersionFromContext(ctx); requested != "" && requested != version {
		return fmt.Errorf("%w: worker runtime mismatch", errs.ErrInvalid)
	}
	return engine.SendToWorker(contract.WithVersion(ctx, version), workerID, message)
}

func (r *RuntimeRouter) StopWorker(ctx context.Context, workerID string) error {
	engine, version, ok := r.workerEngine(workerID)
	if !ok {
		return fmt.Errorf("%w: worker %q", errs.ErrNotFound, workerID)
	}
	if requested := contract.VersionFromContext(ctx); requested != "" && requested != version {
		return fmt.Errorf("%w: worker runtime mismatch", errs.ErrInvalid)
	}
	return engine.StopWorker(contract.WithVersion(ctx, version), workerID)
}

func (r *RuntimeRouter) GetWorker(workerID string) (*contract.Worker, bool) {
	engine, _, ok := r.workerEngine(workerID)
	if !ok {
		return nil, false
	}
	return engine.GetWorker(workerID)
}

func (r *RuntimeRouter) CountWorkersByStatus() (running, pending int) {
	if r == nil {
		return 0, 0
	}
	for _, engine := range r.engines {
		active, waiting := engine.CountWorkersByStatus()
		running += active
		pending += waiting
	}
	return
}
