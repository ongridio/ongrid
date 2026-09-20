package llm_wiki

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase is the LLM Wiki entry point. It owns the Wiki file tree, the durable
// repository and the compilation pipeline, and exposes the operations the HTTP
// layer calls.
type Usecase struct {
	compiler   *buildCompiler
	repo       Repository
	files      *FileStore
	summarizer CompilerLLM
	indexer    SearchIndex
	log        *slog.Logger
	runCtx     context.Context
	trigger    CompileTriggerOption
	usage      TokenUsageRecorder
}

// jobChecker is the narrow cancellation surface of the durable repository.
type jobChecker interface {
	IsCancelRequested(ctx context.Context, tenantID, jobID uint64) (bool, error)
}

// NewWithUsageRecorder wires Wiki compilation to the existing AIOps chat
// transcript accounting without adding a Wiki-specific statistics table.
func NewWithUsageRecorder(ctx context.Context, repo Repository, files *FileStore, summarizer CompilerLLM, indexer SearchIndex, log *slog.Logger, usage TokenUsageRecorder, trigger ...CompileTriggerOption) (*Usecase, error) {
	return newUsecase(ctx, repo, files, summarizer, indexer, log, usage, trigger...)
}

func newUsecase(ctx context.Context, repo Repository, files *FileStore, summarizer CompilerLLM, indexer SearchIndex, log *slog.Logger, usage TokenUsageRecorder, trigger ...CompileTriggerOption) (*Usecase, error) {
	if repo == nil || files == nil {
		return nil, errors.New("llmwiki: repository and file store are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = slog.Default()
	}
	if err := files.Ensure(ctx); err != nil {
		return nil, err
	}
	var compileTrigger CompileTriggerOption
	if len(trigger) > 0 {
		compileTrigger = trigger[0]
	}
	if compileTrigger.Timeout <= 0 {
		compileTrigger.Timeout = 10 * time.Minute
	}

	created := &Usecase{
		repo:       repo,
		files:      files,
		summarizer: summarizer,
		indexer:    indexer,
		log:        log,
		runCtx:     ctx,
		trigger:    compileTrigger,
		usage:      usage,
		compiler:   newBuildCompiler(repo, nil, repo, files, summarizer, indexer, log),
	}
	if err := created.Reconcile(ctx, DefaultTenantID); err != nil {
		return nil, err
	}
	if err := created.rebuildSearchIndex(ctx, DefaultTenantID); err != nil {
		// The index is derived data. Keep the Wiki available and let the next
		// compile rebuild it instead of blocking startup.
		log.WarnContext(ctx, "llmwiki: search index backfill failed", slog.Any("err", err))
	}
	return created, nil
}

// vectorIndexState is the optional capability the derived index exposes so the
// usecase can backfill Qdrant when the collection is empty, for example right
// after upgrading from table-backed page vectors.
type vectorIndexState interface {
	HasVectors(ctx context.Context, tenantID uint64) (bool, error)
}

// rebuildSearchIndex repopulates lexical and vector entries from the active
// immutable build when the vector store has no points for the tenant. It never
// calls the LLM.
func (u *Usecase) rebuildSearchIndex(ctx context.Context, tenantID uint64) error {
	if u.indexer == nil {
		return nil
	}
	state, ok := u.indexer.(vectorIndexState)
	if !ok {
		return nil
	}
	has, err := state.HasVectors(ctx, tenantID)
	if err != nil || has {
		return err
	}
	active, err := u.repo.GetActiveBuild(ctx, tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("llmwiki: load active build for index backfill: %w", err)
	}
	pages, err := u.repo.ListPagesByBuild(ctx, tenantID, active.ID)
	if err != nil {
		return fmt.Errorf("llmwiki: list pages for index backfill: %w", err)
	}
	artifacts := newArtifactStore(u.files)
	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		content, err := artifacts.readPageBody(ctx, active.ID, page.PageID)
		if err != nil {
			return fmt.Errorf("llmwiki: read page %s for index backfill: %w", page.PageID, err)
		}
		if err := u.indexer.IndexPage(ctx, newIndexDocument(page, content)); err != nil {
			return fmt.Errorf("llmwiki: backfill index page %s: %w", page.PageID, err)
		}
	}
	u.log.InfoContext(ctx, "llmwiki: backfilled search index from active build",
		slog.Uint64("build_id", active.ID), slog.Int("pages", len(pages)))
	return nil
}

// Reconcile restores mirrored raw files from their durable source snapshots.
// One source that cannot be restored is marked failed and skipped: it must not
// stop the Wiki from starting, because a Wiki that never opens cannot be
// repaired by re-syncing.
func (u *Usecase) Reconcile(ctx context.Context, tenantID uint64) error {
	sources, err := listAllSources(ctx, u.repo, tenantID)
	if err != nil {
		return fmt.Errorf("llmwiki: list sources for reconcile: %w", err)
	}
	for _, source := range sources {
		if source.CurrentVersionID == nil {
			continue
		}
		version, err := u.repo.GetVersion(ctx, tenantID, *source.CurrentVersionID)
		if err != nil {
			if !errors.Is(err, errs.ErrNotFound) {
				return err
			}
			// The version row is gone, so there is no snapshot left to restore
			// this source from. It stays unusable until the next sync mirrors
			// it again.
			u.markUnrestorable(ctx, tenantID, source, err)
			continue
		}
		body, err := u.files.Read(ctx, version.SnapshotPath, MaxSourceBytes)
		if err != nil {
			u.markUnrestorable(ctx, tenantID, source, err)
			continue
		}
		rawPath, err := u.files.resolve("raw", source.RawPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(rawPath); errors.Is(err, fs.ErrNotExist) {
			if err := atomicWrite(rawPath, body, 0o640); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// markUnrestorable records a source Reconcile could not put back, without
// letting the recording itself stop the reconcile.
func (u *Usecase) markUnrestorable(ctx context.Context, tenantID uint64, source *model.Source, cause error) {
	if err := u.repo.MarkSourceStatus(ctx, tenantID, source.ID, model.SourceFailed); err != nil && !errors.Is(err, errs.ErrNotFound) {
		u.log.ErrorContext(ctx, "llmwiki: failed to mark an unrestorable source",
			slog.Uint64("source_id", source.ID),
			slog.String("error", err.Error()))
	}
	u.log.WarnContext(ctx, "llmwiki: source left out of reconcile",
		slog.Uint64("source_id", source.ID),
		slog.String("raw_path", source.RawPath),
		slog.String("error", cause.Error()))
}
