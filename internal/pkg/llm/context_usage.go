package llm

import "github.com/cloudwego/eino/components/model"

// ContextUsage describes the most recent prepared request, not daily usage.
type ContextUsage struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Window       int    `json:"window"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Source       string `json:"source"`
	Trimmed      bool   `json:"trimmed"`
}

type contextUsageOptions struct{ observe func(ContextUsage) }

// WithContextUsageObserver scopes reporting to this graph invocation; workers
// do not inherit the coordinator's model options.
func WithContextUsageObserver(observe func(ContextUsage)) model.Option {
	return model.WrapImplSpecificOptFn(func(o *contextUsageOptions) { o.observe = observe })
}
