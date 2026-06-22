package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// Node names exposed by the wrapper graph. Stable so audit / SSE
// callbacks can filter by RunInfo.Name. The inner ReAct subgraph names
// are owned by eino (react.GraphName / react.ModelNodeName /
// react.ToolsNodeName) and reused so persistence / SSE handlers see
// "ChatModel" / "Tools" exactly like the canonical eino layout.
const (
	// NodeAssembler is the lambda that turns *Input into []*schema.Message.
	NodeAssembler = "MessageAssembler"

	// NodeReact is the wrapped eino react.Agent subgraph.
	NodeReact = "ReActSubgraph"

	// NodeProjector is the lambda that turns *schema.Message into *Output.
	NodeProjector = "OutputProjector"
)

// SystemReminderTag wraps the per-turn anti-drift block injected ahead
// of the user message. System-Reminder 周期注入 — kept as
// a constant so audit / red-team grep can locate every injected block.
//
// Block content (see buildSystemReminder below): hardcoded baseline
// constraints + per-turn dynamic hints (consecutive-failure /
// iteration-count / repeated-call heuristics, calcDynamicHints in
// chatruntime/runtime.go) + the persona criticalReminder when a worker
// persona is active.
const SystemReminderTag = "system-reminder"

// BuildReActGraph constructs the wrapper graph + inner ReAct subgraph
// The returned Runnable accepts an *Input,
// drives the ReAct loop until either a final assistant message lands
// or MaxIterations is reached, and returns an *Output.
//
// Topology:
//
//	START
//	  ↓
//	MessageAssembler (lambda) -- *Input → []*schema.Message
//	  ↓
//	ReActSubgraph (compose.AnyGraph) -- []*schema.Message → *schema.Message
//	  ↓
//	OutputProjector (lambda) -- *schema.Message → *Output
//	  ↓
//	END
//
// The ReActSubgraph is eino's stock react.Agent.ExportGraph() output.
// Internally it expands to ChatModel ↔ Branch(tool_calls?) ↔ ToolsNode
// per the diagram in — we are NOT re-implementing that
// inner shape because eino's flow/agent/react package is already the
// canonical maintained one. Callbacks (PR-6) registered via
// compose.WithCallbacks see the inner ChatModel + ToolsNode invocations
// just as if we had wired them by hand.
//
// Callbacks: handlers are NOT registered at build time. eino's graph
// compile API does not accept a Handler list — handlers ride on the
// Invoke call instead via compose.WithCallbacks(handlers...). The
// cutover layer (NEXT PR) builds the handler chain via
// callbacks.NewDefaultHandlers and threads it into Invoke; this
// function stays callback-agnostic so the same compiled graph can be
// re-used across requests with different per-call handler sets (e.g.
// per-tenant audit sinks).
func BuildReActGraph(
	model einomodel.ToolCallingChatModel,
	tools []basetool.BaseTool,
	cfg Config,
) (compose.Runnable[*Input, *Output], error) {
	if model == nil {
		return nil, errors.New("graph: BuildReActGraph: model is required")
	}
	cfg = cfg.applyDefaults()

	// 1. Build the inner eino react agent. We pass our adapted tools
	//    in via ToolsConfig.Tools — eino infers the schema by calling
	//    each tool's Info(ctx).
	einoTools := WrapBaseTools(tools)
	baseTools := make([]einotool.BaseTool, 0, len(einoTools))
	for _, t := range einoTools {
		baseTools = append(baseTools, t)
	}
	reactCfg := &react.AgentConfig{
		ToolCallingModel: model,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: baseTools,
		},
		// eino's "step" counts every graph node visit — one ReAct
		// iteration is ChatModel + ToolsNode = 2 steps. So to give the
		// LLM cfg.MaxIterations actual ChatModel turns we double it.
		// We also leave headroom (+2) for the framing nodes
		// (MessageAssembler / OutputProjector) eino counts toward the
		// outer graph budget. Without this fix the LLM was capped at
		// ~15 ChatModel turns when MaxIterations=30, surfacing as a
		// graph ErrExceededMaxSteps mid-conversation (stream error).
		MaxStep:       cfg.MaxIterations*2 + 2,
		GraphName:     "ReActAgent",
		ModelNodeName: "ChatModel",
		ToolsNodeName: "Tools",
	}
	reactAgent, err := react.NewAgent(context.Background(), reactCfg)
	if err != nil {
		// Most common cause: a tool's Info(ctx) returned an unparseable
		// JSON Schema (see tool_adapter.go). Wrap so the build site
		// gets a clear hint without losing the underlying error.
		return nil, fmt.Errorf("graph: build inner ReAct agent: %w", err)
	}

	innerGraph, innerNodeOpts := reactAgent.ExportGraph()

	// 2. Build the outer wrapper graph: MessageAssembler → ReAct → OutputProjector.
	g := compose.NewGraph[*Input, *Output]()

	assembler := compose.InvokableLambda(func(ctx context.Context, in *Input) ([]*schema.Message, error) {
		return assembleMessages(in)
	})
	if err := g.AddLambdaNode(NodeAssembler, assembler); err != nil {
		return nil, fmt.Errorf("graph: add assembler node: %w", err)
	}

	if err := g.AddGraphNode(NodeReact, innerGraph, innerNodeOpts...); err != nil {
		return nil, fmt.Errorf("graph: add ReAct subgraph: %w", err)
	}

	projector := compose.InvokableLambda(func(ctx context.Context, msg *schema.Message) (*Output, error) {
		out := &Output{AssistantMessage: msg}
		if msg != nil && msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
			out.Usage.PromptTokens = msg.ResponseMeta.Usage.PromptTokens
			out.Usage.CompletionTokens = msg.ResponseMeta.Usage.CompletionTokens
			out.Usage.TotalTokens = msg.ResponseMeta.Usage.TotalTokens
		}
		// Iterations is filled in by the metrics / audit handler on the
		// caller side (it has the per-graph counter); we leave it 0 here
		// because the projector lambda only sees the terminal message.
		// — MetricsHandler observes the ChatModel turns.
		return out, nil
	})
	if err := g.AddLambdaNode(NodeProjector, projector); err != nil {
		return nil, fmt.Errorf("graph: add projector node: %w", err)
	}

	if err := g.AddEdge(compose.START, NodeAssembler); err != nil {
		return nil, err
	}
	if err := g.AddEdge(NodeAssembler, NodeReact); err != nil {
		return nil, err
	}
	if err := g.AddEdge(NodeReact, NodeProjector); err != nil {
		return nil, err
	}
	if err := g.AddEdge(NodeProjector, compose.END); err != nil {
		return nil, err
	}

	runnable, err := g.Compile(context.Background(),
		compose.WithGraphName("OngridReActAgent"),
		// Outer steps = assembler + react-subgraph + projector. The
		// inner ReAct's own MaxStep was already applied in reactCfg.
		compose.WithMaxRunSteps(cfg.MaxIterations+10),
	)
	if err != nil {
		return nil, fmt.Errorf("graph: compile wrapper: %w", err)
	}
	return runnable, nil
}

