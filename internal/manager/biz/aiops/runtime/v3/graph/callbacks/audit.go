package callbacks

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// AuditDeps configures the audit handler. Logger is required (a nil
// logger turns the handler into a no-op so the cutover layer can opt
// in/out per environment). SessionID is the chat session this run
// belongs to — included in every log line so operators can grep all
// stages of one conversation. UserID is logged as a numeric scope.
type AuditDeps struct {
	Logger    *slog.Logger
	SessionID string
	UserID    uint64
}

// AuditHandler emits structured slog INFO records for every stage of
// the graph: per-ChatModel turn (prompt token estimate + reply tokens),
// per-tool call (name + duration + status). spec — and
// / 红线: the user's raw prompt content is NEVER
// included in the log line; only counts and identifiers.
//
// One handler instance per graph run. Concurrency: tool fan-out may
// call OnStart / OnEnd concurrently; per-call timestamps live in a
// mutex-guarded map.
type AuditHandler struct {
	deps AuditDeps

	chatTurn atomic.Int64

	startsMu sync.Mutex
	starts   map[string]auditStart
}

type auditStart struct {
	at         time.Time
	component  components.Component
	name       string
	estTokens  int
	toolCallID string
}

// NewAuditHandler builds the handler. Returns nil when Logger is nil
// so the cutover layer can short-circuit.
func NewAuditHandler(deps AuditDeps) *AuditHandler {
	if deps.Logger == nil {
		return nil
	}
	return &AuditHandler{deps: deps, starts: make(map[string]auditStart)}
}

// Needed gates timings.
func (h *AuditHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil || info == nil {
		return false
	}
	switch info.Component {
	case components.ComponentOfChatModel, components.ComponentOfTool:
		switch timing {
		case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError:
			return true
		}
	}
	return false
}

// OnStart records the start time so OnEnd can compute duration.
func (h *AuditHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	at := time.Now()
	entry := auditStart{at: at, component: info.Component, name: info.Name}
	switch info.Component {
	case components.ComponentOfChatModel:
		mi := einomodel.ConvCallbackInput(input)
		if mi != nil {
			entry.estTokens = estimatePromptTokens(mi.Messages)
		}
		turn := h.chatTurn.Add(1)
		h.deps.Logger.Info("graph stage start",
			slog.String("session_id", h.deps.SessionID),
			slog.Uint64("user_id", h.deps.UserID),
			slog.String("kind", "chat_model"),
			slog.String("name", info.Name),
			slog.Int64("iteration", turn),
			slog.Int("est_prompt_tokens", entry.estTokens),
		)
	case components.ComponentOfTool:
		entry.toolCallID = toolCallIDFromCtx(ctx, info)
		args := ""
		if tin := einotool.ConvCallbackInput(input); tin != nil {
			args = tin.ArgumentsInJSON
		}
		// Log args size, not args content — the args may carry user-
		// authored text (PromQL / LogQL queries) that
		// forbids us from logging in cleartext.
		h.deps.Logger.Info("graph stage start",
			slog.String("session_id", h.deps.SessionID),
			slog.Uint64("user_id", h.deps.UserID),
			slog.String("kind", "tool"),
			slog.String("name", info.Name),
			slog.String("tool_call_id", entry.toolCallID),
			slog.Int("args_bytes", len(args)),
		)
	}
	h.startsMu.Lock()
	h.starts[stageKey(ctx, info)] = entry
	h.startsMu.Unlock()
	return ctx
}

