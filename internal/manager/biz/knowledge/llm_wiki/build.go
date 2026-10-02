package llm_wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// BuildResult is the outcome of compiling one Wiki build.
type BuildResult struct {
	BuildID   uint64
	PageCount int
	Chunks    int
	Success   bool
	// NoContent reports a corpus with nothing to compile. The build is dropped
	// and the job ends as skipped rather than publishing an empty Wiki.
	NoContent bool
	// Stage names the pipeline step a failed build stopped at, so the compile
	// job can record where it failed.
	Stage string
	Error error
}

// ErrJobCancelled is returned when the owning compile job has a pending
// cancellation request. It stops the compilation before publication.
var ErrJobCancelled = errors.New("llmwiki: compile job cancelled")

// buildCompiler orchestrates the Wiki build pipeline: it loads the corpus,
// chunks and summarizes it, plans the page structure, resolves evidence and
// writes every page into an isolated build that can then be published.
type buildCompiler struct {
	repo       BuildRepository
	jobID      *uint64
	cancelRepo BuildJobCancellationChecker
	files      *FileStore
	artifacts  *artifactStore
	llm        CompilerLLM
	summarizer *summarizer
	planner    *planner
	resolver   *evidenceResolver
	writer     *pageWriter
	indexer    SearchIndex
	log        *slog.Logger
}

// newBuildCompiler wires the compilation stages together. jobID and
// cancelRepo together opt a compiler into durable job cancellation checks.
func newBuildCompiler(repo BuildRepository, jobID *uint64, cancelRepo BuildJobCancellationChecker, files *FileStore, llm CompilerLLM, indexer SearchIndex, log *slog.Logger) *buildCompiler {
	return &buildCompiler{
		repo:       repo,
		jobID:      jobID,
		cancelRepo: cancelRepo,
		files:      files,
		artifacts:  newArtifactStore(files),
		llm:        llm,
		summarizer: newSummarizer(llm, log),
		planner:    newPlanner(llm, log),
		resolver:   newEvidenceResolver(),
		writer:     newPageWriter(llm, log),
		indexer:    indexer,
		log:        log,
	}
}

// bindJob points cancellation checks at the currently running job.
func (c *buildCompiler) bindJob(jobID uint64) {
	c.jobID = &jobID
}

// withUsageSink returns a per-job compiler whose LLM stages record usage into
// sink. The base compiler stays immutable so concurrent jobs cannot overwrite
// each other's accounting session or cancellation binding.
func (c *buildCompiler) withUsageSink(sink TokenUsageSink) *buildCompiler {
	if c == nil || c.llm == nil || sink == nil {
		return c
	}
	clone := *c
	clone.jobID = nil
	recordingLLM := newUsageRecordingLLM(c.llm, sink, c.log)
	clone.summarizer = newSummarizer(recordingLLM, c.log)
	clone.planner = newPlanner(recordingLLM, c.log)
	clone.writer = newPageWriter(recordingLLM, c.log)
	return &clone
}