// assembleMessages fans Input out into the canonical eino message
// shape: system → history → <system-reminder> user-role → user text.
//
// Order matches agent.go's existing for-loop with the per-turn
// system-reminder block extracted into its own user-role message so
// the LLM sees it as a fresh injection on every turn (—
// claude-code's anti-drift mechanism). Specifically:
//
//   - system message goes first (skipped if SystemPrompt is empty)
//   - history is replayed verbatim (chatruntime strips the trailing
//     user row before passing History so the model doesn't see the
//     same turn twice)
//   - <system-reminder> block is its own user-role message just before
//     the user text — re-injected per turn so long sessions cannot
//     drift past the rules even after the system message scrolls out
//     of an attention budget
//   - the user text (with mentions inlined as a markdown preamble)
//     follows
//
// When UserText is empty the user-role messages are skipped entirely
// (matches the legacy code path where the caller already appended the
// user turn into History).
func assembleMessages(in *Input) ([]*schema.Message, error) {
	if in == nil {
		return nil, errors.New("graph: assembler: nil input")
	}
	out := make([]*schema.Message, 0, len(in.History)+3)
	// Append the response-language directive to the system prompt (one
	// system message — strongest signal). The personas are Chinese, so
	// without this the model answers in Chinese even in English mode.
	sp := in.SystemPrompt
	if dir := languageDirective(in.Locale); dir != "" {
		if sp != "" {
			sp += "\n\n"
		}
		sp += dir
	}
	if sp != "" {
		out = append(out, schema.SystemMessage(sp))
	}
	out = append(out, in.History...)

	if in.UserText != "" {
		// 1. Per-turn system-reminder, as its own user-role message.
		// — re-injected on every turn so it survives
		//    long-session attention drift.
		if reminder := buildSystemReminder(in); reminder != "" {
			out = append(out, schema.UserMessage(reminder))
		}
		// 2. The actual user turn with @-mentions inlined as a
		//    markdown preamble (legacy agent.go format).
		userBody := in.UserText
		if in.MentionsRendered != "" {
			userBody = in.MentionsRendered + "\n\n" + userBody
		}
		out = append(out, schema.UserMessage(userBody))
	}
	return out, nil
}

