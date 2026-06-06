// Package llm is the real OpenAI-backed chat/tool-calling client.
//
// Red line: no provider abstraction — interface follows OpenAI's
// shape. SDK is github.com/sashabaranov/go-openai.
//
// Red line: Prom metric labels MUST NOT contain user_id / org_id /
// session_id. Allowed labels: model, kind, result.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ongridio/ongrid/internal/pkg/zhipuauth"
)

// Sentinel errors.
var (
	// ErrBudgetExceeded is returned when the daily token budget is hit.
	ErrBudgetExceeded = errors.New("llm: budget exceeded")
	// ErrNoAPIKey is returned by the noop client when OPENAI_API_KEY is unset.
	ErrNoAPIKey = errors.New("llm: OPENAI_API_KEY not set")
)

// defaultTimeout is the fallback for callers that build a Chat request
// without putting a deadline on their context. 120s is the project-wide
// unification floor — short enough that a stuck request still gives up
// on a human-grade timescale, long enough that the slowest mainstream
// reasoning model finishes a tool-rich turn without false-failing
// (Anthropic Opus 4.x extended, DeepSeek v4 reasoning, GPT-5.x). The
// 30s prior default broke once the cluster default moved to DeepSeek.
const defaultTimeout = 120 * time.Second

// Config is the LLM client configuration.
//
// BaseURL is optional and lets us point at Azure / Fireworks / a local vLLM
// without reshaping the interface. Timeout applies when the caller's ctx has
// no deadline; default is 30s.
type Config struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

// Message is one entry in the chat completions messages array. The shape is
// OpenAI-flavored on purpose (no provider abstraction).
//
// Role semantics:
//   - "user" : user prompt; Content is required.
//   - "assistant" : model output; Content may be empty when ToolCalls is set.
//   - "tool" : tool result; ToolCallID references the assistant tool call;
//     ToolName is an optional hint for logs.
//   - "system" : system prompt; Content is required.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	ToolName   string
}

// ToolCall is one tool invocation requested by the assistant.
type ToolCall struct {
	ID   string          // provider-assigned id, e.g. "call_abc"
	Name string          // tool name
	Args json.RawMessage // arguments JSON blob as produced by the model
}

// ToolSchema is the JSON-Schema description of a tool exposed to the model.
// Parameters is passed through as-is (JSON Schema draft-07).
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Usage captures token counts as reported by OpenAI.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ChatReq is the input to Client.Chat.
//
// Provider is the optional provider override (e.g. "openai", "anthropic",
// "zhipu", "gemini"). Empty → router uses the default provider. The
// non-multi-provider single-client path ignores Provider.
type ChatReq struct {
	Model       string
	Provider    string
	Messages    []Message
	Tools       []ToolSchema
	Temperature float32
	UserID      uint64 // optional; used for budget scoping + logging only
}

// ChatResp is the output of Client.Chat.
type ChatResp struct {
	Assistant Message // role=assistant; may have empty Content + non-empty ToolCalls
	Usage     Usage
}

// BudgetChecker gates Chat requests against a token budget. Called with an
// estimated prompt size BEFORE the network call; Record is called AFTER
// success with the actual Usage. A nil BudgetChecker means no limit.
type BudgetChecker interface {
	Check(ctx context.Context, userID uint64, estPromptTokens int) error
	Record(ctx context.Context, userID uint64, usage Usage) error
}

// Client is the LLM client surface consumed by the AIOps agent.
type Client interface {
	Chat(ctx context.Context, req ChatReq) (*ChatResp, error)
}

// Resolver supplies the LLM credentials at call time. The seam exists so
// admin-editable settings (system_settings table, biz/setting service)
// can override the env-derived bootstrap values without restarting the
// manager. An empty string from any field means "fall back to the env-
// configured value".
//
// The implementation is expected to be cheap (an in-memory cache lookup);
// a small TTL cache lives inside the LLM client so even a slow Resolver
// does not block hot Chat() paths.
type Resolver interface {
	Resolve(ctx context.Context) (apiKey, model, baseURL string, err error)
}