// Compile runs the whole pipeline and returns the staging build it produced:
// create build → load corpus → chunk → summarize → plan → resolve sources →
// write artifacts → validate → index. A failed or cancelled build is cleaned
// up before returning. sourceIDs is non-empty only those sources are compiled.
func (c *buildCompiler) Compile(ctx context.Context, tenantID uint64, sourceIDs ...uint64) *BuildResult {
	result := &BuildResult{}

	build, err := c.repo.CreateBuild(ctx, tenantID)
	if err != nil {
		result.Stage = "create build"
		result.Error = fmt.Errorf("create build: %w", err)
		return result
	}
	result.BuildID = build.ID
	c.log.InfoContext(ctx, "build: started staging build", slog.Uint64("build_id", build.ID))

	defer func() {
		if !result.Success {
			if cleanupErr := c.cleanupFailedBuild(ctx, tenantID, build.ID); cleanupErr != nil {
				c.log.ErrorContext(ctx, "build: cleanup failed",
					slog.Uint64("build_id", build.ID),
					slog.String("error", cleanupErr.Error()))
			}
		}
	}()

	fail := func(step string, err error) *BuildResult {
		result.Stage = step
		result.Error = fmt.Errorf("%s: %w", step, err)
		c.failBuild(ctx, tenantID, build.ID, result.Error)
		return result
	}

	if c.cancelled(ctx, tenantID) {
		return fail("cancel build", ErrJobCancelled)
	}

	corpus, err := loadCorpus(ctx, c.repo, tenantID, c.log, sourceIDs...)
	if err != nil {
		return fail("load corpus", err)
	}

	loader := newCorpusContentLoader(c.repo, c.files, tenantID)
	if err := prepareCorpusChunks(ctx, corpus, loader, c.log); err != nil {
		return fail("prepare chunks", err)
	}
	result.Chunks = len(corpus.Chunks)
	if len(corpus.Chunks) == 0 {
		// Nothing survived chunking, so there is no evidence to plan from.
		// Asking the planner anyway would either invent pages without sources or
		// fail on an empty plan, so drop the staging build and let the job finish
		// as skipped with the published build untouched.
		if cleanupErr := c.cleanupFailedBuild(ctx, tenantID, build.ID); cleanupErr != nil {
			c.log.ErrorContext(ctx, "build: failed to clean up empty build",
				slog.Uint64("build_id", build.ID),
				slog.String("error", cleanupErr.Error()))
		}
		result.NoContent = true
		result.Success = true
		c.log.InfoContext(ctx, "build: corpus has no chunks, skipping compilation",
			slog.Uint64("build_id", build.ID))
		return result
	}

	if c.cancelled(ctx, tenantID) {
		return fail("summarize corpus", ErrJobCancelled)
	}
	digestItems, err := c.summarizer.Summarize(ctx, corpus)
	if err != nil {
		return fail("summarize corpus", err)
	}

	if c.cancelled(ctx, tenantID) {
		return fail("plan wiki", ErrJobCancelled)
	}
	plan, err := c.planner.Plan(ctx, buildDigest(digestItems))
	if err != nil {
		return fail("plan wiki", err)
	}

	if c.cancelled(ctx, tenantID) {
		return fail("resolve evidence", ErrJobCancelled)
	}
	resolvedPages, err := c.resolver.Resolve(plan, corpus, digestItems)
	if err != nil {
		return fail("resolve evidence", err)
	}

	if c.cancelled(ctx, tenantID) {
		return fail("write pages", ErrJobCancelled)
	}
	pages, err := c.writePages(ctx, tenantID, build.ID, resolvedPages, sourceIDs)
	if err != nil {
		return fail("write pages", err)
	}
	result.PageCount = len(pages)

	if c.cancelled(ctx, tenantID) {
		return fail("validation failed", ErrJobCancelled)
	}
	if validationErrors := c.artifacts.validateBuild(ctx, build.ID, pages); len(validationErrors) > 0 {
		return fail("validation failed", fmt.Errorf("%v", validationErrors))
	}

	if c.cancelled(ctx, tenantID) {
		return fail("mark validated", ErrJobCancelled)
	}
	if err := c.repo.UpdateBuildStatus(ctx, tenantID, build.ID, model.BuildValidated, ""); err != nil {
		result.Stage = "mark validated"
		result.Error = fmt.Errorf("mark validated: %w", err)
		return result
	}

	// Indexing is best effort: a stale search index must not fail a valid build.
	if c.cancelled(ctx, tenantID) {
		return fail("index build pages", ErrJobCancelled)
	}
	if err := c.indexBuildPages(ctx, tenantID, build.ID, pages); err != nil {
		c.log.WarnContext(ctx, "build: indexing failed (non-fatal)",
			slog.Uint64("build_id", build.ID),
			slog.String("error", err.Error()))
	}

	result.Success = true
	c.log.InfoContext(ctx, "build: compilation complete",
		slog.Uint64("build_id", build.ID),
		slog.Int("pages", result.PageCount),
		slog.Int("chunks", result.Chunks))

	return result
}