// OnEnd logs the stage's duration and result.
func (h *AuditHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	h.startsMu.Lock()
	entry, ok := h.starts[stageKey(ctx, info)]
	delete(h.starts, stageKey(ctx, info))
	h.startsMu.Unlock()
	if !ok {
		return ctx
	}
	dur := time.Since(entry.at)
	switch info.Component {
	case components.ComponentOfChatModel:
		mo := einomodel.ConvCallbackOutput(output)
		usage := slog.Group("token_usage")
		toolCalls := 0
		if mo != nil {
			if mo.TokenUsage != nil {
				usage = slog.Group("token_usage",
					slog.Int("prompt", mo.TokenUsage.PromptTokens),
					slog.Int("completion", mo.TokenUsage.CompletionTokens),
					slog.Int("total", mo.TokenUsage.TotalTokens),
				)
			} else if mo.Message != nil && mo.Message.ResponseMeta != nil && mo.Message.ResponseMeta.Usage != nil {
				u := mo.Message.ResponseMeta.Usage
				usage = slog.Group("token_usage",
					slog.Int("prompt", u.PromptTokens),
					slog.Int("completion", u.CompletionTokens),
					slog.Int("total", u.TotalTokens),
				)
			}
			if mo.Message != nil {
				toolCalls = len(mo.Message.ToolCalls)
			}
		}
		h.deps.Logger.Info("graph stage end",
			slog.String("session_id", h.deps.SessionID),
			slog.Uint64("user_id", h.deps.UserID),
			slog.String("kind", "chat_model"),
			slog.String("name", info.Name),
			slog.Int64("duration_ms", dur.Milliseconds()),
			slog.Int("tool_calls_emitted", toolCalls),
			slog.String("status", "success"),
			usage,
		)
	case components.ComponentOfTool:
		body := ""
		if tout := einotool.ConvCallbackOutput(output); tout != nil {
			body = tout.Response
		}
		h.deps.Logger.Info("graph stage end",
			slog.String("session_id", h.deps.SessionID),
			slog.Uint64("user_id", h.deps.UserID),
			slog.String("kind", "tool"),
			slog.String("name", info.Name),
			slog.String("tool_call_id", entry.toolCallID),
			slog.Int64("duration_ms", dur.Milliseconds()),
			slog.Int("result_bytes", len(body)),
			slog.String("status", "success"),
		)
	}
	return ctx
}

// OnError logs the stage's failure.
func (h *AuditHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	if h == nil || info == nil || err == nil {
		return ctx
	}
	h.startsMu.Lock()
	entry, ok := h.starts[stageKey(ctx, info)]
	delete(h.starts, stageKey(ctx, info))
	h.startsMu.Unlock()
	dur := time.Duration(0)
	if ok {
		dur = time.Since(entry.at)
	}
	status := "error"
	if isDeadlineErr(err) {
		status = "timeout"
	}
	h.deps.Logger.Warn("graph stage end",
		slog.String("session_id", h.deps.SessionID),
		slog.Uint64("user_id", h.deps.UserID),
		slog.String("kind", componentKind(info.Component)),
		slog.String("name", info.Name),
		slog.Int64("duration_ms", dur.Milliseconds()),
		slog.String("status", status),
		slog.String("error", err.Error()),
	)
	return ctx
}

// OnStartWithStreamInput drains and closes the stream copy.
func (h *AuditHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, in *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if in != nil {
		in.Close()
	}
	return ctx
}

// OnEndWithStreamOutput drains and closes the stream copy.
func (h *AuditHandler) OnEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if out != nil {
		out.Close()
	}
	return ctx
}

// estimatePromptTokens is the same rule-of-thumb used by the budget
// callback (chars/4). Local copy so we don't pull the llm package in
// just for the function (avoids an import cycle with biz/aiops/graph).
func estimatePromptTokens(msgs []*schema.Message) int {
	const perMsgOverhead = 4
	total := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += perMsgOverhead
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Name) / 4
			total += len(tc.Function.Arguments) / 4
		}
	}
	return total
}

func componentKind(c components.Component) string {
	switch c {
	case components.ComponentOfChatModel:
		return "chat_model"
	case components.ComponentOfTool:
		return "tool"
	default:
		return string(c)
	}
}

// stageKey is the per-stage correlation key used to look up the
// matching OnStart entry from OnEnd / OnError. For tool calls we use
// the tool_call id; for chat models we use the run-info name + the
// turn index so concurrent (eino doesn't fan out chat models, but
// future SOP graphs may) calls don't collide.
func stageKey(ctx context.Context, info *callbacks.RunInfo) string {
	if info == nil {
		return ""
	}
	if info.Component == components.ComponentOfTool {
		return "tool|" + toolCallIDFromCtx(ctx, info)
	}
	if v, ok := ctx.Value(messageIDCtxKey{}).(string); ok && v != "" {
		return string(info.Component) + "|" + info.Name + "|" + v
	}
	return string(info.Component) + "|" + info.Name
}

// Compile-time checks.
var (
	_ callbacks.Handler       = (*AuditHandler)(nil)
	_ callbacks.TimingChecker = (*AuditHandler)(nil)
)
