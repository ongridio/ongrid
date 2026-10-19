// Package restart_service registers the **first mutating skill** in the
// ongrid skill registry — / SOP double-sign. The
// skill is ScopeHost + ClassMutating, which means:
//
//   - The manager-side AI tool wrapper requires an `edge_id` argument
//     and the actual restart runs on the edge.
//   - The framework's permission gate refuses to invoke this skill
//     directly from the LLM. Instead, the manager BaseTool counterpart
//     (internal/manager/biz/aiops/tools/restart_service.go) is
//     wrapped by the ReviewGate decorator (decorators/review_gate.go),
//     which spawns a reviewer worker (agents/reviewer.md) and only
//     dispatches if the reviewer returns "Decision: approve".
//
// This Executor is the **registration shim**: it teaches the
// internal/skill registry that "host_restart_service" exists with the right
// metadata (Class=mutating, Scope=edge) so the framework can render it
// in catalogs / docs / SPA listings. The actual edge dispatcher lives
// in internal/edgeagent/restart_service/handlers.go and is invoked
// through the tunnel — NOT through this Executor's Execute method.
//
// Why this Executor's Execute path is locked off:
//
//   - Edge-scope skills traditionally Execute via tunnel dispatch
//     (Method = "execute_skill", body = {key, params}); this skill
//     bypasses that pathway because mutating skills MUST go through
//     the manager-side BaseTool + ReviewGate. Calling Execute directly
//     would skip the review.
//   - Returning an explicit error here makes the protection visible:
//     anyone wiring a skill-direct-invoke path will see a runtime error
//     and route through the BaseTool instead.
//
// Subprocess on the edge: systemctl restart <unit>. PR-7 ships the
// MOCK implementation (see edgeagent/restart_service/handlers.go) — the
// SOP gating end-to-end is the focus of this PR; real systemctl shell-
// out is a follow-up.
package restart_service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ongridio/ongrid/internal/skill"
)

// Key is the skill's stable identifier. Manager-side BaseTool routes by
// the wire tool name (ToolNameRestartService = "host_restart_service"); the
// two are deliberately equal so audit logs and catalogs cross-link.
const Key = "host_restart_service"

func init() { skill.Register(&RestartService{}) }

// RestartService is the registry entry. It carries no state — the
// authoritative implementation lives on the edge.
type RestartService struct{}

// Metadata returns the framework-visible spec.
//
// Why Class=ClassMutating: this is the spec invariant the framework
// inspects to decide that direct invocation is forbidden and the
// reviewer flow is required. Setting it correctly is the single most
// important field on this skill.
//
// Why Scope=ScopeHost: the actual restart side-effect happens on the
// edge — manager-side BaseTool dispatches via tunnel.MethodRestartService.
// The framework's edge_id requirement is satisfied by the BaseTool's
// device_id arg (edge_devices junction lookup).
func (RestartService) Metadata() skill.Metadata {
	return skill.Metadata{
		Key:         Key,
		Name:        "重启 systemd 服务",
		Description: "重启允许列表内的 systemd service（mutating）— 触发 reviewer 二审，approve 后才执行",
		Class:       skill.ClassMutating,
		Scope:       skill.ScopeHost,
		Category:    "process",
		Params: skill.ParamSchema{
			{Name: "device_id", Param: skill.Param{
				Type: "int", Required: true,
				Desc: "目标设备 id（与 @-mention chip 和 Prom device_id 一致）",
			}},
			{Name: "service", Param: skill.Param{
				Type: "string", Required: true,
				Desc: "systemd 短名（不带 .service 后缀），例: nginx / redis / prometheus",
			}},
			{Name: "reason", Param: skill.Param{
				Type: "string",
				Desc: "重启理由，写入审计行，便于事后复盘",
			}},
		},
		ResultPreview: "{service, restarted, mocked, started_at, ended_at, error?}",
	}
}

// errMutatingDirect is returned when something tries to Execute() this
// skill directly. The reviewer flow lives in the manager BaseTool +
// ReviewGate decorator; bypassing it would break
var errMutatingDirect = errors.New(
	"host_restart_service: mutating skills must be invoked via the manager BaseTool " +
		"(ReviewGate decorator). Direct skill.Execute is not supported.",
)

// Execute is intentionally a no-op error. See package-doc rationale.
//
// In particular: the legacy closure-style agent kernel and the
// generic dispatcher both enter Execute. By erroring here we make sure
// the legacy kernel (which lacks the ReviewGate decorator) cannot
// accidentally fire a real restart. The new graph kernel calls the
// BaseTool wrapper (which is wrapped by ReviewGate) and never reaches
// this path.
func (RestartService) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errMutatingDirect
}