// Publish atomically activates a validated build and drops the artifacts of the
// build it replaces. It rechecks the job cancellation immediately before the
// activation boundary.
func (c *buildCompiler) Publish(ctx context.Context, tenantID, buildID uint64) error {
	if c.cancelled(ctx, tenantID) {
		if err := c.cleanupFailedBuild(ctx, tenantID, buildID); err != nil {
			return fmt.Errorf("clean up cancelled build: %w", err)
		}
		return ErrJobCancelled
	}

	build, err := c.repo.GetBuild(ctx, tenantID, buildID)
	if err != nil {
		return fmt.Errorf("get build: %w", err)
	}
	if build.Status != model.BuildValidated {
		return fmt.Errorf("cannot publish build in status %q", build.Status)
	}

	// A source deleted while this build was compiling must not come back: the
	// source-set check that guards deletes races the job creation, so the
	// citation check at the activation boundary is what makes it safe.
	if err := c.validateCitedSources(ctx, tenantID, buildID); err != nil {
		if cleanupErr := c.cleanupFailedBuild(ctx, tenantID, buildID); cleanupErr != nil {
			return errors.Join(err, fmt.Errorf("clean up stale build: %w", cleanupErr))
		}
		return err
	}

	previousBuild, _ := c.repo.GetActiveBuild(ctx, tenantID)

	if err := c.repo.ActivateBuild(ctx, tenantID, buildID); err != nil {
		return fmt.Errorf("activate build: %w", err)
	}

	if previousBuild != nil {
		if err := c.artifacts.deleteBuild(ctx, previousBuild.ID); err != nil {
			c.log.WarnContext(ctx, "build: failed to cleanup old build artifacts",
				slog.Uint64("old_build_id", previousBuild.ID),
				slog.String("error", err.Error()))
		}
	}

	c.log.InfoContext(ctx, "build: published", slog.Uint64("build_id", buildID))
	return nil
}

// cancelled checks the owning compile job's durable cancellation flag. It is a
// best-effort gate so cancellation can outpace the caller's context.
func (c *buildCompiler) cancelled(ctx context.Context, tenantID uint64) bool {
	if c.jobID == nil || c.cancelRepo == nil {
		return false
	}
	cancelled, err := c.cancelRepo.IsCancelRequested(ctx, tenantID, *c.jobID)
	if err != nil {
		c.log.ErrorContext(ctx, "build: failed to check job cancellation",
			slog.Uint64("job_id", *c.jobID),
			slog.String("error", err.Error()))
		return false
	}
	return cancelled
}

// writePages renders every resolved page to Markdown, stores its body in the
// build artifact tree and records the page rows for the build. Incremental
// builds copy forward pages that do not depend on any selected source.
func (c *buildCompiler) writePages(ctx context.Context, tenantID, buildID uint64, pages []*resolvedPage, selectedSourceIDs []uint64) ([]*model.WikiBuildPage, error) {
	buildPages := make([]*model.WikiBuildPage, 0, len(pages))

	for _, page := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		content, err := c.writer.WritePage(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("write page %s: %w", page.PageID, err)
		}

		bodyPath, bodyHash, err := c.artifacts.writePageBody(ctx, buildID, page.PageID, content)
		if err != nil {
			return nil, fmt.Errorf("write artifact %s: %w", page.PageID, err)
		}

		sourceRefs := make([]model.SourceRef, len(page.Sources))
		for i, source := range page.Sources {
			sourceRefs[i] = model.SourceRef{
				SourceID:        source.SourceID,
				SourceVersionID: source.SourceVersionID,
				ChunkOrdinal:    source.ChunkOrdinal,
				ContentHash:     source.ContentHash,
				SourcePath:      source.DocumentTitle,
			}
		}
		sourceRefsJSON, err := json.Marshal(sourceRefs)
		if err != nil {
			return nil, fmt.Errorf("encode source refs for %s: %w", page.PageID, err)
		}

		buildPages = append(buildPages, &model.WikiBuildPage{
			BuildID:        buildID,
			TenantID:       tenantID,
			PageID:         page.PageID,
			PageType:       "generated",
			Title:          page.Title,
			BodyPath:       bodyPath,
			BodySHA256:     bodyHash,
			SourceRefsJSON: string(sourceRefsJSON),
		})
	}

	if len(selectedSourceIDs) > 0 {
		inherited, err := inheritUnselectedPages(ctx, c.repo, c.artifacts, tenantID, buildID, selectedSourceIDs, buildPages)
		if err != nil {
			return nil, fmt.Errorf("inherit unselected pages: %w", err)
		}
		buildPages = append(buildPages, inherited...)
	}

	if err := c.repo.CreatePagesBatch(ctx, buildPages); err != nil {
		return nil, fmt.Errorf("create pages: %w", err)
	}
	if err := c.repo.UpdateBuildPageCount(ctx, tenantID, buildID, len(buildPages)); err != nil {
		return nil, fmt.Errorf("update page count: %w", err)
	}

	return buildPages, nil
}