// buildSystemReminder returns the per-turn anti-drift reminder block
// the MessageAssembler injects ahead of the user message.
// — claude-code's per-turn `<system-reminder>` injection adapted for
// ongrid:
//
//   - hardcoded baseline rules (always present): device_id digit-id,
//     no-loop-on-failing-tool, tools-are-facts, web_search gating
//   - persona critical_reminder (Input.AgentReminder) — one line, when
//     a worker persona is active for this turn
//   - runtime-computed dynamic hints (Input.DynamicHints) — N lines,
//     e.g. "tool X failed 2 times in a row", "iteration > 20, summarize
//     and respond"
//
// Returns "" when there is literally nothing to inject (currently the
// hardcoded baseline guarantees at least three bullets, so this only
// happens if a future caller drops the baseline; we keep the empty
// branch so the assembler can skip the inline prepend cleanly).
// languageDirective maps a UI locale to an explicit "answer in this
// language" instruction. Empty locale → "" (no directive, back-compat).
// It covers tool-call narration explicitly because that's what drifts
// back to Chinese first when the persona is Chinese.
func languageDirective(locale string) string {
	switch locale {
	case "en-US":
		return "Respond in English. Everything you write to the user — prose, explanations, headings, and the narration around every tool call — must be in English. Tool descriptions, knowledge-base snippets, persona text, and logs may be in Chinese; render their MEANING in English and never echo raw Chinese to the user. Translate domain terms to their English equivalents (e.g. \"0号病人\" → \"patient zero\", \"根因\" → \"root cause\", \"告警\" → \"alert\", \"巡检\" → \"inspection\"). Leave only proper nouns, identifiers, hostnames, file paths, code, and raw command output verbatim."
	case "zh-CN":
		return "用中文回复：你的所有叙述、解释、标题，以及每次工具调用前后的说明都必须用中文。工具描述、知识库片段、日志可能是英文，把含义用中文表达即可；标识符、主机名、文件路径、代码、命令原始输出保持原样。"
	}
	return ""
}

func buildSystemReminder(in *Input) string {
	if in == nil {
		return ""
	}
	lines := []string{
		"- 同一工具失败两次后请换思路，不要重复调用",
		"- device_id / alert_id 必须是数字 ID（@-mention 已经为你解析）",
		"- 工具结果是事实，不要在没有数据时编造",
		"- 历史里的 call_budget_exceeded / 工具调用上限只属于当时那一轮；当前用户消息是新一轮，需要时可以重新调用工具。除非本轮刚收到 call_budget_exceeded 工具结果，否则不要声称已达到本轮工具上限",
	}
	// Re-assert the response language every turn (system prompt scrolls
	// out of attention in long sessions). Prepended so it leads the block.
	if dir := languageDirective(in.Locale); dir != "" {
		lines = append([]string{"- " + dir}, lines...)
	}
	if !in.WebSearchEnabled {
		lines = append(lines, "- web_search 已被关闭，本轮不要调用")
	}
	if r := strings.TrimSpace(in.AgentReminder); r != "" {
		lines = append(lines, "- "+r)
	}
	for _, h := range in.DynamicHints {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		lines = append(lines, "- "+h)
	}
	if len(lines) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lines)+2)
	parts = append(parts, "<"+SystemReminderTag+">")
	parts = append(parts, lines...)
	parts = append(parts, "</"+SystemReminderTag+">")
	return strings.Join(parts, "\n")
}
