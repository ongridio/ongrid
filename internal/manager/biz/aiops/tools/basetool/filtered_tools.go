// filtered_tools.go implements ctx-propagation of the persona-whitelist-
// filtered tool view. ToolSearch reads from ctx so it only returns tools
// the current persona (coordinator or worker) is allowed to see, without
// shared mutable state on the ToolBag that would race across concurrent
// workers.
package basetool

import (
	"context"
)

type filteredToolsCtxKeyT struct{}

var filteredToolsCtxKey = filteredToolsCtxKeyT{}

// WithFilteredTools returns ctx augmented with the persona-filtered tool
// slice. The runtime calls this before graph.Invoke so ToolSearch (which
// runs inside the graph) can scope its search to only whitelist-surviving
// tools. nil/empty is a no-op.
func WithFilteredTools(ctx context.Context, tools []BaseTool) context.Context {
	if len(tools) == 0 {
		return ctx
	}
	return context.WithValue(ctx, filteredToolsCtxKey, tools)
}

// FilteredToolsFromContext retrieves the persona-filtered tool slice from
// ctx, if any. Returns nil when no filtering was applied (caller should
// fall back to the full tool universe).
func FilteredToolsFromContext(ctx context.Context) []BaseTool {
	v, _ := ctx.Value(filteredToolsCtxKey).([]BaseTool)
	return v
}