// pageInheritanceRepository is the persistence needed to carry unaffected pages
// from the active build into a new incremental build.
type pageInheritanceRepository interface {
	GetActiveBuild(ctx context.Context, tenantID uint64) (*model.WikiBuild, error)
	ListPagesByBuild(ctx context.Context, tenantID, buildID uint64) ([]*model.WikiBuildPage, error)
}

// inheritUnselectedPages copies active pages that do not cite any selected
// source into the new build. Pages whose evidence intersects the selected
// sources are allowed to be replaced by the incremental compile result.
func inheritUnselectedPages(
	ctx context.Context,
	repo pageInheritanceRepository,
	artifacts *artifactStore,
	tenantID, buildID uint64,
	selectedSourceIDs []uint64,
	generated []*model.WikiBuildPage,
) ([]*model.WikiBuildPage, error) {
	active, err := repo.GetActiveBuild(ctx, tenantID)
	if errors.Is(err, errs.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active build: %w", err)
	}

	oldPages, err := repo.ListPagesByBuild(ctx, tenantID, active.ID)
	if err != nil {
		return nil, fmt.Errorf("list active build pages: %w", err)
	}

	selected := make(map[uint64]struct{}, len(selectedSourceIDs))
	for _, sourceID := range selectedSourceIDs {
		selected[sourceID] = struct{}{}
	}
	generatedIDs := make(map[string]struct{}, len(generated))
	for _, page := range generated {
		if page != nil {
			generatedIDs[page.PageID] = struct{}{}
		}
	}

	inherited := make([]*model.WikiBuildPage, 0, len(oldPages))
	for _, page := range oldPages {
		if page == nil || page.PageID == "" {
			continue
		}
		if _, replaced := generatedIDs[page.PageID]; replaced {
			continue
		}

		dependsOnSelected, err := pageDependsOnSources(page.SourceRefsJSON, selected)
		if err != nil {
			return nil, fmt.Errorf("inspect page %s: %w", page.PageID, err)
		}
		if dependsOnSelected {
			continue
		}

		body, err := artifacts.readPageBody(ctx, active.ID, page.PageID)
		if err != nil {
			return nil, fmt.Errorf("read page %s from active build: %w", page.PageID, err)
		}
		if actualHash := contentSHA256([]byte(body)); actualHash != page.BodySHA256 {
			return nil, fmt.Errorf("page %s hash mismatch in active build", page.PageID)
		}

		bodyPath, bodyHash, err := artifacts.writePageBody(ctx, buildID, page.PageID, body)
		if err != nil {
			return nil, fmt.Errorf("copy page %s: %w", page.PageID, err)
		}

		cloned := *page
		cloned.ID = 0
		cloned.BuildID = buildID
		cloned.BodyPath = bodyPath
		cloned.BodySHA256 = bodyHash
		inherited = append(inherited, &cloned)
	}

	return inherited, nil
}

