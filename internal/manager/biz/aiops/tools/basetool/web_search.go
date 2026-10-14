package basetool

import "context"

// ToolWebSearch 是联网搜索的稳定工具名。
const ToolWebSearch = "web_search"

type webSearchKey struct{}

// WithWebSearchAllowed 设置本轮联网权限，子任务通过 context 继承。
func WithWebSearchAllowed(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, webSearchKey{}, allowed)
}

// WebSearchAllowed 未明确授权时默认关闭。
func WebSearchAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(webSearchKey{}).(bool)
	return allowed
}

// FilterWebSearch 在模型暴露入口剔除本轮禁止的联网搜索。
func FilterWebSearch(ctx context.Context, tools []BaseTool) []BaseTool {
	if WebSearchAllowed(ctx) {
		return tools
	}
	out := make([]BaseTool, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err == nil && info != nil && info.Name == ToolWebSearch {
			continue
		}
		out = append(out, t)
	}
	return out
}
