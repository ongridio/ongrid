// toolbag.go implements the deferred-tool-loading wrapper described in
// The motivation is the LLM's prompt budget: as the
// marketplace lands and tool count climbs past ~30 the cumulative JSON
// Schema each ChatModel call ships balloons (each tool's parameters
// block can run hundreds of tokens). Anthropic-style "ToolSearch"
// deferred loading flips this around — only a small core of always-
// useful tools advertises a full schema; the rest advertise only their
// (name, description, when_to_use) and the LLM has to call ToolSearch
// to pull a real schema before invoking.
//
// Two-tier classification (hardcoded by name in tierByName below — v1
// keeps the policy out of ToolInfo so the basetool interface stays
// pure):
//
//   - core = always exposes a full schema (host/cluster reads,
//     query_*, database inventory/status helpers, coordinator-only
//     AgentTool / SendMessage / TaskStop, conversational config tools).
//   - specialty = redacted-by-default when toolBag size > threshold;
//     schema fetched on demand via ToolSearch (host_files
//     trio, host_restart_service, alert detail, ranking helpers).
//
// Threshold gating: when len(all) > threshold (default 30, env
// ONGRID_TOOLBAG_DEFERRAL_THRESHOLD) the bag splits core / specialty
// and SchemasForLLM returns the redacted-wrapped slice. Below
// threshold ALL tools keep their full schema (PR-7 behavioural parity).
//
// The wrapper is allocation-light: redactedTool stores only a pointer
// to the inner BaseTool and synthesises the redacted ToolInfo on each
// Info() call. Invocation passes through unchanged so an LLM that
// somehow already knows the parameter shape (e.g. from system prompt
// context) can still call the tool — the inner Tool's argsJSON
// validation handles any malformed input.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// tierByName classifies each registered tool
// "core" stays loaded; "specialty" is redacted when the bag overflows
// the threshold. Unknown names default to specialty (safer than
// shipping a phantom full-schema for a tool we haven't reasoned
// through).
//
// ToolSearch itself is intentionally NOT listed here — when deferral
// kicks in BuildBaseTools attaches it via WithExtra so it's force-loaded
// with a full schema (the LLM needs ToolSearch's schema to actually
// call ToolSearch). Below threshold the bag never partitions, so
// ToolSearch sits in the same flat slice as everything else.
var tierByName = map[string]string{
	// core (always full schema).
	"get_host_load":           "core",
	"get_host_processes":      "core",
	"query_promql":            "core",
	"list_metric_catalog":     "core",
	"list_database_sources":   "core",
	"analyze_database_status": "core",
	"query_logql":             "core",
	"query_traceql":           "core",
	"query_devices":           "core",
	"get_topology":            "core",
	"query_incidents":         "core",
	"get_edge_summary":        "core",
	"correlate_incident":      "core",
	"AgentTool":               "core",
	"SendMessage":             "core",
	"TaskStop":                "core",
	"draft_config_change":     "core",
	"apply_config_change":     "core",

	// specialty (deferred when over threshold) — host-files trio,
	// alert detail tools, ranking helpers, mutating restart, generic
	// bash. Bash is specialty because it's only-on-demand for
	// diagnostic exploration — most chats don't need it, and its
	// when_to_use blob is large enough to want defer-loading once
	// the marketplace pushes the bag past threshold.
	"rank_edges":            "specialty",
	"find_outlier_edges":    "specialty",
	"get_incident_detail":   "specialty",
	"query_alert_rules":     "specialty",
	"host_find_large_files": "specialty",
	"host_du_summary":       "specialty",
	"host_stat_file":        "specialty",
	"host_restart_service":  "specialty",
	"host_bash":             "specialty",
}

// toolTier classifies a BaseTool. Errors from Info() are treated as
// "specialty" — a tool that can't even produce its own metadata
// shouldn't get the privileged core slot.
func toolTier(t basetool.BaseTool) string {
	if t == nil {
		return "specialty"
	}
	info, err := t.Info(context.Background())
	if err != nil || info == nil {
		return "specialty"
	}
	if tier, ok := tierByName[info.Name]; ok {
		return tier
	}
	return "specialty"
}

