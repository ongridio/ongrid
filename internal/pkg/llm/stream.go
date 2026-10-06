package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// ChatStreamChunk carries one incremental model message. Text chunks only
// contain newly received content; complete tool calls and usage metadata are
// delivered in the final chunk before io.EOF.
type ChatStreamChunk struct {
	Message Message
	Usage   *Usage
}

// ChatStreamReader is the streaming counterpart of Client.Chat. It follows
// the same EOF/error contract as schema.StreamReader: the final successful
// read is followed by io.EOF, and the caller must always call Close.
type ChatStreamReader interface {
	Recv() (*ChatStreamChunk, error)
	Close()
}

// StreamingClient is an optional capability so existing Client test doubles
// and background callers can continue implementing Chat only.
type StreamingClient interface {
	ChatStream(ctx context.Context, req ChatReq) (ChatStreamReader, error)
}

var ErrStreamingUnsupported = errors.New("llm: streaming unsupported")

// ChatStream opens an OpenAI-compatible streaming chat completion. It shares
// credential resolution, budget gating, request translation and metrics with
// the blocking Chat path.
func (c *openaiClient) ChatStream(ctx context.Context, req ChatReq) (ChatStreamReader, error) {
	apiKey, defaultModel, baseURL, _ := c.effectiveCreds(ctx)
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}
	model := req.Model
	if model == "" {
		model = defaultModel
	}
	if c.budget != nil {
		if err := c.budget.Check(ctx, req.UserID, estimatePromptTokens(req.Messages)); err != nil {
			c.metrics.requestsTotal.WithLabelValues(model, "budget_exceeded").Inc()
			c.log.Warn("llm budget check refused",
				"model", model,
				"user_id", req.UserID,
			)
			return nil, err
		}
	}

	sdkReq, err := c.toOpenAIReq(req, model)
	if err != nil {
		return nil, fmt.Errorf("llm: build stream request: %w", err)
	}
	sdkReq.Stream = true
	sdkReq.StreamOptions = &openai.StreamOptions{IncludeUsage: true}

	callCtx := ctx
	var cancel context.CancelFunc
	if _, ok := ctx.Deadline(); !ok {
		callCtx, cancel = context.WithTimeout(ctx, c.cfg.Timeout)
	}
	if needsDeepSeekReasoningCompatibility(sdkReq) {
		callCtx = context.WithValue(callCtx, deepSeekReasoningKey{}, true)
	}

	sdk := c.sdkFor(apiKey, baseURL)
	start := time.Now()
	stream, err := sdk.CreateChatCompletionStream(callCtx, sdkReq)
	if err != nil {
		c.metrics.requestsTotal.WithLabelValues(model, "error").Inc()
		c.metrics.requestSeconds.WithLabelValues(model).Observe(time.Since(start).Seconds())
		return nil, fmt.Errorf("llm: chat completion stream: %w", err)
	}

	return &openAIStreamReader{
		ctx:              callCtx,
		stream:           stream,
		client:           c,
		cancel:           cancel,
		model:            model,
		userID:           req.UserID,
		promptEstimate:   estimatePromptTokens(req.Messages),
		startedAt:        start,
		toolCallsByIndex: make(map[int]*streamToolCall),
	}, nil
}

type streamToolCall struct {
	index     int
	id        string
	name      string
	arguments strings.Builder
}

func (c timeoutClient) ChatStream(ctx context.Context, req ChatReq) (ChatStreamReader, error) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		if streamer, ok := c.inner.(StreamingClient); ok {
			return streamer.ChatStream(ctx, req)
		}
		return nil, ErrStreamingUnsupported
	}
	timeout := c.provider(ctx)
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	streamCtx, cancel := context.WithTimeout(ctx, timeout)
	if streamer, ok := c.inner.(StreamingClient); ok {
		reader, err := streamer.ChatStream(streamCtx, req)
		if err != nil {
			cancel()
			return nil, err
		}
		return &cancelingChatStreamReader{inner: reader, cancel: cancel}, nil
	}
	cancel()
	return nil, ErrStreamingUnsupported
}

