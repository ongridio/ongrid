package llm_wiki

import (
	"context"
	"errors"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// Repository is the persistence the Wiki workflow needs from the data layer.
type Repository interface {
	UpsertSourceVersion(ctx context.Context, source *model.Source, version *model.SourceVersion) (*model.Source, *model.SourceVersion, bool, error)
	ListSources(ctx context.Context, tenantID uint64, status string, limit int) ([]*model.Source, int64, error)
	ListSourcesAfter(ctx context.Context, tenantID uint64, status string, afterID uint64, limit int) ([]*model.Source, error)
	GetSource(ctx context.Context, tenantID, id uint64) (*model.Source, error)
	DeleteSource(ctx context.Context, tenantID, id uint64) error
	GetVersion(ctx context.Context, tenantID, id uint64) (*model.SourceVersion, error)
	CreateJob(ctx context.Context, job *model.CompileJob) error
	ListJobs(ctx context.Context, tenantID uint64, limit int) ([]*model.CompileJob, int64, error)
	HasActiveJob(ctx context.Context, tenantID uint64) (bool, error)
	GetJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error)
	RetryJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error)
	CancelJob(ctx context.Context, tenantID, id uint64) (*model.CompileJob, error)
	ClaimJob(ctx context.Context, tenantID uint64, owner string, lease time.Duration) (*model.CompileJob, error)
	ClaimJobByID(ctx context.Context, tenantID, id uint64, owner string, lease time.Duration) (*model.CompileJob, error)
	UpdateJob(ctx context.Context, tenantID, id uint64, status, stage, errMessage string) error
	IsCancelRequested(ctx context.Context, tenantID, id uint64) (bool, error)
	MarkSourceStatus(ctx context.Context, tenantID, id uint64, status string) error
	CreateBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error)
	GetBuild(ctx context.Context, tenantID, buildID uint64) (*model.WikiBuild, error)
	GetActiveBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error)
	UpdateBuildStatus(ctx context.Context, tenantID, buildID uint64, status, errorMsg string) error
	UpdateBuildPageCount(ctx context.Context, tenantID, buildID uint64, count int) error
	ActivateBuild(ctx context.Context, tenantID, buildID uint64) error
	DeleteBuild(ctx context.Context, tenantID, buildID uint64) error

	CreatePage(ctx context.Context, page *model.WikiBuildPage) error
	CreatePagesBatch(ctx context.Context, pages []*model.WikiBuildPage) error
	GetPage(ctx context.Context, tenantID, buildID uint64, pageID string) (*model.WikiBuildPage, error)
	ListPagesByBuild(ctx context.Context, tenantID, buildID uint64) ([]*model.WikiBuildPage, error)
	DeleteBuildPages(ctx context.Context, tenantID, buildID uint64) error
}

// BuildRepository is the narrower persistence surface the build compiler needs.
type BuildRepository interface {
	ListSources(ctx context.Context, tenantID uint64, status string, limit int) ([]*model.Source, int64, error)
	ListSourcesAfter(ctx context.Context, tenantID uint64, status string, afterID uint64, limit int) ([]*model.Source, error)
	GetSource(ctx context.Context, tenantID, id uint64) (*model.Source, error)
	GetVersion(ctx context.Context, tenantID, id uint64) (*model.SourceVersion, error)
	CreateBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error)
	GetBuild(ctx context.Context, tenantID, buildID uint64) (*model.WikiBuild, error)
	GetActiveBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error)
	UpdateBuildStatus(ctx context.Context, tenantID, buildID uint64, status, errorMsg string) error
	UpdateBuildPageCount(ctx context.Context, tenantID, buildID uint64, count int) error
	ActivateBuild(ctx context.Context, tenantID, buildID uint64) error
	DeleteBuild(ctx context.Context, tenantID, buildID uint64) error
	CreatePage(ctx context.Context, page *model.WikiBuildPage) error
	CreatePagesBatch(ctx context.Context, pages []*model.WikiBuildPage) error
	GetPage(ctx context.Context, tenantID, buildID uint64, pageID string) (*model.WikiBuildPage, error)
	ListPagesByBuild(ctx context.Context, tenantID, buildID uint64) ([]*model.WikiBuildPage, error)
	DeleteBuildPages(ctx context.Context, tenantID, buildID uint64) error
}

// BuildJobCancellationChecker exposes the one job cancellation flag the build
// pipeline needs so cancellation stays a Biz-side concern.
type BuildJobCancellationChecker interface {
	IsCancelRequested(ctx context.Context, tenantID, jobID uint64) (bool, error)
}

// CompilerLLM is the single LLM entry point every compilation stage shares. The
// implementation pins provider and model, so the Wiki contract never silently
// follows the interactive chat default.
type CompilerLLM interface {
	Complete(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error)
	ModelVersion() string
}

// TokenUsageRecorder opens the token accounting session of one compile job.
type TokenUsageRecorder interface {
	Start(ctx context.Context, jobID uint64) (TokenUsageSink, error)
}

// TokenUsageSink records the tokens one compile job spends.
type TokenUsageSink interface {
	Record(ctx context.Context, usage llm.Usage) error
	Close(ctx context.Context) error
}

// SearchIndex is the Wiki search index. IndexPage and Clear keep the active
// published snapshot in sync; Search answers queries for the API.
type SearchIndex interface {
	IndexPage(ctx context.Context, document IndexDocument) error
	Clear(ctx context.Context, tenantID uint64) error
	Search(ctx context.Context, tenantID uint64, query string, limit int) ([]SearchHit, error)
}

// ErrNoPendingJob is returned when no compile job is waiting to be claimed.
var ErrNoPendingJob = errors.New("no pending wiki compile job")

// CompileTriggerOption configures request-triggered compilation.
type CompileTriggerOption struct {
	Owner   string
	Timeout time.Duration
}

// noopTokenUsageSink is used when no usage recorder is configured.
type noopTokenUsageSink struct{}

func (noopTokenUsageSink) Record(context.Context, llm.Usage) error { return nil }
func (noopTokenUsageSink) Close(context.Context) error             { return nil }
