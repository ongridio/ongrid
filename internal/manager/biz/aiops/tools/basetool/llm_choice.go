// Shared ctx propagation for the cluster's active LLM provider+model
// choice. Symmetrical to locale.go and lives in this leaf package for
// the same reason: chatruntime sets the values on the coordinator's
// ctx; tools/agent_tool reads them inside InvokableRun to populate the
// sub-agent's SpawnWorkerRequest; chatruntime cannot import tools
// (would close the dep loop).
//
// Why it matters: without this, runWorker's g.Invoke does not thread
// any chatModelOpts, so the RoutingChatModel falls back to its built-in
// default. Installs without an OpenAI API key see specialists fail with
// `provider "openai" not configured` — the cluster default (e.g.
// deepseek/zhipu) the user picked for the coordinator is the right
// answer; we just have to forward it.

package basetool

import "context"

type llmProviderCtxKeyT struct{}
type llmModelCtxKeyT struct{}

var (
	llmProviderCtxKey = llmProviderCtxKeyT{}
	llmModelCtxKey    = llmModelCtxKeyT{}
)

// WithLLMChoice stamps ctx with the (provider, model) pair the
// coordinator resolved for its own ChatModel call. Empty fields are
// no-ops, preserving the back-compat path for callers that don't
// have an explicit choice (the investigator auto-spawn path).
func WithLLMChoice(ctx context.Context, provider, model string) context.Context {
	if provider != "" {
		ctx = context.WithValue(ctx, llmProviderCtxKey, provider)
	}
	if model != "" {
		ctx = context.WithValue(ctx, llmModelCtxKey, model)
	}
	return ctx
}

// LLMProviderFromContext returns the coordinator's provider id, or "".
func LLMProviderFromContext(ctx context.Context) string {
	v, _ := ctx.Value(llmProviderCtxKey).(string)
	return v
}

// LLMModelFromContext returns the coordinator's model name, or "".
func LLMModelFromContext(ctx context.Context) string {
	v, _ := ctx.Value(llmModelCtxKey).(string)
	return v
}