// pageDependsOnSources reports whether a serialized source reference list
// intersects the selected source set.
func pageDependsOnSources(raw string, selected map[uint64]struct{}) (bool, error) {
	if raw == "" {
		return false, nil
	}

	var refs []model.SourceRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return false, fmt.Errorf("decode source refs: %w", err)
	}
	for _, ref := range refs {
		if _, ok := selected[ref.SourceID]; ok {
			return true, nil
		}
	}
	return false, nil
}

// indexBuildPages feeds every written page into the search index.
func (c *buildCompiler) indexBuildPages(ctx context.Context, tenantID, buildID uint64, pages []*model.WikiBuildPage) error {
	if c.indexer == nil {
		return nil
	}
	if err := c.indexer.Clear(ctx, tenantID); err != nil {
		return fmt.Errorf("clear previous build index: %w", err)
	}
	for _, page := range pages {
		content, err := c.artifacts.readPageBody(ctx, buildID, page.PageID)
		if err != nil {
			return fmt.Errorf("read page %s for indexing: %w", page.PageID, err)
		}

		if err := c.indexer.IndexPage(ctx, newIndexDocument(page, content)); err != nil {
			return fmt.Errorf("index page %s: %w", page.PageID, err)
		}
	}

	return nil
}

// validateCitedSources rejects a build whose pages cite a source that no longer
// exists, so pages generated from a deleted source are never activated.
func (c *buildCompiler) validateCitedSources(ctx context.Context, tenantID, buildID uint64) error {
	pages, err := c.repo.ListPagesByBuild(ctx, tenantID, buildID)
	if err != nil {
		return fmt.Errorf("list build pages: %w", err)
	}

	checked := make(map[uint64]struct{})
	for _, page := range pages {
		if page == nil || page.SourceRefsJSON == "" {
			continue
		}
		var refs []model.SourceRef
		if err := json.Unmarshal([]byte(page.SourceRefsJSON), &refs); err != nil {
			return fmt.Errorf("decode source refs of page %s: %w", page.PageID, err)
		}
		for _, ref := range refs {
			if _, seen := checked[ref.SourceID]; seen {
				continue
			}
			checked[ref.SourceID] = struct{}{}
			if _, err := c.repo.GetSource(ctx, tenantID, ref.SourceID); err != nil {
				if errors.Is(err, errs.ErrNotFound) {
					return errors.Join(errs.ErrConflict, fmt.Errorf("source %d was deleted while the build was compiling", ref.SourceID))
				}
				return fmt.Errorf("get source %d: %w", ref.SourceID, err)
			}
		}
	}
	return nil
}

// failBuild marks a build as failed with an error message. The write runs on a
// detached context so a compile that ran out of time or was cancelled can still
// record why it stopped.
func (c *buildCompiler) failBuild(ctx context.Context, tenantID, buildID uint64, err error) {
	stateCtx, cancel := newStateWriteContext(ctx)
	defer cancel()
	if updateErr := c.repo.UpdateBuildStatus(stateCtx, tenantID, buildID, model.BuildFailed, err.Error()); updateErr != nil {
		c.log.ErrorContext(stateCtx, "build: failed to mark build as failed",
			slog.Uint64("build_id", buildID),
			slog.String("error", updateErr.Error()))
	}
}

// cleanupFailedBuild removes the artifacts and rows of a failed build. Like
// failBuild it stays writable after cancellation, otherwise a timed-out build
// would leave its staging directory and rows behind forever.
func (c *buildCompiler) cleanupFailedBuild(ctx context.Context, tenantID, buildID uint64) error {
	stateCtx, cancel := newStateWriteContext(ctx)
	defer cancel()
	if err := c.artifacts.deleteBuild(stateCtx, buildID); err != nil {
		return err
	}
	return c.repo.DeleteBuild(stateCtx, tenantID, buildID)
}