// ToolBag is the partitioned tool collection. core + deferred together
// equal the universe of tools the agent has access to. When deferred is
// empty, deferral is OFF (toolBag size ≤ threshold) and SchemasForLLM
// returns the full schema for every tool; when deferred is non-empty,
// deferral is ON and SchemasForLLM swaps each deferred tool through the
// redactedTool wrapper.
//
// extras is the WithExtra slot — typically just ToolSearch itself — and
// is appended to core unconditionally so the LLM always sees its full
// schema when deferral is active. extras is an empty slice when
// deferral is off (the bag is already flat) so callers can chain
// WithExtra without triggering accidental dual-listing.
type ToolBag struct {
	core      []basetool.BaseTool
	deferred  []basetool.BaseTool
	extras    []basetool.BaseTool
	threshold int
	deferring bool
}

// NewToolBag inspects all and partitions by tier when len(all) >
// threshold. Below or at threshold every tool stays in core and
// deferred is nil — SchemasForLLM behaves byte-identical to "return
// all" (PR-7 behaviour).
//
// threshold ≤ 0 is treated as "always defer": useful for tests; in
// production main.go passes envIntDefault so 0 isn't reachable through
// normal env config.
func NewToolBag(all []basetool.BaseTool, threshold int) *ToolBag {
	bag := &ToolBag{threshold: threshold}
	// Below-or-equal threshold: keep flat. We treat "exactly equal"
	// as still flat so the threshold reads as a high-water mark
	// ("up to N tools is fine"), not as a strict ceiling.
	if len(all) <= threshold {
		bag.core = append(bag.core, all...)
		return bag
	}
	bag.deferring = true
	for _, t := range all {
		if t == nil {
			continue
		}
		switch toolTier(t) {
		case "core":
			bag.core = append(bag.core, t)
		default:
			bag.deferred = append(bag.deferred, t)
		}
	}
	return bag
}

// SchemasForLLM returns the BaseTool slice the graph node feeds into
// the ChatModel. Three behaviours:
//
//   - deferral off: the input slice (full schemas, PR-7 parity).
//   - deferral on: core (full) + extras (full, e.g. ToolSearch) +
//     deferred (each wrapped in redactedTool which
//     advertises an empty schema).
func (b *ToolBag) SchemasForLLM() []basetool.BaseTool {
	if b == nil {
		return nil
	}
	if !b.deferring {
		// Flat: extras may still hold ToolSearch (registered
		// unconditionally by BuildBaseTools — harmless when
		// deferral is off because ToolSearch returning full
		// schemas is still a valid no-op affordance).
		out := make([]basetool.BaseTool, 0, len(b.core)+len(b.extras))
		out = append(out, b.core...)
		out = append(out, b.extras...)
		return out
	}
	out := make([]basetool.BaseTool, 0, len(b.core)+len(b.extras)+len(b.deferred))
	out = append(out, b.core...)
	out = append(out, b.extras...)
	for _, t := range b.deferred {
		out = append(out, redactedTool{inner: t})
	}
	return out
}

// AllTools returns every tool in the bag (core + extras + deferred),
// each in its UNREDACTED form. Used by ToolSearch so it can hand back a
// full schema for any tool the LLM asks about.
func (b *ToolBag) AllTools() []basetool.BaseTool {
	if b == nil {
		return nil
	}
	out := make([]basetool.BaseTool, 0, len(b.core)+len(b.extras)+len(b.deferred))
	out = append(out, b.core...)
	out = append(out, b.extras...)
	out = append(out, b.deferred...)
	return out
}

// DeferredTools returns only the redacted-by-default tier — the slice
// that SchemasForLLM wraps in redactedTool. Used by ToolSearch to bias
// "select:..."-less keyword queries toward tools that actually need
// their schema fetched (the others are already fully exposed).
func (b *ToolBag) DeferredTools() []basetool.BaseTool {
	if b == nil {
		return nil
	}
	out := make([]basetool.BaseTool, 0, len(b.deferred))
	out = append(out, b.deferred...)
	return out
}

