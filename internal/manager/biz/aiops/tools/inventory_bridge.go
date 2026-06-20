// inventory_bridge.go is the inverse of skill_bridge.go.
//
// skill_bridge:    skill registry  → aiops Tool registry  (so the LLM
//
//	sees skills as function-calling tools)
//
// inventory_bridge: aiops BaseTool bag → skill registry  (so the /skills
//
//	page sees every cloud-side tool as
//	an inventoried capability, with audit
//	+ class gate)
//
// Background. Before this bridge, the manager-side LLM toolset had two
// parallel populations:
//
//  1. ScopeHost skills (host_probe_http, host_tail_file, ...) — declarative
//     metadata, auto-routed via tunnel, surfaced on /skills.
//  2. Hand-written BaseTools (correlate_incident, get_edge_summary,
//     batch fan-out tools, bash, host_restart_service, host_files batch,
//     AgentTool/SendMessage/TaskStop) — JSON-schema'd, manager-side
//     execution, NOT surfaced on /skills.
//
// Operators couldn't tell what cloud-side capabilities the AI agent had
// without reading source. The bridge fixes that: every BaseTool gets a
// matching skill registration with Scope=ScopeManager, and an opt-in
// RawSchemaProvider so its hand-written JSON Schema is preserved
// verbatim (no ParamSchema down-conversion required for shapes the
// declarative form can't express, like device_ids[] or nested objects).
//
// Bridge direction. We walk the BaseTool bag — that's the production
// surface fed to the LLM (cmd/main.go::BuildBaseTools). Tools whose
// name already exists as a skill (i.e. they were brought in via
// skill_bridge from the skill side) are skipped to avoid double-counting.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	aiopstoolsbase "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

// baseToolSkillExecutor wraps an aiops BaseTool as a skill.Executor.
// JSONSchema() implements skill.RawSchemaProvider so the bridge keeps
// the BaseTool's hand-crafted schema instead of generating one from
// (empty) ParamSchema.
//
// The original BaseTool is captured by reference; Execute calls its
// InvokableRun and decodes the JSON-string return into the
// json.RawMessage skill.Executor expects. Errors propagate.
type baseToolSkillExecutor struct {
	meta   skillcore.Metadata
	schema json.RawMessage
	tool   aiopstoolsbase.BaseTool
}

func (b *baseToolSkillExecutor) Metadata() skillcore.Metadata { return b.meta }

func (b *baseToolSkillExecutor) JSONSchema() json.RawMessage { return b.schema }

func (b *baseToolSkillExecutor) Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	args := string(params)
	if args == "" {
		args = "{}"
	}
	out, err := b.tool.InvokableRun(ctx, args)
	if err != nil {
		return nil, err
	}
	// BaseTool returns a JSON string; pass through as RawMessage. Validate
	// it's valid JSON so /skills page renderer doesn't choke.
	if !json.Valid([]byte(out)) {
		// Wrap non-JSON output in a string envelope so the framework
		// invariant (result is JSON) holds. Most BaseTools already
		// return JSON; this is a defensive belt for the rare exception.
		body, marshalErr := json.Marshal(map[string]string{"raw": out})
		if marshalErr != nil {
			return nil, marshalErr
		}
		return body, nil
	}
	return json.RawMessage(out), nil
}

// RegisterBaseToolsAsSkills walks every BaseTool in the bag and creates
// a matching skill.Executor in the global skill registry. Tools whose
// name already exists as a skill (because skill_bridge brought them
// in from the skill side) are skipped — registering them again would
// be a duplicate-key panic and conceptually they ARE the same
// capability viewed from two angles.
//
// Call this AFTER BuildBaseTools + AppendHostFilesTools so the bag
// holds the complete production tool set.
//
// Re-callable: idempotent at the skill side (skip when key already
// exists). Tools with bad metadata (empty Info / non-snake key) are
// skipped with a warning rather than crashing — robustness > strict
// invariants for an inventory feature.
func (r *Registry) RegisterBaseToolsAsSkills(bag *ToolBag, log *slog.Logger) {
	if bag == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	registered := 0
	skipped := 0
	for _, t := range bag.AllTools() {
		info, err := t.Info(context.Background())
		if err != nil || info == nil || info.Name == "" {
			skipped++
			continue
		}
		if _, exists := skillcore.Get(info.Name); exists {
			skipped++
			continue
		}
		if !isLowerSnake(info.Name) {
			log.Warn("inventory_bridge: skipping non-snake tool name", "name", info.Name)
			skipped++
			continue
		}
		desc := info.Description
		if info.WhenToUse != "" {
			desc = desc + "\n\nWhen to use:\n" + info.WhenToUse
		}
		meta := skillcore.Metadata{
			Key:           info.Name,
			Name:          info.Name,
			Description:   nonEmptyOrFallback(desc, info.Name),
			Class:         classifyToolClass(info),
			Scope:         skillcore.ScopeManager,
			Category:      classifyToolCategory(info.Name),
			ResultPreview: "", // BaseTools have heterogeneous result shapes
		}
		executor := &baseToolSkillExecutor{
			meta:   meta,
			schema: info.Parameters,
			tool:   t,
		}
		// Defensive: skip rather than panic on Validate failure (would
		// only happen if Name has unusual chars; we already pre-checked).
		if err := meta.Validate(); err != nil {
			log.Warn("inventory_bridge: skipping invalid skill meta",
				"name", info.Name, "err", err)
			skipped++
			continue
		}
		skillcore.Register(executor)
		registered++
	}
	log.Info("inventory_bridge: BaseTools registered as skills",
		slog.Int("registered", registered),
		slog.Int("skipped", skipped))
}

// classifyToolClass maps the BaseTool's Class hint (typically "read"
// or "write" — see basetool.ToolInfo) to the skill class taxonomy.
// Tools that mutate state (host_restart_service) become Mutating; AgentTool
// / SendMessage / TaskStop are read-shaped (no edge mutation) and stay
// Safe. Conservative default: Safe.
func classifyToolClass(info *aiopstoolsbase.ToolInfo) skillcore.Class {
	if info == nil {
		return skillcore.ClassSafe
	}
	if info.Class == "write" {
		return skillcore.ClassMutating
	}
	return skillcore.ClassSafe
}

// classifyToolCategory groups BaseTool names into category buckets
// matching the existing skill categories ("network" / "filesystem" /
// ...). Best-effort string sniffing; unknowns get "agent" so they
// group on /skills under one chip.
func classifyToolCategory(name string) string {
	switch name {
	case "host_bash":
		return "shell"
	case "get_host_load", "get_host_processes":
		return "system"
	case "host_find_large_files", "host_du_summary", "host_stat_file":
		return "filesystem"
	case "get_edge_summary", "get_topology", "query_devices",
		"query_alert_rules", "query_incidents", "get_incident_detail",
		"rank_edges", "find_outlier_edges", "correlate_incident":
		return "diagnostic"
	case "query_promql", "list_metric_catalog", "query_logql", "query_traceql":
		return "telemetry"
	case "agent", "send_message", "task_stop", "tool_search":
		return "agent"
	case "host_restart_service":
		return "process"
	}
	return "agent"
}

func nonEmptyOrFallback(s, fallback string) string {
	if s == "" {
		return fmt.Sprintf("(no description) %s", fallback)
	}
	return s
}

// isLowerSnake guards skillcore.Register's panic on bad keys. The
// validKey check there is private; this mirror keeps the bridge
// tolerant.
func isLowerSnake(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