// New builds a Client.
//
// If cfg.APIKey is empty, a noop client is returned whose Chat always fails
// with ErrNoAPIKey (useful for local dev without OPENAI_API_KEY).
//
// Metrics are registered on reg; pass nil to register on the default
// registerer (a warn is logged once).
//
// NOTE: logger is derived from slog.Default() to keep the 3-arg signature
// frozen in The agent-loop caller can still inject its own slog
// attrs via the ctx-carried logger if desired.
func New(cfg Config, budget BudgetChecker, reg *prometheus.Registry) Client {
	return NewWithResolver(cfg, nil, budget, reg)
}

// NewWithResolver is New with an optional dynamic credential source. The
// resolver, when non-nil, is queried before each Chat call (with a small
// internal TTL cache) and its non-empty fields override cfg. Empty fields
// fall back to cfg, which itself was env-seeded at startup.
//
// When the effective API key is empty (neither resolver nor cfg has one),
// Chat returns ErrNoAPIKey — the same behaviour as the noop client.
func NewWithResolver(cfg Config, resolver Resolver, budget BudgetChecker, reg *prometheus.Registry) Client {
	log := slog.Default().With(slog.String("component", "llm"))

	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}

	// With no resolver and no env key, fall back to noop so callers see a
	// clean ErrNoAPIKey instead of a confusing 401 from the OpenAI SDK.
	if resolver == nil && cfg.APIKey == "" {
		log.Warn("OPENAI_API_KEY empty and no Resolver wired — returning noop client; Chat will fail with ErrNoAPIKey")
		return &noopClient{}
	}

	return &openaiClient{
		cfg:        cfg,
		resolver:   resolver,
		budget:     budget,
		metrics:    newMetrics(reg, log),
		log:        log,
		resolveTTL: 60 * time.Second,
	}
}

type openaiClient struct {
	cfg      Config
	resolver Resolver
	budget   BudgetChecker
	metrics  *metrics
	log      *slog.Logger

	// SDK clients are keyed by (apiKey, baseURL) so a settings change
	// transparently swaps the underlying *openai.Client without us having
	// to rebuild it on every Chat.
	sdkMu    sync.Mutex
	sdkCache map[sdkKey]*openai.Client

	// Resolver TTL cache so hot paths don't pay the DB round-trip per call.
	resolveTTL time.Duration
	resolveMu  sync.Mutex
	resolved   resolvedCreds
	resolvedAt time.Time
}

type sdkKey struct {
	apiKey  string
	baseURL string
}

type resolvedCreds struct {
	apiKey  string
	model   string
	baseURL string
}

// effectiveCreds returns the credentials for the next Chat. Resolver values
// override cfg per-field; missing/empty resolver fields fall back to the
// env-seeded cfg. The result is cached for resolveTTL.
func (c *openaiClient) effectiveCreds(ctx context.Context) (string, string, string, error) {
	if c.resolver == nil {
		return c.cfg.APIKey, c.cfg.Model, c.cfg.BaseURL, nil
	}
	c.resolveMu.Lock()
	defer c.resolveMu.Unlock()
	if !c.resolvedAt.IsZero() && time.Since(c.resolvedAt) < c.resolveTTL {
		return c.resolved.apiKey, c.resolved.model, c.resolved.baseURL, nil
	}
	apiKey, model, baseURL, err := c.resolver.Resolve(ctx)
	if err != nil {
		// Soft-fail: log and fall back to cfg so a transient DB hiccup
		// does not break the chat surface.
		c.log.Warn("resolver failed; falling back to env-seeded cfg", slog.Any("err", err))
		apiKey, model, baseURL = "", "", ""
	}
	if apiKey == "" {
		apiKey = c.cfg.APIKey
	}
	if model == "" {
		model = c.cfg.Model
	}
	if baseURL == "" {
		baseURL = c.cfg.BaseURL
	}
	c.resolved = resolvedCreds{apiKey: apiKey, model: model, baseURL: baseURL}
	c.resolvedAt = time.Now()
	return apiKey, model, baseURL, nil
}

