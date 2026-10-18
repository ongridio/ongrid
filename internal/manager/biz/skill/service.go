// Package skill is the manager-side skill orchestration layer. It
// turns the shared registry (internal/skill) into operator-facing
// affordances:
//   - List/Get HTTP-friendly metadata
//   - Execute by dispatching the cloud->edge MethodExecuteSkill RPC
//   - Permission gating (Class -> caller role policy)
//   - Audit log
//
// The on-edge implementation runs through internal/edgeagent/skill —
// the manager never executes skill bodies in-process; it only
// dispatches and records the round-trip.
package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

// Caller is the narrow auth context the service needs. Mirrors
// service/alert.Caller so the package isn't tightly coupled to iam.
type Caller struct {
	UserID uint64
	Role   string // "admin" | "user"
}

// EdgeCaller is the narrow surface the service needs to dispatch the
// cloud->edge RPC. The frontierbound.Client value satisfies it via the
// Call method on (Client).Call(ctx, edgeID, method, body).
type EdgeCaller interface {
	Call(ctx context.Context, edgeID uint64, method string, body []byte) ([]byte, error)
}

// AuditSink records each skill invocation for compliance / replay.
// nil sink disables audit (used in tests).
type AuditSink interface {
	Record(ctx context.Context, ev AuditEvent) error
}

