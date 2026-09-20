package llm_wiki

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// llmAdapter binds Wiki compilation to one provider/model on top of the shared
// LLM client. Stage-specific request construction stays inside the compilation
// stages; this adapter only executes the request and exposes the configured
// model identity.
type llmAdapter struct {
	client       llm.Client
	provider     string
	model        string
	modelVersion string
}

// NewLLMAdapter binds Wiki compilation to one provider/model. Provider and
// model are explicit on purpose: Wiki output is a machine-validated contract
// and must not silently follow the interactive chat default when multiple
// providers are configured.
func NewLLMAdapter(client llm.Client, provider, model, modelVersion string) *llmAdapter {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	modelVersion = strings.TrimSpace(modelVersion)
	if modelVersion == "" {
		modelVersion = "unknown"
	}
	return &llmAdapter{client: client, provider: provider, model: model, modelVersion: modelVersion}
}

// ModelVersion reports the configured model identity.
func (a *llmAdapter) ModelVersion() string {
	if a == nil || strings.TrimSpace(a.modelVersion) == "" {
		return "unknown"
	}
	return a.modelVersion
}

// Complete executes one compiler stage request.
func (a *llmAdapter) Complete(ctx context.Context, request llm.ChatReq) (*llm.ChatResp, error) {
	if a == nil || a.client == nil {
		return nil, errors.New("llmwiki: LLM client is not configured")
	}
	request.Provider = a.provider
	request.Model = a.model
	resp, err := a.client.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("llmwiki: compiler returned an empty response")
	}
	if strings.TrimSpace(resp.Assistant.Content) == "" {
		return resp, errors.New("llmwiki: compiler returned empty JSON")
	}
	return resp, nil
}

var _ CompilerLLM = (*llmAdapter)(nil)

// usageRecordingLLM records the provider-reported usage of every successful
// compiler call. Accounting is best-effort: a persistence failure is logged
// but must not turn a valid provider response into a failed Wiki build.
type usageRecordingLLM struct {
	inner CompilerLLM
	sink  TokenUsageSink
	log   *slog.Logger
}

func newUsageRecordingLLM(inner CompilerLLM, sink TokenUsageSink, log *slog.Logger) *usageRecordingLLM {
	if log == nil {
		log = slog.Default()
	}
	return &usageRecordingLLM{inner: inner, sink: sink, log: log}
}

func (a *usageRecordingLLM) Complete(ctx context.Context, request llm.ChatReq) (*llm.ChatResp, error) {
	if a == nil || a.inner == nil {
		return nil, errors.New("llmwiki: usage recorder has no compiler LLM")
	}
	resp, err := a.inner.Complete(ctx, request)
	if err != nil {
		return resp, err
	}
	if resp == nil {
		return nil, errors.New("llmwiki: usage recorder received an empty response")
	}
	if a.sink != nil {
		if err := a.sink.Record(ctx, resp.Usage); err != nil {
			a.log.ErrorContext(ctx, "llmwiki: record token usage failed", slog.Any("err", err))
		}
	}
	return resp, nil
}

func (a *usageRecordingLLM) ModelVersion() string {
	if a == nil || a.inner == nil {
		return "unknown"
	}
	return a.inner.ModelVersion()
}

var _ CompilerLLM = (*usageRecordingLLM)(nil)
