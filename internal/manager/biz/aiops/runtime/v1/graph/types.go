// Package graph is the eino-based agent execution kernel introduced by
// / (Agent compose.Graph 拼 ReAct) + (主参考图
// — Graph 执行层). It builds a `compose.Runnable[*Input, *Output]` whose
// internal shape is the standard ReAct loop:
//
//	START → MessageAssembler → ChatModel → Branch(tool_calls?) →
//	  yes → ToolsNode → MsgAppend → 回 ChatModel
//	  no → END
//
// Implementation note (architectural deviation ): the
// inner ReAct subgraph is built via cloudwego/eino's stock
// `flow/agent/react.NewAgent`, which already produces the exact graph
// the spec calls out (ChatModel ↔ Branch ↔ ToolsNode), bound at compile
// time with the user-supplied `model.ToolCallingChatModel` + tools. Our
// own wrapper graph (this package) adds the **MessageAssembler** lambda
// in front so callers pass `*Input` (system prompt + history + user
// text + system-reminder cfg) instead of pre-flattened `[]*schema.Message`,
// and an **OutputProjector** lambda in the back so callers receive
// `*Output` (assistant message + iteration count + usage) instead of
// the bare assistant `*schema.Message`. We are NOT re-implementing the
// branch / tools-node wiring by hand because eino's ReAct implementation
// is the canonical maintained one — re-rolling it would be churn against
// no benefit. See react.go header comment for the layered topology.
//
// PR-6 of scaffolding only. The cutover (replacing agent.go's
// 660-line for-loop with this graph) is the NEXT PR — main.go is not
// touched here. Callers in this PR are tests + internal wiring only.
package graph

import (
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Input is the structured request for one ReAct-loop run. It mirrors the
// information the legacy agent.go for-loop assembles from the chat
// session before dispatching to the LLM. The MessageAssembler lambda
// (react.go) flattens this into `[]*schema.Message` for the eino graph.
//
// ASCII shows MessageAssembler taking
// `Input{Messages, UserText, SystemPrompt}`; we add WebSearchEnabled and
// MentionsRendered as separate fields so the assembler can decide where
// to inline them (mentions go above the user text; web_search gating is
// enforced by the toolBag, not by message content).
type Input struct {
	// SystemPrompt is the base agent instructions. Empty = no system
	// message. PR-6: the caller (chatruntime, NEXT PR) stitches together
	// base prompt + active skill prompts + agent persona; this layer
	// just consumes the final string.
	SystemPrompt string

	// History is the prior conversation in eino message form. Includes
	// user / assistant / tool messages; ordered chronologically.
	// HistoryLimit gating happens upstream — this layer takes the slice
	// as-is.
	History []*schema.Message

	// UserText is the new user turn (verbatim, post-mention-inlining).
	// MentionsRendered, when non-empty, is prepended as a markdown
	// preamble per agent.go's existing behaviour.
	UserText string

	// WebSearchEnabled is the per-call gate the SPA's globe toggle
	// flips. PR-6 carries this through but enforcement remains at the
	// toolBag layer (chatruntime wires the right tool list); the field
	// is here so the assembler can echo it into a system-reminder block
	// for belt-and-braces (a hijacked tool_call from history replay
	// won't slip through).
	WebSearchEnabled bool

	// MentionsRendered is the pre-rendered markdown bullet list of
	// @-mention context blocks. Empty = no mentions on this turn.
	// agent.go's exact format ("用户在消息中引用了以下平台对象 ...") is
	// reproduced here for replay parity.
	MentionsRendered string

	// AgentReminder is the persona-level critical_reminder string the
	// coordinator/runtime resolved for this turn. When
	// non-empty it is appended verbatim — without further wrapping — as
	// one bullet inside the per-turn <system-reminder> block the
	// MessageAssembler injects. Empty = no persona reminder for this
	// turn (coordinator persona without a critical_reminder, or no
	// persona at all).
	//
	// Static persona prose still lives inside SystemPrompt via
	// chatruntime.ComposeSystemPrompt — this field carries only the
	// short reminder line that gets re-injected on every turn.
	AgentReminder string
	// DynamicHints is the runtime-computed list of per-turn hint lines
	// (each rendered as one bullet inside the <system-reminder> block).
	// — examples: "tool X failed N times in a row", "we
	// already ran Y iterations". Computed by chatruntime from the
	// session's recent message + tool history; the graph is not the
	// source of truth and never inspects history to derive its own
	// hints.
	DynamicHints []string

	// Locale is the UI language the answer should be written in
	// ("en-US" / "zh-CN"). The personas are Chinese, so without this the
	// model defaults to Chinese even when the SPA is in English mode. The
	// assembler turns it into an explicit "respond in <language>"
	// directive (system prompt + per-turn reminder). Empty = no directive
	// (back-compat; e.g. the IM bridge, which doesn't carry a UI locale).
	Locale string
}

// Output is the terminal result of a graph run. Mirrors the parts of
// agent.Reply this layer can populate without reaching into the
// session repo (which is the persistence callback's job). Iterations
// is best-effort — eino's react agent doesn't expose the step count
// directly, so the OutputProjector counts ChatModel turns on the
// callback channel and stamps the value here.
type Output struct {
	// AssistantMessage is the final assistant turn the model produced
	// (no tool_calls). Always non-nil on success.
	AssistantMessage *schema.Message

	// Iterations is the number of ChatModel calls that ran during this
	// turn. Counted via the metrics/audit handler chain; 0 if no
	// counter handler was wired.
	Iterations int

	// Usage is the aggregated token usage across every ChatModel call
	// in this run. Populated by the MetricsHandler (callbacks/metrics.go).
	// Zero values when no counter was wired.
	Usage llm.Usage
}

// Config tunes graph behaviour. Defaults match agent.go's existing
// for-loop so the cutover PR sees zero behavioural drift.
//
// 改进点 #5: MaxIterations 内嵌 — eino's ReAct accepts
// MaxStep at construction; we expose it here.
type Config struct {
	// Model is the LLM model id (e.g. "gpt-4o", "claude-sonnet-4-6").
	// Empty = use the underlying ChatModel's default.
	Model string

	// Provider is the routing key recognised by RoutingChatModel
	// (— provider 路由保留). Empty = use the routing
	// model's defaultProvider.
	Provider string

	// Temperature controls sampling randomness. 0 retains the legacy
	// agent default of 0.1.
	Temperature float32

	// MaxIterations caps the outer ReAct loop. 0 -> 30 (agent.go default).
	MaxIterations int

	// ToolTimeout is the per-tool wall clock ceiling. 0 -> 15s
	// (agent.go default). Enforced by the BaseTool decorator chain
	// (PR-3); this field is kept here so the cutover PR can pass the
	// value through to chatruntime when it builds the toolBag.
	ToolTimeout time.Duration
}

// applyDefaults fills in zero-valued Config fields with the same
// defaults agent.go uses. Returned by value so callers don't see
// mutation of their Config struct.
func (c Config) applyDefaults() Config {
	if c.MaxIterations <= 0 {
		c.MaxIterations = 30
	}
	if c.ToolTimeout <= 0 {
		c.ToolTimeout = 15 * time.Second
	}
	return c
}