// AuditEvent is one row in the skill_executions audit log.
type AuditEvent struct {
	SkillKey   string
	EdgeID     uint64
	DeviceID   uint64
	CallerID   uint64
	CallerRole string
	Class      skillcore.Class
	Params     json.RawMessage
	Result     json.RawMessage
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Service is the application-layer entry point HTTP / AI tool registry
// consume. It is stateless apart from its dependencies; safe for
// concurrent use.
type Service struct {
	hosts  HostRouter
	caller EdgeCaller
	audit  AuditSink
	log    *slog.Logger
	// extra optionally supplies catalog entries from OUTSIDE skillcore —
	// specifically the chatruntime SkillRegistry's SKILL.md skills (built-in
	// + marketplace-installed), which live in a separate registry and would
	// otherwise be invisible in the catalog. Wired in main.go (HLD-017).
	extra func() []SkillSummary
}

// New builds the service with the given edge caller (typically the
// frontierbound.Client) and audit sink (may be nil).
func New(caller EdgeCaller, audit AuditSink, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{caller: caller, audit: audit, log: log}
}

// HostRouter provides explicit bidirectional identity resolution.
type HostRouter interface {
	LookupEdgeForDevice(context.Context, uint64) (uint64, error)
	LookupHostDevice(context.Context, uint64) (uint64, error)
}

// WithHostRouter is wired once at construction before serving requests.
func (s *Service) WithHostRouter(hosts HostRouter) *Service { s.hosts = hosts; return s }

func (s *Service) resolveHostTarget(ctx context.Context, in ExecuteInput) (uint64, uint64, error) {
	if in.DeviceID == 0 && in.EdgeID == 0 {
		return 0, 0, fmt.Errorf("%w: device_id required", errs.ErrInvalid)
	}
	if s.hosts == nil {
		return 0, 0, errs.ErrNotWiredYet
	}
	hostID := in.DeviceID
	if in.EdgeID != 0 {
		linked, err := s.hosts.LookupHostDevice(ctx, in.EdgeID)
		if err != nil {
			return 0, 0, fmt.Errorf("resolve edge host: %w", err)
		}
		if hostID != 0 && hostID != linked {
			return 0, 0, fmt.Errorf("%w: host/edge mismatch", errs.ErrInvalid)
		}
		hostID = linked
	}
	if hostID == 0 {
		return 0, 0, errs.ErrNotFound
	}
	edgeID, err := s.hosts.LookupEdgeForDevice(ctx, hostID)
	if err != nil {
		return 0, 0, fmt.Errorf("resolve current host edge: %w", err)
	}
	if edgeID == 0 {
		return 0, 0, errs.ErrNotFound
	}
	if in.EdgeID != 0 && in.EdgeID != edgeID {
		return 0, 0, fmt.Errorf("%w: superseded edge installation", errs.ErrInvalid)
	}
	reverse, err := s.hosts.LookupHostDevice(ctx, edgeID)
	if err != nil {
		return 0, 0, fmt.Errorf("verify edge host: %w", err)
	}
	if reverse != hostID {
		return 0, 0, fmt.Errorf("%w: host/edge mismatch", errs.ErrInvalid)
	}
	return hostID, edgeID, nil
}

// WithExtraSkills wires a provider of catalog entries not in skillcore
// (chatruntime SKILL.md skills). Returns the service for chaining.
func (s *Service) WithExtraSkills(fn func() []SkillSummary) *Service {
	s.extra = fn
	return s
}

// SkillSummary is the DTO used for the listing endpoint and for the
// detail endpoint (it carries enough to render the UI form). The full
// Param schema is included so the SPA doesn't need a separate /params
// endpoint.
type SkillSummary struct {
	Key           string          `json:"key"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Class         skillcore.Class `json:"class"`
	Scope         skillcore.Scope `json:"scope,omitempty"`
	Category      string          `json:"category,omitempty"`
	Params        []SkillParamDef `json:"params"`
	ResultPreview string          `json:"result_preview,omitempty"`
	// Source tags where the entry came from for the catalog UI:
	// "" / "builtin" = shipped; "marketplace" = installed SKILL.md pack;
	// "git"/"tarball"/"local" carry the install origin. Lets the UI badge
	// installed skills distinctly from built-ins.
	Source string `json:"source,omitempty"`
	// InventoryOnly flags skills that are listed for visibility but have
	// no auto-renderable form — i.e. their schema lives only in raw JSON
	// Schema (RawSchemaProvider) without a declarative ParamSchema. The
	// SPA renders these as "use via AI chat" instead of an executable
	// form, since the LLM is the intended caller and a hand-filled empty
	// {} would just fail required-field validation on the inner BaseTool.
	InventoryOnly bool `json:"inventory_only,omitempty"`
}

// SkillParamDef mirrors skillcore.ParamDef for JSON. Default is left as
// any so the UI can render numeric / string / bool defaults without
// re-typing.
type SkillParamDef struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Default  any      `json:"default,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

// List returns the full registry, optionally filtered by category.
func (s *Service) List(_ context.Context, _ Caller, category string) []SkillSummary {
	all := skillcore.All()
	out := make([]SkillSummary, 0, len(all))
	for _, e := range all {
		m := e.Metadata()
		if category != "" && m.Category != category {
			continue
		}
		out = append(out, toSummary(e))
	}
	// Merge chatruntime SKILL.md skills (built-in + marketplace-installed)
	// so the catalog shows them too — they live in a separate registry.
	if s.extra != nil {
		seen := make(map[string]struct{}, len(out))
		for _, x := range out {
			seen[x.Key] = struct{}{}
		}
		for _, x := range s.extra() {
			if _, dup := seen[x.Key]; dup {
				continue
			}
			if category != "" && x.Category != category {
				continue
			}
			out = append(out, x)
		}
	}
	return out
}

// Get returns one skill's metadata. Returns errs.ErrNotFound when
// the key is unknown so the HTTP layer can render 404.
func (s *Service) Get(_ context.Context, _ Caller, key string) (*SkillSummary, error) {
	exec, ok := skillcore.Get(key)
	if !ok {
		return nil, errs.ErrNotFound
	}
	sum := toSummary(exec)
	return &sum, nil
}

// ExecuteInput is the body of an Execute call.
type ExecuteInput struct {
	Key      string
	DeviceID uint64
	EdgeID   uint64
	Params   json.RawMessage
}

// ExecuteOutput is the response. Result is the JSON the skill returned;
// Error is non-empty when the skill returned an error (the RPC itself
// succeeded — error came from inside skill.Execute).
type ExecuteOutput struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Execute dispatches a skill and records audit. Routing depends on the
// skill's Scope:
//
//   - ScopeHost (default): manager round-trips a tunnel
//     MethodExecuteSkill RPC to in.EdgeID; skill body runs on the
//     edge agent. Requires in.EdgeID != 0.
//   - ScopeManager: manager runs the skill body in-process via the
//     local Executor. EdgeID is ignored (allowed to be 0). Used by
//     web_search and the subprocess skill runtime.
//
// Permission policy (PR-G1 minimum):
//   - safe: any authenticated caller
//   - mutating: admin only
//   - dangerous: admin only AND requires SOP signature (not yet wired —
//     we reject for now until PR-G4 lands signing)
func (s *Service) Execute(ctx context.Context, caller Caller, in ExecuteInput) (*ExecuteOutput, error) {
	if in.Key == "" {
		return nil, fmt.Errorf("%w: skill key required", errs.ErrInvalid)
	}
	exec, ok := skillcore.Get(in.Key)
	if !ok {
		return nil, errs.ErrNotFound
	}
	meta := exec.Metadata()

	if err := authorize(meta.EffectiveClass(), caller.Role); err != nil {
		return nil, err
	}

	scope := meta.EffectiveScope()
	if scope == skillcore.ScopeHost {
		hostID, edgeID, err := s.resolveHostTarget(ctx, in)
		if err != nil {
			return nil, err
		}
		in.DeviceID, in.EdgeID = hostID, edgeID
		if s.caller == nil {
			return nil, errs.ErrNotWiredYet
		}
	}

	startedAt := time.Now().UTC()
	out := &ExecuteOutput{}
	var callErr error

	switch scope {
	case skillcore.ScopeManager:
		// In-process dispatch. The Executor (e.g. webSearchSkill,
		// SubprocessSkill) lives in the manager binary and returns
		// (result, err); error becomes ExecuteOutput.Error so the
		// caller still sees structured output.
		result, execErr := exec.Execute(ctx, in.Params)
		if execErr != nil {
			out.Error = execErr.Error()
		} else {
			out.Result = result
		}
	default:
		body, _ := json.Marshal(struct {
			Key    string          `json:"key"`
			Params json.RawMessage `json:"params,omitempty"`
		}{Key: in.Key, Params: in.Params})

		var resp []byte
		resp, callErr = s.caller.Call(ctx, in.EdgeID, tunnel.MethodExecuteSkill, body)
		if callErr != nil {
			out.Error = callErr.Error()
		} else {
			var wire struct {
				Result json.RawMessage `json:"result,omitempty"`
				Error  string          `json:"error,omitempty"`
			}
			if err := json.Unmarshal(resp, &wire); err != nil {
				out.Error = "decode skill response: " + err.Error()
			} else {
				out.Result = wire.Result
				out.Error = wire.Error
			}
		}
	}

	finishedAt := time.Now().UTC()

	if s.audit != nil {
		if err := s.audit.Record(ctx, AuditEvent{
			SkillKey:   in.Key,
			EdgeID:     in.EdgeID,
			DeviceID:   in.DeviceID,
			CallerID:   caller.UserID,
			CallerRole: caller.Role,
			Class:      meta.EffectiveClass(),
			Params:     in.Params,
			Result:     out.Result,
			Error:      out.Error,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
		}); err != nil {
			s.log.Warn("skill: audit record failed",
				slog.String("skill", in.Key),
				slog.Uint64("edge_id", in.EdgeID),
				slog.Any("err", err),
			)
		}
	}

	if callErr != nil {
		return out, fmt.Errorf("execute skill %q on edge %d: %w", in.Key, in.EdgeID, callErr)
	}
	return out, nil
}

// authorize maps skill class × caller role → allow / deny
//
//   - Safe — every authenticated caller (admin / user / viewer)
//   - Mutating — admin and user; viewer denied
//   - Dangerous — deny everyone until SOP double-sign lands (PR-G4)
func authorize(class skillcore.Class, role string) error {
	switch class {
	case skillcore.ClassSafe:
		return nil
	case skillcore.ClassMutating:
		switch role {
		case "admin", "user":
			return nil
		case "viewer":
			return fmt.Errorf("%w: viewer role cannot run mutating skills", errs.ErrForbidden)
		default:
			return fmt.Errorf("%w: mutating skills require admin or user role", errs.ErrForbidden)
		}
	case skillcore.ClassDangerous:
		// PR-G4 will gate this behind RSA-signed SOP; until then, deny.
		return fmt.Errorf("%w: dangerous skills require SOP signature (not implemented)", errs.ErrForbidden)
	}
	return errors.New("skill: unknown class")
}

func toSummary(e skillcore.Executor) SkillSummary {
	m := e.Metadata()
	params := make([]SkillParamDef, 0, len(m.Params))
	for _, p := range m.Params {
		params = append(params, SkillParamDef{
			Name:     p.Name,
			Type:     p.Type,
			Required: p.Required,
			Default:  p.Default,
			Desc:     p.Desc,
			Enum:     p.Enum,
		})
	}
	// inventory_only: this skill came in via inventory_bridge — its
	// schema lives in raw JSON Schema, ParamSchema is empty, so the
	// SPA's auto-form has nothing to render. Skill is still listed for
	// "what capabilities does the agent have" visibility, but UI hides
	// the execute button and points the user to chat.
	_, hasRawSchema := e.(skillcore.RawSchemaProvider)
	inventoryOnly := hasRawSchema && len(m.Params) == 0
	return SkillSummary{
		Key:           m.Key,
		Name:          m.Name,
		Description:   m.Description,
		Class:         m.EffectiveClass(),
		Scope:         m.EffectiveScope(),
		Category:      m.Category,
		Params:        params,
		ResultPreview: m.ResultPreview,
		InventoryOnly: inventoryOnly,
	}
}
