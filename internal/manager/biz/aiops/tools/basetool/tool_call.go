package basetool

import (
	"context"
)

// toolCallIDCtxKeyT stores a persisted tool-call row ID so approval cards can
// attach to the existing streaming tool card without depending on eino compose.
type toolCallIDCtxKeyT struct{}

var toolCallIDCtxKey = toolCallIDCtxKeyT{}

// WithToolCallID tags ctx with the current persisted tool-call row id.
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDCtxKey, id)
}

// ToolCallIDFromContext returns the persisted tool-call row id, if any.
func ToolCallIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(toolCallIDCtxKey).(string)
	return v
}
