package callbacks

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// SSEEventType enumerates the streaming events the SSE handler emits.
// — wire shapes mirror the legacy agent.Emit contract used
// in internal/manager/server/aiops/http.go so the SPA round-trips
// without changes after cutover.
type SSEEventType string

const (
	// SSEEventAssistantStart fires before the ChatModel produces any
	// content (— assistant_start frame).
	SSEEventAssistantStart SSEEventType = "assistant_start"
	// SSEEventAssistantDelta is the new token-level streaming frame
	// (— "新增能力"). The delta payload only carries the
	// incremental Content chunk so the SPA can append to the bubble.
	SSEEventAssistantDelta SSEEventType = "assistant_delta"
	// SSEEventAssistantEnd fires after the ChatModel returns. Payload
	// matches the legacy `assistant` frame: full content + iteration +
	// pending tool count.
	SSEEventAssistantEnd SSEEventType = "assistant_end"
	// SSEEventToolStart fires when a tool starts. Mirrors the legacy
	// `tool_start` frame.
	SSEEventToolStart SSEEventType = "tool_start"
	// SSEEventToolEnd fires when a tool finishes. Mirrors the legacy
	// `tool_end` frame; status carries success / error / timeout.
	SSEEventToolEnd SSEEventType = "tool_end"
	// SSEEventDone fires once at terminal success.
	SSEEventDone SSEEventType = "done"
	// SSEEventError fires on any unrecoverable graph error.
	SSEEventError SSEEventType = "error"
	// SSEEventTaskNotification fires when a background sub-agent worker
	// reaches a terminal state The coordinator
	// runtime emits this through the same SSE channel as the assistant
	// frames so the SPA can render a worker-result tile inline. Payload
	// shape lives on TaskNotificationPayload below.
	SSEEventTaskNotification SSEEventType = "task_notification"
)

// SSEEvent is the payload-typed envelope passed to the SSEEmitter.
// The handler stays format-agnostic (it doesn't know about HTTP /
// `event:` lines / JSON encoding); the cutover layer's emitter is
// responsible for encoding into wire frames so the existing http.go
// writeSSE routine stays intact.
type SSEEvent struct {
	Type         SSEEventType
	Iteration    int
	Assistant    *AssistantPayload        // for assistant_start / assistant_end
	Delta        *AssistantDelta          // for assistant_delta
	Tool         *ToolPayload             // for tool_start / tool_end
	Done         *DonePayload             // for done
	Error        *ErrorPayload            // for error
	Notification *TaskNotificationPayload // for task_notification
}

// AssistantPayload is the start/end frame body. ContentSoFar is empty
// at start; on end it carries the model's full reply content.
//
// MessageID is the persisted chat_messages row id assigned by the
// PersistenceHandler when it wrote the assistant row. It's filled at
// assistant_end emit time by looking it up via the chain's
// AssistantIDReader (chained ordering: persistence runs before SSE).
// Empty when persistence is disabled or hasn't completed.
type AssistantPayload struct {
	Iteration        int
	MessageID        string
	Content          string
	PendingToolCalls int
	CreatedAt        time.Time
}

// AssistantDelta is the per-chunk streaming payload. The handler
// emits one for each non-empty Content chunk delivered by eino's
// stream output.
type AssistantDelta struct {
	Iteration int
	Content   string
}

// ToolPayload is the tool_start / tool_end frame body.
type ToolPayload struct {
	ToolCallID string
	Name       string
	ArgsJSON   string
	Status     string // pending | success | error | timeout
	StartedAt  time.Time
	EndedAt    *time.Time
	DurationMs int64
	ResultJSON string
	Error      string
}

// DonePayload is the terminal success frame.
type DonePayload struct {
	Iterations int
}

// ErrorPayload is the terminal failure frame.
type ErrorPayload struct {
	Message string
	Code    string
}

// TaskNotificationPayload is the body of a task_notification frame
// Emitted by the coordinator runtime when a background
// worker spawned via AgentTool reaches a terminal state. The shape is
// stable wire because the SPA reads each field directly:
//
//	event: task_notification
//	data: {
//	  "task_id": "agent-12ab34cd", // worker identifier
//	  "status": "completed"|"failed"|"killed",
//	  "summary": "Agent incident-investigator completed",
//	  "result": "...final assistant content...", // present on completed
//	  "error": "context canceled", // present on failed/killed
//	  "usage": { // optional, may be empty
//	    "duration_ms": 12345
//	  }
//	}
type TaskNotificationPayload struct {
	TaskID  string         `json:"task_id"`
	Status  string         `json:"status"`
	Summary string         `json:"summary"`
	Result  string         `json:"result,omitempty"`
	Err     string         `json:"error,omitempty"`
	Usage   map[string]any `json:"usage,omitempty"`
}

