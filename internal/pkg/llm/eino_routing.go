// eino_routing.go is the eino-typed LLM layer /
// (Agent eino skill framework). It lives next to the existing
// OpenAI-shape Client (client.go) and MUST NOT be wired into the live agent
// loop in this PR — wiring is a later PR.
//
// What this file provides:
//   - RoutingChatModel: an eino model.ChatModel that dispatches to a
//     pre-built inner ChatModel keyed by provider id. Selection happens via
//     the impl-specific WithProvider option; absent it, defaultProvider
//     handles the call. Reference diagram in shows this as
//     the "ChatModel" layer at the bottom of the agent graph.
//   - WithProvider: impl-specific eino option to pick an inner provider per
//     call.
//   - NewClientChatModel: thin adapter wrapping an existing llm.Client into
//     an eino model.ToolCallingChatModel. Lets PR-1 ship without depending
//     on github.com/cloudwego/eino-ext/components/model/openai (its dep
//     surface is heavy and PR-1 is scaffolding only). Provider endpoints
//     are OpenAI-compatible, so a single
//     adapter suffices.
//
// Streaming note: this scaffolding adapter buffers the response from the
// underlying llm.Client and returns it as a single-chunk StreamReader so
// the eino interface is satisfied. Real token-by-token streaming arrives
// in a later PR alongside graph wiring.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Provider ids accepted by RoutingChatModel. Keep in lockstep with the
// admin settings page (all named providers are OpenAI-compatible).
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderZhipu     = "zhipu"
	ProviderGemini    = "gemini"
	ProviderDeepSeek  = "deepseek"
	ProviderKimi      = "kimi"
	ProviderMiniMax   = "minimax"
	ProviderXiaomi    = "xiaomi"
	// ProviderCustom is a generic OpenAI-compatible endpoint configured
	// entirely from settings (base_url + key + models). Routing is
	// id-agnostic, so it dispatches like any other provider.
	ProviderCustom = "custom"
)

// ErrUnknownProvider is returned when WithProvider names a provider that
// was not registered in RoutingChatModel.
var ErrUnknownProvider = errors.New("llm: unknown provider")

// providerOpts is the impl-specific options bag carried by WithProvider.
// Kept private so callers reach it only via the WithProvider helper.
type providerOpts struct {
	provider string
}

// WithProvider returns an eino model.Option that selects which inner
// provider RoutingChatModel should dispatch to for this call. If the
// option is omitted, RoutingChatModel falls back to its defaultProvider.
//
// Designed to be passed alongside standard model.Option values:
//
//	rcm.Generate(ctx, msgs,
//	    model.WithTemperature(0.1),
//	    llm.WithProvider(llm.ProviderAnthropic),
func WithProvider(provider string) model.Option {
	return model.WrapImplSpecificOptFn(func(o *providerOpts) {
		o.provider = provider
	})
}

// RoutingChatModel implements eino's deprecated ChatModel surface (which
// embeds BaseChatModel + BindTools) so that legacy callers can still call
// BindTools to fan out a tool list to every inner client. New code paths
// should prefer per-call model.WithTools, which is forwarded as-is.
//
// The struct is concurrency-safe for Generate / Stream once constructed:
// inner ChatModels are never reassigned. BindTools is the one mutating
// path and inherits eino's documented non-atomic caveat (see
// model.ChatModel doc).
//
// Reference: +
type RoutingChatModel struct {
	inner           map[string]model.ChatModel
	defaultProvider string
	// defaultResolver, when non-nil, supplies the LIVE configured default
	// provider and its model for calls that omit WithProvider — so a runtime
	// change to the configured default (the home-page model picker writing
	// default_provider / <provider>_default_model) takes effect without a
	// restart. This is what makes background consumers that don't pin a model
	// — the RCA investigator worker, query_translate — track the home
	// selection. Returning an empty provider, or one with no inner ChatModel,
	// falls back to defaultProvider.
	defaultResolver func(context.Context) (provider, mdl string)
}

// RoutingChatModelConfig configures a RoutingChatModel.
//
// Inner is the provider id -> ChatModel map; an entry MUST exist for at
// least DefaultProvider. DefaultProvider is the fallback used when a call
// omits WithProvider; it must be a key in Inner.
type RoutingChatModelConfig struct {
	Inner           map[string]model.ChatModel
	DefaultProvider string
	// DefaultResolver is optional; see RoutingChatModel.defaultResolver. When
	// set, it overrides DefaultProvider per-call for calls that omit
	// WithProvider, letting the configured default change live.
	DefaultResolver func(context.Context) (provider, mdl string)
}

