package decorators

import (
	"context"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// AuditSink is the interface-only seam that the audit decorator writes
// through. The production binding (later PR) implements this against
// the chat_tool_calls repo; tests inject a fake. The seam is here
// (instead of importing biz/aiops.SessionRepo) so decorators stay free
// of biz repo coupling — 不变量 (模块边界).
//
// Callback 链 spec for OnToolStart / OnToolEnd — this
// decorator is the synchronous, in-process equivalent for the parallel
// BaseTool path. When the agent loop migrates to eino + ToolsNode the
// implementation may switch to eino callbacks; the AuditSink contract
// stays the same.
type AuditSink interface {
	// OnToolStart records the start of a tool invocation. id is an
	// opaque correlation token returned to the caller and passed back
	// to OnToolEnd; implementations typically map it to the
	// chat_tool_calls.id (UUID). When the sink wants to short-circuit
	// the call (e.g. quota exceeded) it returns a non-nil error and
	// the decorator skips InvokableRun entirely, surfacing the error.
	OnToolStart(ctx context.Context, ev ToolStartEvent) (id string, err error)

	// OnToolEnd records the end of a tool invocation. id is the value
	// returned from OnToolStart. Errors here are logged but do NOT
	// override the tool's own outcome — audit failures must not cause
	// tool failures (可观测性: audit best-effort).
	OnToolEnd(ctx context.Context, id string, ev ToolEndEvent) error
}

// ToolStartEvent is what the audit sink sees at the start of a call.
// — captures the inputs needed to write the pending
// chat_tool_calls row.
type ToolStartEvent struct {
	ToolName  string
	ArgsJSON  string
	Tenant    string
	UserID    uint64
	DeviceID  *uint64
	StartedAt time.Time
}

// ToolEndEvent is what the audit sink sees at the end of a call.
// — captures the result/error needed to update the
// chat_tool_calls row to status=success/error/timeout.
type ToolEndEvent struct {
	ResultJSON string
	Err        error // nil on success
	EndedAt    time.Time
	Duration   time.Duration
}

// AuditTool wraps inner so OnToolStart fires before InvokableRun and
// OnToolEnd fires after (regardless of inner error). —
// AuditTool 装饰器层。
type AuditTool struct {
	inner basetool.BaseTool
	sink  AuditSink
}

// WithAudit returns inner wrapped to emit start + end events through
// sink. A nil sink is a no-op pass-through (so production wiring can
// disable audit in tests/dev without conditional decorators at the
// call site).
func WithAudit(inner basetool.BaseTool, sink AuditSink) basetool.BaseTool {
	if sink == nil {
		return inner
	}
	return &AuditTool{inner: inner, sink: sink}
}

// Info passes through — auditing is invocation-only, the schema is
// public.
func (a *AuditTool) Info(ctx context.Context) (*basetool.ToolInfo, error) {
	return a.inner.Info(ctx)
}

// InvokableRun emits OnToolStart, runs the inner tool, then emits
// OnToolEnd. OnToolStart errors abort the call (returned as-is so
// quota / preflight failures surface unmangled). OnToolEnd errors are
// silently swallowed to honour the "audit must not fail the tool"
// invariant — they reach observability via the sink's own logging.
func (a *AuditTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	resolved := basetool.ResolveOptions(opts)

	// Resolve the tool name once; Info must not perform I/O.
	name := ""
	if info, err := a.inner.Info(ctx); err == nil && info != nil {
		name = info.Name
	}

	startedAt := time.Now().UTC()
	id, err := a.sink.OnToolStart(ctx, ToolStartEvent{
		ToolName:  name,
		ArgsJSON:  argsJSON,
		Tenant:    resolved.Tenant,
		UserID:    resolved.UserID,
		DeviceID:  resolved.DeviceID,
		StartedAt: startedAt,
	})
	if err != nil {
		return "", err
	}

	out, runErr := a.inner.InvokableRun(ctx, argsJSON, opts...)
	endedAt := time.Now().UTC()
	_ = a.sink.OnToolEnd(ctx, id, ToolEndEvent{
		ResultJSON: out,
		Err:        runErr,
		EndedAt:    endedAt,
		Duration:   endedAt.Sub(startedAt),
	})
	return out, runErr
}
