package store

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type compileUsageLLM struct {
	usage llm.Usage
}

func (f *compileUsageLLM) Complete(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
	return &llm.ChatResp{
		Assistant: llm.Message{
			Role:    "assistant",
			Content: `{"pages":[{"page_id":"overview","title":"Overview","sections":[{"heading":"Overview","content":"line 1\nline 2\nline 3\nline 4","source_ids":[0]}]}]}`,
		},
		Usage: f.usage,
	}, nil
}

func (f *compileUsageLLM) ModelVersion() string {
	return "fake-wiki-model"
}

type compileUsageRecorder struct {
	usages []llm.Usage
	closed bool
}

func (r *compileUsageRecorder) Start(context.Context, uint64) (biz.TokenUsageSink, error) {
	return &compileUsageSink{recorder: r}, nil
}

type compileUsageSink struct {
	recorder *compileUsageRecorder
}

func (s *compileUsageSink) Record(_ context.Context, usage llm.Usage) error {
	s.recorder.usages = append(s.recorder.usages, usage)
	return nil
}

func (s *compileUsageSink) Close(context.Context) error {
	s.recorder.closed = true
	return nil
}

func TestCompileJob_RecordsSuccessfulLLMUsage(t *testing.T) {
	repo, _ := testRepo(t)

	ctx := context.Background()
	files, err := biz.NewFileStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, files.Ensure(ctx))

	usageRecorder := &compileUsageRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc, err := biz.NewWithUsageRecorder(
		ctx,
		repo,
		files,
		&compileUsageLLM{usage: llm.Usage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150}},
		nil,
		log,
		usageRecorder,
	)
	require.NoError(t, err)

	_, err = uc.SyncOrganizationSources(ctx, []biz.OrganizationSource{{
		ID:      1,
		Title:   "Operations",
		Path:    "ops",
		Content: strings.Repeat("Operational knowledge for the fake Wiki compile. ", 8),
	}})
	require.NoError(t, err)

	job, err := uc.CreateCompileJob(ctx, biz.DefaultTenantID, false, nil)
	require.NoError(t, err)
	require.NoError(t, uc.RunOnce(ctx, biz.DefaultTenantID, "usage-integration-test", time.Minute))

	storedJob, err := repo.GetJob(ctx, biz.DefaultTenantID, job.ID)
	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, storedJob.Status)

	assert.Equal(t, []llm.Usage{{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150}}, usageRecorder.usages)
	assert.True(t, usageRecorder.closed)
}