// sdkFor returns a cached *openai.Client for the (apiKey, baseURL) pair.
// The cache survives forever (it tops out at a handful of entries even
// across a year of edits) but the value count is small enough to ignore.
//
// For Zhipu (open.bigmodel.cn) we install a custom HTTP transport that
// rewrites Authorization to a freshly-signed JWT on every request
// (raw <id>.<secret>) gets rejected by Zhipu's v4 endpoints with
// 401). The SDK's static apiKey field becomes irrelevant — our
// transport always overrides — but we still feed it the raw key so the
// (apiKey, baseURL) cache key stays stable across calls.
func (c *openaiClient) sdkFor(apiKey, baseURL string) *openai.Client {
	k := sdkKey{apiKey: apiKey, baseURL: baseURL}
	c.sdkMu.Lock()
	defer c.sdkMu.Unlock()
	if c.sdkCache == nil {
		c.sdkCache = make(map[sdkKey]*openai.Client)
	}
	if sdk, ok := c.sdkCache[k]; ok {
		return sdk
	}
	sdkCfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		sdkCfg.BaseURL = baseURL
	}
	if zhipuauth.LooksLikeZhipuURL(baseURL) && zhipuauth.LooksLikeZhipuKey(apiKey) {
		sdkCfg.HTTPClient = &http.Client{
			Transport: &zhipuJWTTransport{apiKey: apiKey, base: http.DefaultTransport},
		}
	}
	sdk := openai.NewClientWithConfig(sdkCfg)
	c.sdkCache[k] = sdk
	return sdk
}

// zhipuJWTTransport rewrites the Authorization header on every outbound
// request to a freshly-signed Zhipu JWT (TTL 1h). go-openai's stock
// Authorization header (raw apiKey as Bearer) is silently dropped in
// favour of ours.
type zhipuJWTTransport struct {
	apiKey string
	base   http.RoundTripper
}

func (t *zhipuJWTTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := zhipuauth.SignJWT(t.apiKey, time.Hour)
	if err != nil {
		return nil, err
	}
	// Clone first — Go's RoundTripper contract forbids mutating the
	// request the caller passed in (some HTTP middleware reuses it).
	cloned := req.Clone(req.Context())
	cloned.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(cloned)
}

// Chat implements Client.
func (c *openaiClient) Chat(ctx context.Context, req ChatReq) (*ChatResp, error) {
	// Resolve effective credentials for this call. Resolver overrides cfg;
	// empty fields fall back to cfg (env-seeded at startup).
	apiKey, defaultModel, baseURL, _ := c.effectiveCreds(ctx)
	if apiKey == "" {
		// No env key, no DB-seeded key. Match the noop-client contract so
		// the caller sees a single sentinel.
		return nil, ErrNoAPIKey
	}
	model := req.Model
	if model == "" {
		model = defaultModel
	}

	// 1. Budget gate BEFORE any network call.
	if c.budget != nil {
		if err := c.budget.Check(ctx, req.UserID, estimatePromptTokens(req.Messages)); err != nil {
			c.metrics.requestsTotal.WithLabelValues(model, "budget_exceeded").Inc()
			// Never log user content — we only note the fact and the user bucket.
			c.log.Warn("llm budget check refused",
				slog.Uint64("user_id", req.UserID),
				slog.String("model", model),
			)
			return nil, err
		}
	}

	// 2. Translate to openai.ChatCompletionRequest.
	sdkReq, err := c.toOpenAIReq(req, model)
	if err != nil {
		return nil, fmt.Errorf("llm: build request: %w", err)
	}

	// 3. Bound ctx to cfg.Timeout if caller provided no deadline.
	callCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
		defer cancel()
	}

	// 4. Issue the request through the SDK matching the resolved creds.
	sdk := c.sdkFor(apiKey, baseURL)
	start := time.Now()
	sdkResp, err := sdk.CreateChatCompletion(callCtx, sdkReq)
	dur := time.Since(start)
	c.metrics.requestSeconds.WithLabelValues(model).Observe(dur.Seconds())

	if err != nil {
		// 5. Error path — no Record, no retry (tools not idempotent).
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		c.log.Error("llm chat completion failed",
			slog.String("model", model),
			slog.Duration("duration", dur),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("llm: chat completion: %w", err)
	}

	if len(sdkResp.Choices) == 0 {
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		return nil, fmt.Errorf("llm: empty choices in response")
	}

	// 6. Translate response back.
	assistant, err := fromOpenAIMessage(sdkResp.Choices[0].Message)
	if err != nil {
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		return nil, fmt.Errorf("llm: decode assistant message: %w", err)
	}
	usage := Usage{
		PromptTokens:     sdkResp.Usage.PromptTokens,
		CompletionTokens: sdkResp.Usage.CompletionTokens,
		TotalTokens:      sdkResp.Usage.TotalTokens,
	}

	c.metrics.tokensTotal.WithLabelValues(model, "prompt").Add(float64(usage.PromptTokens))
	c.metrics.tokensTotal.WithLabelValues(model, "completion").Add(float64(usage.CompletionTokens))
	c.metrics.requestsTotal.WithLabelValues(model, "success").Inc()

	// 7. Record actual usage for the budget.
	if c.budget != nil {
		if rerr := c.budget.Record(ctx, req.UserID, usage); rerr != nil {
			// Recording failures must not fail the user's request.
			c.log.Warn("llm budget record failed",
				slog.Uint64("user_id", req.UserID),
				slog.Any("err", rerr),
			)
		}
	}

	// 8. Structured log — NEVER the message content; only shape + usage.
	c.log.Info("llm chat completion",
		slog.String("model", model),
		slog.Uint64("user_id", req.UserID),
		slog.Int("prompt_tokens", usage.PromptTokens),
		slog.Int("completion_tokens", usage.CompletionTokens),
		slog.Int("total_tokens", usage.TotalTokens),
		slog.Int("tool_calls", len(assistant.ToolCalls)),
		slog.Duration("duration", dur),
	)

	return &ChatResp{Assistant: assistant, Usage: usage}, nil
}