// SSEEmitter is the seam between the handler and the HTTP writer.
// The cutover layer builds an emitter that wraps the http.Flusher +
// writeSSE helper from internal/manager/server/aiops/http.go so the
// wire format stays byte-compatible. emit MUST NOT block — slow
// consumers must be handled (drop or buffer) inside the implementation
// Streaming non-blocking 约束.
type SSEEmitter func(SSEEvent)

// SSEHandler translates graph callbacks into SSEEvents.
//
// spec:
//
//	OnChatModelStart → assistant_start
//	OnChatModelStream(chunk)→ assistant_delta
//	OnChatModelEnd → assistant_end
//	OnToolStart → tool_start
//	OnToolEnd → tool_end
//	OnGraphComplete → done
//	OnError → error
//
// The graph "complete" event is emitted from OnEnd when info.Component
// is the wrapper graph itself (components.ComponentOfGraph) — eino
// fires the same OnEnd contract on graph-scope as on node-scope.
//
// Concurrency: the emitter is called sequentially within the lifetime
// of a single graph run; tool fan-out (compose ToolsNode parallel
// execution) emits per-tool start/end events that may interleave —
// the iteration counter is atomic so the assistant_start sequence is
// consistent. Cutover-layer emitter implementations should be
// goroutine-safe (the existing http.go writeSSE serializes through
// the response writer, which is already single-goroutine).
type SSEHandler struct {
	emit SSEEmitter

	// iterations counts ChatModel turns for the assistant frame's
	// `iteration` field. Atomic because tool fan-out callbacks may
	// interleave.
	iterations atomic.Int64

	// toolStarts records per-tool start time to compute duration in OnEnd.
	toolStartsMu sync.Mutex
	toolStarts   map[string]toolStart

	// assistantIDRelay (optional) is the cross-handler share with
	// PersistenceHandler — see chain.go. NewDefaultHandlers sets it;
	// nil when SSE is used standalone (tests).
	assistantIDRelay *assistantIDRelay
}

type toolStart struct {
	at       time.Time
	argsJSON string
	name     string
}

// NewSSEHandler builds the handler. Returns nil if emit is nil so the
// cutover layer can opt into SSE on a per-request basis.
func NewSSEHandler(emit SSEEmitter) *SSEHandler {
	if emit == nil {
		return nil
	}
	return &SSEHandler{
		emit:       emit,
		toolStarts: make(map[string]toolStart),
	}
}

// Needed gates which timings the handler observes.
func (h *SSEHandler) Needed(_ context.Context, info *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	if h == nil || info == nil {
		return false
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		switch timing {
		case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError, callbacks.TimingOnEndWithStreamOutput:
			return true
		}
	case components.ComponentOfTool:
		switch timing {
		case callbacks.TimingOnStart, callbacks.TimingOnEnd, callbacks.TimingOnError:
			return true
		}
	default:
		// Graph / Workflow / Chain — observe terminal so we can emit
		// `done` / `error` frames.
		switch timing {
		case callbacks.TimingOnEnd, callbacks.TimingOnError:
			return true
		}
	}
	return false
}

// OnStart fires before a component runs.
func (h *SSEHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		it := h.iterations.Add(1)
		h.emit(SSEEvent{
			Type:      SSEEventAssistantStart,
			Iteration: int(it),
			Assistant: &AssistantPayload{Iteration: int(it)},
		})
	case components.ComponentOfTool:
		tin := einotool.ConvCallbackInput(input)
		args := ""
		if tin != nil {
			args = tin.ArgumentsInJSON
		}
		startedAt := time.Now().UTC()
		key := toolCallIDFromCtx(ctx, info)
		h.toolStartsMu.Lock()
		h.toolStarts[key] = toolStart{at: startedAt, argsJSON: args, name: info.Name}
		h.toolStartsMu.Unlock()
		h.emit(SSEEvent{
			Type: SSEEventToolStart,
			Tool: &ToolPayload{
				ToolCallID: key,
				Name:       info.Name,
				ArgsJSON:   args,
				Status:     "pending",
				StartedAt:  startedAt,
			},
		})
	}
	return ctx
}