// NewRoutingChatModel builds a RoutingChatModel. Returns an error if
// DefaultProvider is missing from Inner or if Inner is empty.
func NewRoutingChatModel(cfg RoutingChatModelConfig) (*RoutingChatModel, error) {
	if len(cfg.Inner) == 0 {
		return nil, errors.New("llm: RoutingChatModel needs at least one inner ChatModel")
	}
	if cfg.DefaultProvider == "" {
		return nil, errors.New("llm: RoutingChatModel needs a default provider")
	}
	if _, ok := cfg.Inner[cfg.DefaultProvider]; !ok {
		return nil, fmt.Errorf("llm: default provider %q not in Inner map", cfg.DefaultProvider)
	}
	// Defensive copy so later mutations on caller's map don't leak in.
	cp := make(map[string]model.ChatModel, len(cfg.Inner))
	for k, v := range cfg.Inner {
		if v == nil {
			return nil, fmt.Errorf("llm: inner ChatModel for provider %q is nil", k)
		}
		cp[k] = v
	}
	return &RoutingChatModel{
		inner:           cp,
		defaultProvider: cfg.DefaultProvider,
		defaultResolver: cfg.DefaultResolver,
	}, nil
}

// withDynamicDefault injects the live configured default provider (and its
// model) for calls that omit WithProvider, so a runtime change to the
// configured default — the home-page model picker writing default_provider —
// takes effect without a restart. Calls that pin a provider (the chat picker)
// keep it; a pinned model is never overridden. Falls back to defaultProvider
// when the resolver is absent, returns empty, or names a provider with no
// inner ChatModel (e.g. one added since boot).
func (r *RoutingChatModel) withDynamicDefault(ctx context.Context, opts []model.Option) []model.Option {
	if r.defaultResolver == nil {
		return opts
	}
	if model.GetImplSpecificOptions(&providerOpts{}, opts...).provider != "" {
		return opts
	}
	prov, mdl := r.defaultResolver(ctx)
	if prov == "" {
		return opts
	}
	if _, ok := r.inner[prov]; !ok {
		return opts
	}
	extra := []model.Option{WithProvider(prov)}
	if mdl != "" && model.GetCommonOptions(&model.Options{}, opts...).Model == nil {
		extra = append(extra, model.WithModel(mdl))
	}
	return append(opts, extra...)
}

// pick resolves the inner ChatModel for this call.
func (r *RoutingChatModel) pick(opts ...model.Option) (model.ChatModel, string, error) {
	po := model.GetImplSpecificOptions(&providerOpts{}, opts...)
	prov := po.provider
	if prov == "" {
		prov = r.defaultProvider
	}
	inner, ok := r.inner[prov]
	if !ok {
		return nil, prov, fmt.Errorf("%w: %q", ErrUnknownProvider, prov)
	}
	return inner, prov, nil
}

// Generate dispatches a non-streaming chat to the selected inner model.
// Standard model.Option values (WithTemperature, WithTools, etc.) are
// passed through verbatim; the WithProvider option is consumed here.
func (r *RoutingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	opts = r.withDynamicDefault(ctx, opts)
	inner, _, err := r.pick(opts...)
	if err != nil {
		return nil, err
	}
	return inner.Generate(ctx, input, opts...)
}

// Stream dispatches a streaming chat to the selected inner model. The
// caller must Close the returned StreamReader.
func (r *RoutingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	opts = r.withDynamicDefault(ctx, opts)
	inner, _, err := r.pick(opts...)
	if err != nil {
		return nil, err
	}
	return inner.Stream(ctx, input, opts...)
}

// BindTools fans the tool list out to every inner ChatModel.
// Mutates the inner instances in place and inherits eino's
// non-atomic caveat (see model.ChatModel doc). New call sites should
// prefer per-call model.WithTools.
func (r *RoutingChatModel) BindTools(tools []*schema.ToolInfo) error {
	for prov, inner := range r.inner {
		if err := inner.BindTools(tools); err != nil {
			return fmt.Errorf("llm: BindTools on provider %q: %w", prov, err)
		}
	}
	return nil
}