type cancelingChatStreamReader struct {
	inner     ChatStreamReader
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func (r *cancelingChatStreamReader) Recv() (*ChatStreamChunk, error) {
	return r.inner.Recv()
}

func (r *cancelingChatStreamReader) Close() {
	r.closeOnce.Do(func() {
		r.inner.Close()
		r.cancel()
	})
}

type openAIStreamReader struct {
	ctx            context.Context
	stream         *openai.ChatCompletionStream
	client         *openaiClient
	cancel         context.CancelFunc
	model          string
	userID         uint64
	promptEstimate int
	startedAt      time.Time

	closeOnce  sync.Once
	finishOnce sync.Once
	finished   bool
	content    strings.Builder
	reasoning  strings.Builder
	// reasoningForwarded prevents the EOF metadata chunk from repeating
	// reasoning text that was already emitted incrementally.
	reasoningForwarded bool
	role               string
	usage              Usage

	toolCallsMu       sync.Mutex
	toolCallsByIndex  map[int]*streamToolCall
	toolCallOrder     []int
	nextToolCallIndex int
}

func (r *openAIStreamReader) Recv() (*ChatStreamChunk, error) {
	if r.finished {
		return nil, io.EOF
	}

	resp, err := r.stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			r.finished = true
			chunk := r.finalChunk()
			r.finish(true, nil)
			return chunk, nil
		}
		r.finished = true
		r.finish(false, err)
		return nil, fmt.Errorf("llm: receive chat completion stream: %w", err)
	}

	if resp.Model != "" {
		r.model = resp.Model
	}
	if resp.Usage != nil {
		r.usage = Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	if len(resp.Choices) == 0 {
		return &ChatStreamChunk{Message: Message{Role: schemaAssistantRole}}, nil
	}

	choice := resp.Choices[0]
	delta := choice.Delta
	if delta.Role != "" {
		r.role = delta.Role
	}
	if delta.ReasoningContent != "" {
		r.reasoning.WriteString(delta.ReasoningContent)
		r.reasoningForwarded = true
	}
	r.addToolCallDeltas(delta.ToolCalls)

	if delta.ReasoningContent != "" {
		return &ChatStreamChunk{
			Message: Message{
				Role:             r.assistantRole(),
				ReasoningContent: delta.ReasoningContent,
				Content:          delta.Content,
			},
		}, nil
	}
	if delta.Content == "" {
		return &ChatStreamChunk{Message: Message{Role: schemaAssistantRole}}, nil
	}
	r.content.WriteString(delta.Content)
	return &ChatStreamChunk{
		Message: Message{
			Role:    r.assistantRole(),
			Content: delta.Content,
		},
	}, nil
}

func (r *openAIStreamReader) Close() {
	r.closeOnce.Do(func() {
		r.stream.Close()
		if !r.finished {
			r.finished = true
			r.finish(false, context.Canceled)
			if r.cancel != nil {
				defer r.cancel()
			}
		}
	})
}

func (r *openAIStreamReader) assistantRole() string {
	if r.role != "" {
		return r.role
	}
	return schemaAssistantRole
}

func (r *openAIStreamReader) addToolCallDeltas(deltas []openai.ToolCall) {
	if len(deltas) == 0 {
		return
	}
	r.toolCallsMu.Lock()
	defer r.toolCallsMu.Unlock()
	for _, delta := range deltas {
		index := r.nextToolCallIndex
		if delta.Index != nil {
			index = *delta.Index
		}
		call, ok := r.toolCallsByIndex[index]
		if !ok {
			call = &streamToolCall{index: index}
			r.toolCallsByIndex[index] = call
			r.toolCallOrder = append(r.toolCallOrder, index)
		}
		if delta.Index == nil {
			r.nextToolCallIndex++
		}
		if delta.ID != "" {
			call.id = delta.ID
		}
		if delta.Function.Name != "" {
			call.name = delta.Function.Name
		}
		call.arguments.WriteString(delta.Function.Arguments)
	}
}

func (r *openAIStreamReader) finalChunk() *ChatStreamChunk {
	usage := r.usage
	if usage.TotalTokens == 0 {
		usage.PromptTokens = r.promptEstimate
		usage.CompletionTokens = estimateStringTokens(r.content.String()) + estimateStringTokens(r.reasoning.String())
		for _, call := range r.toolCallsSnapshot() {
			usage.CompletionTokens += estimateStringTokens(call.Name + string(call.Args))
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}
	r.usage = usage

	msg := Message{
		Role: r.assistantRole(),
	}
	if !r.reasoningForwarded {
		msg.ReasoningContent = r.reasoning.String()
	}
	if calls := r.toolCallsSnapshot(); len(calls) > 0 {
		msg.ToolCalls = calls
	}
	return &ChatStreamChunk{
		Message: msg,
		Usage:   &usage,
	}
}

func (r *openAIStreamReader) toolCallsSnapshot() []ToolCall {
	r.toolCallsMu.Lock()
	defer r.toolCallsMu.Unlock()
	if len(r.toolCallOrder) == 0 {
		return nil
	}
	indexes := append([]int(nil), r.toolCallOrder...)
	sort.Ints(indexes)
	out := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := r.toolCallsByIndex[index]
		args := call.arguments.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out = append(out, ToolCall{
			ID:   call.id,
			Name: call.name,
			Args: []byte(args),
		})
	}
	return out
}