// OnEnd fires after a component succeeds.
func (h *SSEHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	if h == nil || info == nil {
		return ctx
	}
	switch info.Component {
	case components.ComponentOfChatModel:
		mo := einomodel.ConvCallbackOutput(output)
		if mo == nil {
			return ctx
		}
		var content string
		var pending int
		if mo.Message != nil {
			content = mo.Message.Content
			pending = len(mo.Message.ToolCalls)
		}
		it := int(h.iterations.Load())
		// h.assistantIDRelay carries the row id PersistenceHandler
		// just stored — chain.go registers Persistence before SSE so
		// by the time we emit here, the AppendMessage for this iter
		// is done. Empty string when persistence is disabled
		// (e.g. tests that wire only SSE).
		h.emit(SSEEvent{
			Type:      SSEEventAssistantEnd,
			Iteration: it,
			Assistant: &AssistantPayload{
				Iteration:        it,
				MessageID:        h.assistantIDRelay.load(),
				Content:          content,
				PendingToolCalls: pending,
				CreatedAt:        time.Now().UTC(),
			},
		})
	case components.ComponentOfTool:
		tout := einotool.ConvCallbackOutput(output)
		key := toolCallIDFromCtx(ctx, info)
		h.toolStartsMu.Lock()
		ts, ok := h.toolStarts[key]
		delete(h.toolStarts, key)
		h.toolStartsMu.Unlock()
		endedAt := time.Now().UTC()
		var dur int64
		if ok {
			dur = endedAt.Sub(ts.at).Milliseconds()
		}
		body := ""
		if tout != nil {
			body = tout.Response
		}
		h.emit(SSEEvent{
			Type: SSEEventToolEnd,
			Tool: &ToolPayload{
				ToolCallID: key,
				Name:       info.Name,
				ArgsJSON:   ts.argsJSON,
				Status:     "success",
				StartedAt:  ts.at,
				EndedAt:    &endedAt,
				DurationMs: dur,
				ResultJSON: body,
			},
		})
	default:
		// Graph-scope OnEnd — emit `done`.
		h.emit(SSEEvent{
			Type: SSEEventDone,
			Done: &DonePayload{Iterations: int(h.iterations.Load())},
		})
	}
	return ctx
}

// OnError fires when a component returns a non-nil error.
func (h *SSEHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	if h == nil || info == nil || err == nil {
		return ctx
	}
	if info.Component == components.ComponentOfTool {
		key := toolCallIDFromCtx(ctx, info)
		h.toolStartsMu.Lock()
		ts, ok := h.toolStarts[key]
		delete(h.toolStarts, key)
		h.toolStartsMu.Unlock()
		endedAt := time.Now().UTC()
		var dur int64
		if ok {
			dur = endedAt.Sub(ts.at).Milliseconds()
		}
		status := "error"
		if isDeadlineErr(err) {
			status = "timeout"
		}
		h.emit(SSEEvent{
			Type: SSEEventToolEnd,
			Tool: &ToolPayload{
				ToolCallID: key,
				Name:       info.Name,
				ArgsJSON:   ts.argsJSON,
				Status:     status,
				StartedAt:  ts.at,
				EndedAt:    &endedAt,
				DurationMs: dur,
				Error:      err.Error(),
			},
		})
		return ctx
	}
	// Top-level / ChatModel error — emit terminal `error` frame.
	h.emit(SSEEvent{
		Type:  SSEEventError,
		Error: &ErrorPayload{Message: err.Error()},
	})
	return ctx
}

// OnStartWithStreamInput is a no-op (we ignore streaming input).
func (h *SSEHandler) OnStartWithStreamInput(ctx context.Context, _ *callbacks.RunInfo, in *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	if in != nil {
		in.Close()
	}
	return ctx
}

// OnEndWithStreamOutput drains the streaming output of a ChatModel and
// fans assistant_delta frames to the SSE emitter as chunks land.
// — assistant_delta 是新增的 token-level 帧。
//
// We close the receiver-side copy when done so eino can reclaim the
// stream goroutine; the original output stream continues to flow to
// downstream graph nodes.
func (h *SSEHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, out *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	if h == nil || info == nil || out == nil {
		if out != nil {
			out.Close()
		}
		return ctx
	}
	if info.Component != components.ComponentOfChatModel {
		out.Close()
		return ctx
	}
	go h.drainStream(out)
	return ctx
}

func (h *SSEHandler) drainStream(out *schema.StreamReader[callbacks.CallbackOutput]) {
	defer out.Close()
	for {
		chunk, err := out.Recv()
		if err != nil {
			return
		}
		mo := einomodel.ConvCallbackOutput(chunk)
		if mo == nil || mo.Message == nil {
			continue
		}
		if mo.Message.Content == "" {
			continue
		}
		it := int(h.iterations.Load())
		h.emit(SSEEvent{
			Type:      SSEEventAssistantDelta,
			Iteration: it,
			Delta:     &AssistantDelta{Iteration: it, Content: mo.Message.Content},
		})
	}
}

// IterationCount returns the number of ChatModel turns the handler has
// observed in this run. Exposed for tests.
func (h *SSEHandler) IterationCount() int {
	if h == nil {
		return 0
	}
	return int(h.iterations.Load())
}

// Compile-time check.
var (
	_ callbacks.Handler       = (*SSEHandler)(nil)
	_ callbacks.TimingChecker = (*SSEHandler)(nil)
)