// WithTools satisfies model.ToolCallingChatModel by returning a new
// RoutingChatModel whose inner ChatModels have all been bound to the
// given tool list. Each inner that supports the WithTools idiom is
// preferred (immutable derivation); inners that only expose the
// legacy BindTools surface get bound in-place on a copy. The receiver
// is not mutated.
//
// — eino's ReAct agent dispatches via WithTools at
// build time, so RoutingChatModel must implement it for the graph
// kernel (PR-9) to wire cleanly.
func (r *RoutingChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cp := &RoutingChatModel{
		inner:           make(map[string]model.ChatModel, len(r.inner)),
		defaultProvider: r.defaultProvider,
		defaultResolver: r.defaultResolver,
	}
	for prov, inner := range r.inner {
		if tcm, ok := inner.(model.ToolCallingChatModel); ok {
			derived, err := tcm.WithTools(tools)
			if err != nil {
				return nil, fmt.Errorf("llm: WithTools on provider %q: %w", prov, err)
			}
			// derived is BaseChatModel-shaped (no BindTools). Wrap
			// it so it satisfies the wider model.ChatModel surface
			// the inner map stores.
			cp.inner[prov] = &derivedChatModel{tcm: derived}
			continue
		}
		// Legacy ChatModel — fall back to BindTools on the same
		// instance. Caller has accepted the non-atomic caveat by
		// using such an inner.
		if err := inner.BindTools(tools); err != nil {
			return nil, fmt.Errorf("llm: BindTools on provider %q: %w", prov, err)
		}
		cp.inner[prov] = inner
	}
	return cp, nil
}

// derivedChatModel adapts a ToolCallingChatModel into the wider
// ChatModel surface (the inner map's value type) by stubbing
// BindTools so the WithTools-derived instance plugs cleanly into
// the existing routing layout. The stub is a no-op because tools
// were already bound via the WithTools call that produced tcm.
type derivedChatModel struct {
	tcm model.ToolCallingChatModel
}

func (d *derivedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return d.tcm.Generate(ctx, input, opts...)
}

func (d *derivedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return d.tcm.Stream(ctx, input, opts...)
}

// BindTools is a no-op stub: the wrapped ToolCallingChatModel was
// already derived via WithTools and is immutable.
func (d *derivedChatModel) BindTools(_ []*schema.ToolInfo) error { return nil }

// Providers returns the set of registered provider ids. Useful for
// admin UIs and tests.
func (r *RoutingChatModel) Providers() []string {
	out := make([]string, 0, len(r.inner))
	for k := range r.inner {
		out = append(out, k)
	}
	return out
}

// DefaultProvider returns the provider used when WithProvider is omitted.
func (r *RoutingChatModel) DefaultProvider() string {
	return r.defaultProvider
}

// Compile-time check.
var (
	_ model.ChatModel            = (*RoutingChatModel)(nil)
	_ model.ToolCallingChatModel = (*RoutingChatModel)(nil)
)

// ---------------------------------------------------------------------
// clientChatModel — adapter wrapping our existing llm.Client into the
// eino model.ChatModel surface. Used as the inner ChatModel for each of
// the 4 OpenAI-compatible providers in PR-1's scaffolding.
//
// We avoid pulling github.com/cloudwego/eino-ext/components/model/openai
// in PR-1: that pulls another OpenAI SDK fork into go.mod, and the
// existing llm.Client (sashabaranov/go-openai) already covers all four
// providers via per-instance BaseURL / APIKey (calls all
// four "OpenAI-compatible"). PR-2 / PR-3 may swap this for the eino-ext
// implementation if needed; the boundary is the eino interface, not this
// adapter.
// ---------------------------------------------------------------------

// clientChatModel adapts a llm.Client into eino's ChatModel.
type clientChatModel struct {
	inner Client
	// model is the default model name to send when the call site does not
	// override via model.WithModel. May be empty to defer to whatever the
	// underlying llm.Client resolves at call time.
	model string
	// userID is forwarded into ChatReq.UserID so the legacy budget gate
	// path (client.go:259) keeps working when the call comes through this
	// adapter without going via BudgetCallbackHandler.
	userID uint64
	// boundTools is the list set by BindTools, applied to every Generate /
	// Stream call unless overridden by per-call model.WithTools.
	boundTools []*schema.ToolInfo
}

// ClientChatModelConfig configures a NewClientChatModel.
type ClientChatModelConfig struct {
	Client Client
	Model  string
	UserID uint64
}

// NewClientChatModel builds an eino ChatModel that delegates to the given
// llm.Client. Returns an error if Client is nil. The returned value
// satisfies model.ChatModel (BindTools-style) AND model.ToolCallingChatModel
// (WithTools-style); call sites can pick whichever fits.
func NewClientChatModel(cfg ClientChatModelConfig) (model.ChatModel, error) {
	if cfg.Client == nil {
		return nil, errors.New("llm: NewClientChatModel: Client is required")
	}
	return &clientChatModel{
		inner:  cfg.Client,
		model:  cfg.Model,
		userID: cfg.UserID,
	}, nil
}

// Generate translates eino input → ChatReq, calls the underlying Client,
// translates ChatResp → *schema.Message.
func (c *clientChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	common := model.GetCommonOptions(&model.Options{}, opts...)
	req, err := c.buildChatReq(input, common)
	if err != nil {
		return nil, err
	}
	resp, err := c.inner.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return einoMessageFromChatResp(resp), nil
}