// IsDeferring reports whether the bag is over threshold. Useful for
// boot-time logging — operators want to see in the manager log whether
// the LLM is going through ToolSearch or seeing flat schemas.
func (b *ToolBag) IsDeferring() bool {
	if b == nil {
		return false
	}
	return b.deferring
}

// Threshold returns the configured deferral threshold. Logging-only.
func (b *ToolBag) Threshold() int {
	if b == nil {
		return 0
	}
	return b.threshold
}

// WithExtra appends a tool to the always-loaded slot. Used by
// BuildBaseTools to register ToolSearch after construction (chicken-
// and-egg: ToolSearch needs the bag as its DeferredToolBagProvider, the
// bag has to exist before ToolSearch can be built). Returns the same
// bag so callers can chain.
func (b *ToolBag) WithExtra(t basetool.BaseTool) *ToolBag {
	if b == nil || t == nil {
		return b
	}
	b.extras = append(b.extras, t)
	return b
}

// Append injects an extra tool into the partitioned bag AFTER
// construction — used by AppendHostFilesTools so the host_files trio is
// bucketed correctly (specialty) without rebuilding the bag.
//
// We do NOT re-evaluate the deferral toggle here: a bag built with 8
// tools below threshold doesn't suddenly start deferring after we
// append 3 more to a 9-tool bag. The deferral decision is made once at
// NewToolBag — main.go chains AppendHostFilesTools right after
// BuildBaseTools so the natural way to flip on deferral is to bump
// ONGRID_TOOLBAG_DEFERRAL_THRESHOLD or wait for marketplace tools to
// land in NewToolBag's input slice directly.
func (b *ToolBag) Append(t basetool.BaseTool) *ToolBag {
	if b == nil || t == nil {
		return b
	}
	if !b.deferring {
		b.core = append(b.core, t)
		return b
	}
	if toolTier(t) == "core" {
		b.core = append(b.core, t)
	} else {
		b.deferred = append(b.deferred, t)
	}
	return b
}

// ----- redactedTool wrapper -----

// redactedTool wraps a BaseTool so its Info() returns an empty JSON
// schema. The Description + WhenToUse + Name + Class still ship — the
// LLM sees enough to decide whether the tool is worth pulling, but not
// enough to call it without going through ToolSearch first. The
// wrapper is intentionally not exported: nothing outside this package
// should ever construct one directly (use NewToolBag).
type redactedTool struct {
	inner basetool.BaseTool
}

// Info returns the inner tool's metadata with Parameters replaced by
// an empty-schema stub that points the LLM at ToolSearch. The stub's
// description doubles as a hint string the LLM is guaranteed to see at
// the schema position so even a model that hasn't read the system
// prompt's deferral notice gets the message.
func (r redactedTool) Info(ctx context.Context) (*basetool.ToolInfo, error) {
	info, err := r.inner.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, fmt.Errorf("redactedTool: inner Info returned nil")
	}
	out := *info
	hint := fmt.Sprintf(
		`{"type":"object","properties":{},"description":"Schema redacted; call ToolSearch with query='select:%s' to load the full schema before invoking."}`,
		info.Name,
	)
	out.Parameters = json.RawMessage(hint)
	return &out, nil
}

// InvokableRun delegates to the inner tool. This is the fallback when
// the LLM tries to call the tool without first fetching the schema —
// the inner tool's argsJSON unmarshal will fail with a clear error
// message that the agent loop classifies as a tool failure. We do NOT
// short-circuit with our own error: defending against malformed args
// is the inner tool's job, and a future schema-cache layer might want
// to allow the call through if the LLM happens to know the parameters.
func (r redactedTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if r.inner == nil {
		return "", fmt.Errorf("redactedTool: inner not wired")
	}
	return r.inner.InvokableRun(ctx, argsJSON, opts...)
}