// toOpenAIReq translates the public ChatReq to the SDK request shape.
func (c *openaiClient) toOpenAIReq(req ChatReq, model string) (openai.ChatCompletionRequest, error) {
	temp := req.Temperature
	if temp == 0 {
		temp = 0.1
	}

	msgs := make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		sm, err := toOpenAIMessage(m)
		if err != nil {
			return openai.ChatCompletionRequest{}, fmt.Errorf("message[%d]: %w", i, err)
		}
		msgs = append(msgs, sm)
	}

	var tools []openai.Tool
	if len(req.Tools) > 0 {
		tools = make([]openai.Tool, 0, len(req.Tools))
		for i, t := range req.Tools {
			// Parameters is passed through as json.RawMessage; the SDK
			// accepts `any` and re-marshals. Validate it is valid JSON up
			// front so a malformed schema surfaces here, not at send time.
			var params any = t.Parameters
			if len(t.Parameters) > 0 {
				var tmp any
				if err := json.Unmarshal(t.Parameters, &tmp); err != nil {
					return openai.ChatCompletionRequest{}, fmt.Errorf("tool[%d] %q parameters: %w", i, t.Name, err)
				}
				params = json.RawMessage(t.Parameters)
			}
			tools = append(tools, openai.Tool{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  params,
				},
			})
		}
	}

	return openai.ChatCompletionRequest{
		Model:       model,
		Messages:    msgs,
		Tools:       tools,
		Temperature: temp,
	}, nil
}

func toOpenAIMessage(m Message) (openai.ChatCompletionMessage, error) {
	out := openai.ChatCompletionMessage{
		Role:       m.Role,
		Content:    m.Content,
		Name:       m.ToolName,
		ToolCallID: m.ToolCallID,
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]openai.ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: string(tc.Args),
				},
			})
		}
	}
	return out, nil
}

func fromOpenAIMessage(m openai.ChatCompletionMessage) (Message, error) {
	out := Message{
		Role:       m.Role,
		Content:    m.Content,
		ToolName:   m.Name,
		ToolCallID: m.ToolCallID,
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
	return out, nil
}

// estimatePromptTokens is a cheap pre-call estimate: ~4 chars per token is a
// common rule of thumb for English, plus a fixed overhead per message for
// role/tool framing. Good enough to gate budgets; real billing is the Usage
// we get back.
func estimatePromptTokens(msgs []Message) int {
	const perMsgOverhead = 4
	total := 0
	for _, m := range msgs {
		total += perMsgOverhead
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Name) / 4
			total += len(tc.Args) / 4
		}
	}
	return total
}
