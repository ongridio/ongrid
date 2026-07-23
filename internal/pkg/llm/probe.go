package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ongridio/ongrid/internal/pkg/zhipuauth"
)

// ProbeResult is the small, secret-free result returned by
// ProbeChatCompletion. The caller only needs token usage to prove the provider
// returned a structurally valid chat completion; assistant content is
// deliberately discarded.
type ProbeResult struct {
	Usage Usage
}

// ProbeChatCompletion sends one minimal request through the same URL
// normalization and authentication path used by the production LLM client.
// It intentionally skips metrics, logs, retries and budgets: invalid
// credentials are an expected validation outcome and must not pollute runtime
// error telemetry or leak upstream response details into logs.
func ProbeChatCompletion(ctx context.Context, cfg Config) (*ProbeResult, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrNoAPIKey
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("llm: model is empty")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	baseURL := normalizeOpenAIBaseURL(cfg.BaseURL)
	sdkCfg := openai.DefaultConfig(cfg.APIKey)
	if baseURL != "" {
		sdkCfg.BaseURL = baseURL
	}
	transport := http.RoundTripper(http.DefaultTransport)
	if zhipuauth.LooksLikeZhipuURL(baseURL) && zhipuauth.LooksLikeZhipuKey(cfg.APIKey) {
		transport = &zhipuJWTTransport{apiKey: cfg.APIKey, base: transport}
	}
	sdkCfg.HTTPClient = &http.Client{Timeout: cfg.Timeout, Transport: transport}

	sdk := openai.NewClientWithConfig(sdkCfg)
	resp, err := sdk.CreateChatCompletion(callCtx, openai.ChatCompletionRequest{
		Model: cfg.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "Reply with OK."},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("llm probe: chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("llm probe: empty choices in response")
	}
	return &ProbeResult{Usage: Usage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
	}}, nil
}
