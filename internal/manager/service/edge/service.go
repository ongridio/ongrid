// Package edge is the manager/edge service-layer: HTTP request validation,
// error mapping, delegation to biz/edge.Usecase. Never imports data/.
package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/edge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// EdgeCaller is the narrow surface this service uses to dispatch
// cloud→edge RPCs (currently agent_upgrade). Implemented by
// frontierbound.Client.Call. Mirrors skill.Service / aiops.Caller so
// the test seam is consistent across services.
type EdgeCaller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

type ManagedEdgeGuard interface {
	ManagedClusterIDForEdge(ctx context.Context, edgeID uint64) (clusterID uint64, managed bool, err error)
}

// Service is the HTTP-facing shim over biz.Usecase. Keeps handlers
// reasonably thin: validation + DTO translation in the HTTP layer, business
// logic (key generation, hashing, soft-delete semantics) in biz.
type Service struct {
	uc     *biz.Usecase
	caller EdgeCaller // nil disables UpgradeAgent (returns ErrNotWiredYet)
	guard  ManagedEdgeGuard
	log    *slog.Logger
}

// New builds the Service. log may be nil. caller may be nil — handlers
// that depend on it (UpgradeAgent) will return a typed not-wired error
// until SetEdgeCaller back-fills it. The caller is wired post-hoc
// because frontierbound.Client is constructed later in main than
// edgeSvc — same pattern pluginConfigUC uses.
func New(uc *biz.Usecase, caller EdgeCaller, log *slog.Logger) *Service {
	return &Service{uc: uc, caller: caller, log: log}
}

// SetEdgeCaller back-fills the cloud→edge dispatcher after main has
// built frontierbound.Client. Safe to call before any HTTP traffic
// hits — wiring happens during startup before HTTP servers listen.
func (s *Service) SetEdgeCaller(c EdgeCaller)             { s.caller = c }
func (s *Service) SetManagedEdgeGuard(g ManagedEdgeGuard) { s.guard = g }

// Create delegates to biz.Usecase.Create. The plaintext SecretKey in the
// returned CreateResult must be echoed back to the caller ONCE; it is not
// stored anywhere the API can retrieve it later.
func (s *Service) Create(ctx context.Context, name string, createdBy *uint64) (*biz.CreateResult, error) {
	return s.uc.Create(ctx, name, createdBy)
}

// List returns edges matching filter.
func (s *Service) List(ctx context.Context, f biz.ListFilter) ([]*model.Edge, error) {
	return s.uc.List(ctx, f)
}

// Get returns one edge by id.
func (s *Service) Get(ctx context.Context, id uint64) (*model.Edge, error) {
	return s.uc.Get(ctx, id)
}

// Delete soft-deletes an edge.
func (s *Service) Delete(ctx context.Context, id uint64) error {
	if err := s.rejectManagedMutation(ctx, id); err != nil {
		return err
	}
	return s.uc.Delete(ctx, id)
}

func (s *Service) DeleteManaged(ctx context.Context, id uint64) error {
	return s.uc.Delete(ctx, id)
}

// RotateSecret generates + stores a new hash, returns plaintext ONCE.
func (s *Service) RotateSecret(ctx context.Context, id uint64) (string, error) {
	if err := s.rejectManagedMutation(ctx, id); err != nil {
		return "", err
	}
	return s.uc.RotateSecret(ctx, id)
}

func (s *Service) RotateManagedSecret(ctx context.Context, id uint64) (string, error) {
	return s.uc.RotateSecret(ctx, id)
}

// HandleRegister is the tunnel-side entrypoint for the register_edge RPC.
// Thin passthrough to biz.Usecase.HandleRegister.
func (s *Service) HandleRegister(ctx context.Context, edgeID uint64, info tunnel.HostInfo, agentVersion string) error {
	return s.uc.HandleRegister(ctx, edgeID, info, agentVersion)
}

// HandleHeartbeat is the tunnel-side entrypoint for the heartbeat RPC.
// Thin passthrough to biz.Usecase.HandleHeartbeat.
func (s *Service) HandleHeartbeat(ctx context.Context, edgeID uint64, ts time.Time) error {
	return s.uc.HandleHeartbeat(ctx, edgeID, ts)
}

// PluginHealth returns the last-reported per-plugin runtime health for one
// edge (in-memory, fed by the heartbeat path). nil when none reported yet.
func (s *Service) PluginHealth(edgeID uint64) []biz.PluginHealth {
	return s.uc.PluginHealth(edgeID)
}

