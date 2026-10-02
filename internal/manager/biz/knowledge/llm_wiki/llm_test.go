package llm_wiki

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type compilerLLMFunc struct {
	version string
	call    func(context.Context, llm.ChatReq) (*llm.ChatResp, error)
}

func (f compilerLLMFunc) Complete(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	return f.call(ctx, req)
}

func (f compilerLLMFunc) ModelVersion() string {
	return f.version
}

type recordingUsageSink struct {
	usages    []llm.Usage
	recordErr error
}

func (s *recordingUsageSink) Record(_ context.Context, usage llm.Usage) error {
	s.usages = append(s.usages, usage)
	return s.recordErr
}

func (s *recordingUsageSink) Close(context.Context) error {
	return nil
}

func testWikiLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestUsageRecordingLLM_RecordsEverySuccessfulCall(t *testing.T) {
	expected := []llm.Usage{
		{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14},
		{PromptTokens: 17, CompletionTokens: 5, TotalTokens: 22},
	}
	callCount := 0
	inner := compilerLLMFunc{
		version: "wiki-model-v1",
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			resp := &llm.ChatResp{
				Assistant: llm.Message{Role: "assistant", Content: "ok"},
				Usage:     expected[callCount],
			}
			callCount++
			return resp, nil
		},
	}
	sink := &recordingUsageSink{}
	recorder := newUsageRecordingLLM(inner, sink, testWikiLogger())

	for range expected {
		resp, err := recorder.Complete(context.Background(), llm.ChatReq{})
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Assistant.Content)
	}

	assert.Equal(t, expected, sink.usages)
	assert.Equal(t, "wiki-model-v1", recorder.ModelVersion())
}

func TestUsageRecordingLLM_WhenInnerCallFails_DoesNotRecord(t *testing.T) {
	innerErr := errors.New("provider unavailable")
	inner := compilerLLMFunc{
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return nil, innerErr
		},
	}
	sink := &recordingUsageSink{}
	recorder := newUsageRecordingLLM(inner, sink, testWikiLogger())

	resp, err := recorder.Complete(context.Background(), llm.ChatReq{})

	require.ErrorIs(t, err, innerErr)
	assert.Nil(t, resp)
	assert.Empty(t, sink.usages)
}

func TestUsageRecordingLLM_WhenRecordFails_ReturnsProviderResponse(t *testing.T) {
	expected := &llm.ChatResp{
		Assistant: llm.Message{Role: "assistant", Content: "ok"},
		Usage:     llm.Usage{PromptTokens: 9, CompletionTokens: 4, TotalTokens: 13},
	}
	inner := compilerLLMFunc{
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return expected, nil
		},
	}
	sink := &recordingUsageSink{recordErr: errors.New("database unavailable")}
	recorder := newUsageRecordingLLM(inner, sink, testWikiLogger())

	resp, err := recorder.Complete(context.Background(), llm.ChatReq{})

	require.NoError(t, err)
	assert.Same(t, expected, resp)
	assert.Equal(t, []llm.Usage{expected.Usage}, sink.usages)
}

func TestBuildCompilerWithUsageSink_WrapsAllLLMStagesWithoutMutatingBase(t *testing.T) {
	inner := compilerLLMFunc{
		version: "wiki-model-v1",
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return &llm.ChatResp{Assistant: llm.Message{Role: "assistant", Content: "ok"}}, nil
		},
	}
	base := &buildCompiler{llm: inner, log: testWikiLogger()}

	perJob := base.withUsageSink(&recordingUsageSink{})

	require.NotSame(t, base, perJob)
	assert.Nil(t, base.summarizer)
	assert.Nil(t, base.planner)
	assert.Nil(t, base.writer)
	assert.IsType(t, &usageRecordingLLM{}, perJob.summarizer.llm)
	assert.IsType(t, &usageRecordingLLM{}, perJob.planner.llm)
	assert.IsType(t, &usageRecordingLLM{}, perJob.writer.llm)
}