// Stream wraps Generate behind an array-backed StreamReader. PR-1 is
// scaffolding only — real token-by-token streaming lands in a later PR.
func (c *clientChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := c.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// BindTools stores the tool list on the adapter; it is forwarded into
// every subsequent Generate / Stream unless overridden by model.WithTools.
func (c *clientChatModel) BindTools(tools []*schema.ToolInfo) error {
	c.boundTools = tools
	return nil
}

// WithTools satisfies model.ToolCallingChatModel: returns a new instance
// with the given tools bound, leaving the receiver immutable. Safe for
// concurrent use.
func (c *clientChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cp := *c
	cp.boundTools = tools
	return &cp, nil
}

func (c *clientChatModel) buildChatReq(input []*schema.Message, common *model.Options) (ChatReq, error) {
	msgs := make([]Message, 0, len(input))
	for i, m := range input {
		if m == nil {
			return ChatReq{}, fmt.Errorf("input[%d] is nil", i)
		}
		msgs = append(msgs, einoMessageToLLM(m))
	}

	tools := c.boundTools
	if len(common.Tools) > 0 {
		tools = common.Tools
	}
	llmTools, err := einoToolsToLLM(tools)
	if err != nil {
		return ChatReq{}, err
	}

	req := ChatReq{
		Messages: msgs,
		Tools:    llmTools,
		UserID:   c.userID,
	}
	if common.Temperature != nil {
		req.Temperature = *common.Temperature
	}
	if common.Model != nil && *common.Model != "" {
		req.Model = *common.Model
	} else if c.model != "" {
		req.Model = c.model
	}
	return req, nil
}

// einoMessageToLLM converts an eino *schema.Message → llm.Message.
// Preserves text, tool calls, and user image inputs.
func einoMessageToLLM(m *schema.Message) Message {
	out := Message{
		Role:       string(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
	}
	for _, part := range m.UserInputMultiContent {
		if part.Type != schema.ChatMessagePartTypeImageURL || part.Image == nil || part.Image.Base64Data == nil {
			continue
		}
		out.Images = append(out.Images, ImageInput{MIMEType: part.Image.MIMEType, Data: *part.Image.Base64Data})
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			args := json.RawMessage(tc.Function.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			})
		}
	}
	return out
}

// einoMessageFromChatResp converts a llm.ChatResp → eino *schema.Message,
// populating ResponseMeta.Usage so BudgetCallbackHandler can read it back
// out on OnEnd.
func einoMessageFromChatResp(resp *ChatResp) *schema.Message {
	if resp == nil {
		return nil
	}
	m := &schema.Message{
		Role:       schema.RoleType(resp.Assistant.Role),
		Content:    resp.Assistant.Content,
		Name:       resp.Assistant.ToolName,
		ToolCallID: resp.Assistant.ToolCallID,
		ToolName:   resp.Assistant.ToolName,
	}
	if len(resp.Assistant.ToolCalls) > 0 {
		m.ToolCalls = make([]schema.ToolCall, 0, len(resp.Assistant.ToolCalls))
		for _, tc := range resp.Assistant.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, schema.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			})
		}
	}
	m.ResponseMeta = &schema.ResponseMeta{
		Usage: &schema.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	return m
}

// einoToolsToLLM converts eino *schema.ToolInfo → llm.ToolSchema.
// ParamsOneOf is rendered to JSON Schema via its public ToOpenAPIV3 path
// when available; absent that, we fall back to a minimal empty object
// schema so the underlying client still sees valid JSON.
func einoToolsToLLM(in []*schema.ToolInfo) ([]ToolSchema, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]ToolSchema, 0, len(in))
	for i, t := range in {
		if t == nil {
			return nil, fmt.Errorf("tool[%d] is nil", i)
		}
		params, err := paramsToJSONSchema(t.ParamsOneOf)
		if err != nil {
			return nil, fmt.Errorf("tool[%d] %q: %w", i, t.Name, err)
		}
		out = append(out, ToolSchema{
			Name:        t.Name,
			Description: t.Desc,
			Parameters:  params,
		})
	}
	return out, nil
}

// paramsToJSONSchema renders a ParamsOneOf into JSON Schema bytes that
// llm.ToolSchema.Parameters expects. Delegates to the public ToJSONSchema
// helper from eino's schema package so we don't duplicate the JSON-Schema
// synthesis logic here.
func paramsToJSONSchema(p *schema.ParamsOneOf) (json.RawMessage, error) {
	if p == nil {
		// Empty-object schema = "no parameters". Some OpenAI-compatible
		// providers reject a missing schema, so emit one explicitly.
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	js, err := p.ToJSONSchema()
	if err != nil {
		return nil, err
	}
	if js == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	raw, err := json.Marshal(js)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
