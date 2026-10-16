// Package decorators wraps basetool.BaseTool with cross-cutting concerns:
// timeout / audit / ratelimit / metric / tenant_bind. ASCII
// diagram and 主参考图 Tool 执行后端区块 spell out the standard chain;
// see chain.go's Wrap() for the production composition order.
//
// Each decorator returns a basetool.BaseTool so chains compose without
// concrete types leaking. Decorators are stateless apart from injected
// dependencies (audit sink, limiter, registerer) and safe to share across
// goroutines — the underlying BaseTool implementations carry the
// concurrency contract.
package decorators

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// ErrToolTimeout wraps the inner tool's error when the parent context's
// deadline (set by this decorator) fires. Callers can errors.Is() it to
// distinguish a decorator-imposed timeout from a tool-level error.
var ErrToolTimeout = errors.New("tool timed out")

// TimeoutTool wraps inner so InvokableRun runs under a context.WithTimeout.
// — TimeoutTool 装饰器层。
type TimeoutTool struct {
	inner   basetool.BaseTool
	timeout time.Duration
}

// WithTimeout returns inner wrapped so InvokableRun is bounded by d.
// Zero or negative d adds no deadline; the caller and tool retain ownership
// of cancellation and operation-specific budgets.
func WithTimeout(inner basetool.BaseTool, d time.Duration) basetool.BaseTool {
	if d <= 0 {
		return inner
	}
	return &TimeoutTool{inner: inner, timeout: d}
}

// Info passes through unchanged — schema metadata is not affected by
// the timeout wrapper.
func (t *TimeoutTool) Info(ctx context.Context) (*basetool.ToolInfo, error) {
	return t.inner.Info(ctx)
}

// InvokableRun runs the inner tool under context.WithTimeout(t.timeout).
// On deadline expiry the returned error wraps ErrToolTimeout so the
// agent loop can classify the call as status=timeout in chat_tool_calls.
func (t *TimeoutTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	out, err := t.inner.InvokableRun(callCtx, argsJSON, opts...)
	if err != nil {
		// If the parent ctx is alive but the call ctx died, the
		// timeout we imposed is the cause — surface that explicitly.
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("%w after %s: %v", ErrToolTimeout, t.timeout, err)
		}
		return "", err
	}
	// Even on successful inner return, check the deadline so a tool
	// that ignores ctx and races past doesn't slip through. This is
	// belt-and-braces — well-behaved tools respect ctx.
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return "", fmt.Errorf("%w after %s", ErrToolTimeout, t.timeout)
	}
	return out, nil
}