// GetProcessList dispatches the get_process_list RPC to a connected
// edge. edge agent enumerates the host via gopsutil and returns the
// top N processes sorted by cpu or mem. Used by the Monitor page's
// per-device process panel — same RPC the LLM tool get_host_processes
// uses, just exposed over HTTP.
func (s *Service) GetProcessList(ctx context.Context, edgeID uint64, topN uint32, sortBy string) (tunnel.GetProcessListResponse, error) {
	if s.caller == nil {
		return tunnel.GetProcessListResponse{}, fmt.Errorf("processes not wired: no edge caller configured")
	}
	body, err := json.Marshal(tunnel.GetProcessListRequest{TopN: topN, SortBy: sortBy})
	if err != nil {
		return tunnel.GetProcessListResponse{}, fmt.Errorf("processes: marshal: %w", err)
	}
	respBytes, err := s.caller.Call(ctx, edgeID, tunnel.MethodGetProcessList, body)
	if err != nil {
		return tunnel.GetProcessListResponse{}, err
	}
	var resp tunnel.GetProcessListResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return tunnel.GetProcessListResponse{}, fmt.Errorf("processes: unmarshal resp: %w", err)
	}
	return resp, nil
}

// UpgradeAgent dispatches the agent_upgrade RPC to a connected edge.
// The edge stages the binary at URL after sha256 verification; the
// actual swap happens on the next process restart via the systemd
// ExecStartPre script. Caller (HTTP handler) is responsible for
// resolving the URL from a target version + matching architecture.
//
// Returns the edge's response (StagedPath + Bytes) or a wrapped error
// — most failures are tunnel-level "edge offline" or remote
// validation errors (sha256 mismatch, bad URL).
func (s *Service) UpgradeAgent(ctx context.Context, edgeID uint64, url, sha256 string) (tunnel.AgentUpgradeResponse, error) {
	if err := s.rejectManagedMutation(ctx, edgeID); err != nil {
		return tunnel.AgentUpgradeResponse{}, err
	}
	if s.caller == nil {
		return tunnel.AgentUpgradeResponse{}, fmt.Errorf("upgrade not wired: no edge caller configured")
	}
	body, err := json.Marshal(tunnel.AgentUpgradeRequest{URL: url, SHA256: sha256})
	if err != nil {
		return tunnel.AgentUpgradeResponse{}, fmt.Errorf("upgrade: marshal: %w", err)
	}
	respBytes, err := s.caller.Call(ctx, edgeID, tunnel.MethodAgentUpgrade, body)
	if err != nil {
		return tunnel.AgentUpgradeResponse{}, err
	}
	var resp tunnel.AgentUpgradeResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return tunnel.AgentUpgradeResponse{}, fmt.Errorf("upgrade: unmarshal resp: %w", err)
	}
	return resp, nil
}

// FetchPackage / ApplyPackage dispatch the two halves of the
// integer-bundle upgrade. Manager wires URL+sha auto-resolution in the
// HTTP handler so admins click one button instead of typing a 64-char
// hash.
func (s *Service) FetchPackage(ctx context.Context, edgeID uint64, url, sha256, version string) (tunnel.FetchPackageResponse, error) {
	if err := s.rejectManagedMutation(ctx, edgeID); err != nil {
		return tunnel.FetchPackageResponse{}, err
	}
	if s.caller == nil {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("upgrade not wired: no edge caller configured")
	}
	body, err := json.Marshal(tunnel.FetchPackageRequest{URL: url, SHA256: sha256, Version: version})
	if err != nil {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: marshal: %w", err)
	}
	respBytes, err := s.caller.Call(ctx, edgeID, tunnel.MethodFetchPackage, body)
	if err != nil {
		return tunnel.FetchPackageResponse{}, err
	}
	var resp tunnel.FetchPackageResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return tunnel.FetchPackageResponse{}, fmt.Errorf("fetch_package: unmarshal resp: %w", err)
	}
	return resp, nil
}

func (s *Service) ApplyPackage(ctx context.Context, edgeID uint64) (tunnel.ApplyPackageResponse, error) {
	if err := s.rejectManagedMutation(ctx, edgeID); err != nil {
		return tunnel.ApplyPackageResponse{}, err
	}
	if s.caller == nil {
		return tunnel.ApplyPackageResponse{}, fmt.Errorf("upgrade not wired: no edge caller configured")
	}
	body, err := json.Marshal(tunnel.ApplyPackageRequest{})
	if err != nil {
		return tunnel.ApplyPackageResponse{}, fmt.Errorf("apply_package: marshal: %w", err)
	}
	respBytes, err := s.caller.Call(ctx, edgeID, tunnel.MethodApplyPackage, body)
	if err != nil {
		return tunnel.ApplyPackageResponse{}, err
	}
	var resp tunnel.ApplyPackageResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return tunnel.ApplyPackageResponse{}, fmt.Errorf("apply_package: unmarshal resp: %w", err)
	}
	return resp, nil
}

func (s *Service) rejectManagedMutation(ctx context.Context, edgeID uint64) error {
	if s.guard == nil {
		return nil
	}
	clusterID, managed, err := s.guard.ManagedClusterIDForEdge(ctx, edgeID)
	if err != nil {
		return fmt.Errorf("check managed edge: %w", err)
	}
	if managed {
		return fmt.Errorf("kubernetes-managed edge belongs to cluster %d; use the Kubernetes cluster operation: %w", clusterID, errs.ErrConflict)
	}
	return nil
}