func (r *openAIStreamReader) finish(success bool, cause error) {
	r.finishOnce.Do(func() {
		if r.client == nil {
			return
		}
		result := "success"
		if !success {
			result = "error"
		}
		r.client.metrics.requestSeconds.WithLabelValues(r.model).Observe(time.Since(r.startedAt).Seconds())
		r.client.metrics.requestsTotal.WithLabelValues(r.model, result).Inc()
		if !success {
			return
		}

		usage := r.usage
		r.client.metrics.tokensTotal.WithLabelValues(r.model, "prompt").Add(float64(usage.PromptTokens))
		r.client.metrics.tokensTotal.WithLabelValues(r.model, "completion").Add(float64(usage.CompletionTokens))
		if r.client.budget != nil {
			if err := r.client.budget.Record(r.ctx, r.userID, usage); err != nil {
				r.client.log.Warn("llm stream budget record failed",
					"model", r.model,
					"user_id", r.userID,
					"err", err,
				)
			}
		}
		if r.cancel != nil {
			defer r.cancel()
		}
		r.client.log.Info("llm chat completion stream",
			"model", r.model,
			"user_id", r.userID,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens,
			"duration", time.Since(r.startedAt),
			"cause", cause,
		)
	})
}

const schemaAssistantRole = "assistant"

func estimateStringTokens(value string) int {
	return len(value) / 4
}

func (m *MultiClient) ChatStream(ctx context.Context, req ChatReq) (ChatStreamReader, error) {
	subs, _, defID, allowFallback := m.activeSubs(ctx)
	id := strings.TrimSpace(req.Provider)
	if id == "" {
		id = defID
	}

	var (
		reader ChatStreamReader
		err    error
	)
	switch {
	case id == "":
		if !allowFallback || m.fallback == nil {
			err = errors.New("llm: no providers configured")
		} else if streamer, ok := m.fallback.(StreamingClient); ok {
			reader, err = streamer.ChatStream(ctx, req)
		} else {
			err = ErrStreamingUnsupported
		}
	default:
		sub, ok := subs[id]
		if !ok {
			err = fmt.Errorf("llm: provider %q not configured", id)
		} else if streamer, ok := sub.(StreamingClient); ok {
			reader, err = streamer.ChatStream(ctx, req)
		} else {
			err = fmt.Errorf("%w: provider %q", ErrStreamingUnsupported, id)
		}
	}

	providerLabel := id
	if providerLabel == "" {
		providerLabel = "fallback"
	}
	modelLabel := strings.TrimSpace(req.Model)
	if modelLabel == "" {
		modelLabel = "(default)"
	}
	if err != nil {
		prom.ObserveLLMCall(providerLabel, modelLabel, llmStatusFor(err), 0, 0, 0)
		return nil, err
	}
	return &multiChatStreamReader{
		inner:    reader,
		provider: providerLabel,
		model:    modelLabel,
		started:  time.Now(),
	}, nil
}

type multiChatStreamReader struct {
	inner      ChatStreamReader
	provider   string
	model      string
	started    time.Time
	usage      Usage
	closeOnce  sync.Once
	finishOnce sync.Once
}

func (r *multiChatStreamReader) Recv() (*ChatStreamChunk, error) {
	chunk, err := r.inner.Recv()
	if chunk != nil && chunk.Usage != nil {
		r.usage = *chunk.Usage
	}
	if errors.Is(err, io.EOF) {
		r.finish("ok")
	} else if err != nil {
		r.finish(llmStatusFor(err))
	}
	return chunk, err
}

func (r *multiChatStreamReader) Close() {
	r.closeOnce.Do(func() {
		r.inner.Close()
		r.finish("timeout")
	})
}

func (r *multiChatStreamReader) finish(status string) {
	r.finishOnce.Do(func() {
		prom.ObserveLLMCall(
			r.provider,
			r.model,
			status,
			time.Since(r.started).Seconds(),
			r.usage.PromptTokens,
			r.usage.CompletionTokens,
		)
	})
}
