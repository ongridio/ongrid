package llm_wiki

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeWikiRepo overrides only the calls a test exercises. The embedded
// Repository is nil, so reaching an unexpected dependency panics instead of
// silently answering with zero values.
type fakeWikiRepo struct {
	Repository

	job             *model.CompileJob
	updates         []jobUpdate
	hasActive       bool
	cancelRequested bool
	createBuildErr  error
	build           *model.WikiBuild
	activeBuild     *model.WikiBuild
	pages           []*model.WikiBuildPage
	sources         []*model.Source
	version         *model.SourceVersion
	versions        map[uint64]*model.SourceVersion
	failedSources   []uint64
	sourceErr       error
	deletedSources  []uint64
	deletedBuild    uint64
	createBuildID   uint64
	buildStatus     string
	buildStatusMsg  string
	activated       bool
	nextVersionID   uint64
}

type jobUpdate struct {
	status  string
	stage   string
	message string
}

func (r *fakeWikiRepo) GetJob(context.Context, uint64, uint64) (*model.CompileJob, error) {
	if r.job == nil {
		return nil, errs.ErrNotFound
	}
	return r.job, nil
}

func (r *fakeWikiRepo) UpdateJob(_ context.Context, _, _ uint64, status, stage, message string) error {
	r.updates = append(r.updates, jobUpdate{status: status, stage: stage, message: message})
	if r.job != nil {
		r.job.Status = status
	}
	return nil
}

func (r *fakeWikiRepo) HasActiveJob(context.Context, uint64) (bool, error) {
	return r.hasActive, nil
}

func (r *fakeWikiRepo) IsCancelRequested(context.Context, uint64, uint64) (bool, error) {
	return r.cancelRequested, nil
}

func (r *fakeWikiRepo) CreateBuild(context.Context, uint64) (*model.WikiBuild, error) {
	if r.createBuildErr != nil {
		return nil, r.createBuildErr
	}
	buildID := r.createBuildID
	if buildID == 0 {
		buildID = 1
	}
	return &model.WikiBuild{ID: buildID}, nil
}

func (r *fakeWikiRepo) UpdateBuildStatus(_ context.Context, _ uint64, _ uint64, status, message string) error {
	r.buildStatus = status
	r.buildStatusMsg = message
	return nil
}

func (r *fakeWikiRepo) GetBuild(context.Context, uint64, uint64) (*model.WikiBuild, error) {
	return r.build, nil
}

func (r *fakeWikiRepo) GetActiveBuild(context.Context, uint64) (*model.WikiBuild, error) {
	if r.activeBuild != nil {
		return r.activeBuild, nil
	}
	return nil, errs.ErrNotFound
}

func (r *fakeWikiRepo) ActivateBuild(context.Context, uint64, uint64) error {
	r.activated = true
	return nil
}

func (r *fakeWikiRepo) DeleteBuild(_ context.Context, _ uint64, buildID uint64) error {
	r.deletedBuild = buildID
	return nil
}

func (r *fakeWikiRepo) ListPagesByBuild(context.Context, uint64, uint64) ([]*model.WikiBuildPage, error) {
	return r.pages, nil
}

func (r *fakeWikiRepo) GetSource(context.Context, uint64, uint64) (*model.Source, error) {
	if r.sourceErr != nil {
		return nil, r.sourceErr
	}
	return &model.Source{ID: 1}, nil
}

func (r *fakeWikiRepo) DeleteSource(_ context.Context, _ uint64, id uint64) error {
	r.deletedSources = append(r.deletedSources, id)
	return nil
}

func (r *fakeWikiRepo) GetVersion(_ context.Context, _ uint64, id uint64) (*model.SourceVersion, error) {
	if r.versions != nil {
		if version, ok := r.versions[id]; ok {
			return version, nil
		}
		return nil, errs.ErrNotFound
	}
	if r.version == nil {
		return nil, errs.ErrNotFound
	}
	return r.version, nil
}

func (r *fakeWikiRepo) MarkSourceStatus(_ context.Context, _, id uint64, status string) error {
	if status == model.SourceFailed {
		r.failedSources = append(r.failedSources, id)
	}
	return nil
}

func (r *fakeWikiRepo) UpsertSourceVersion(_ context.Context, source *model.Source, version *model.SourceVersion) (*model.Source, *model.SourceVersion, bool, error) {
	for index, existing := range r.sources {
		if existing.TenantID != source.TenantID || existing.SourceKey != source.SourceKey {
			continue
		}
		changed := existing.ContentSHA256 != source.ContentSHA256
		stored := *source
		stored.ID = existing.ID
		if changed || existing.CurrentVersionID == nil {
			r.nextVersionID++
			version.ID = r.nextVersionID
			stored.CurrentVersionID = &version.ID
		} else {
			stored.CurrentVersionID = existing.CurrentVersionID
		}
		r.sources[index] = &stored
		return &stored, version, changed, nil
	}

	stored := *source
	stored.ID = uint64(len(r.sources) + 1)
	r.nextVersionID++
	version.ID = r.nextVersionID
	stored.CurrentVersionID = &version.ID
	r.sources = append(r.sources, &stored)
	return &stored, version, true, nil
}

// ListSources mimics the store: it answers with one capped page, so a test that
// needs every source has to go through ListSourcesAfter like the usecase does.
func (r *fakeWikiRepo) ListSources(context.Context, uint64, string, int) ([]*model.Source, int64, error) {
	limit := 200
	if limit > len(r.sources) {
		limit = len(r.sources)
	}
	return r.sources[:limit], int64(len(r.sources)), nil
}

func (r *fakeWikiRepo) ListSourcesAfter(_ context.Context, _ uint64, _ string, afterID uint64, limit int) ([]*model.Source, error) {
	page := make([]*model.Source, 0, limit)
	for _, source := range r.sources {
		if source.ID <= afterID {
			continue
		}
		page = append(page, source)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

func newTestUsecase(t *testing.T, repo Repository) (*Usecase, *FileStore) {
	t.Helper()
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	uc, err := newUsecase(context.Background(), repo, files, mustNotCallLLM(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)
	return uc, files
}

// seedMirroredSource registers one source whose version snapshot exists on disk.
// A size below the corpus minimum makes the document drop out of chunking, which
// is how a corpus ends up empty.
func seedMirroredSource(t *testing.T, repo *fakeWikiRepo, files *FileStore, sizeBytes int, content string) {
	t.Helper()
	snapshot := "versions/" + shortHash(content) + ".md"
	absolute, err := files.resolve("", snapshot)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(absolute), 0o750))
	require.NoError(t, os.WriteFile(absolute, []byte(content), 0o640))

	versionID := uint64(11)
	repo.version = &model.SourceVersion{ID: versionID, TenantID: DefaultTenantID, SHA256: contentSHA256([]byte(content)), SizeBytes: uint64(sizeBytes), SnapshotPath: snapshot}
	repo.sources = []*model.Source{{
		ID:               7,
		TenantID:         DefaultTenantID,
		SourceKey:        "organization:7",
		SourceType:       "organization",
		RawPath:          "docs/runbook.md",
		ContentSHA256:    contentSHA256([]byte(content)),
		Status:           model.SourcePending,
		CurrentVersionID: &versionID,
	}}
}

// mustNotCallLLM fails a test loudly if a stage reaches the model.
func mustNotCallLLM() CompilerLLM {
	return compilerLLMFunc{
		version: "test",
		call: func(context.Context, llm.ChatReq) (*llm.ChatResp, error) {
			return nil, errors.New("llm must not be called")
		},
	}
}

// TestRunJob_MarksJobFailedAndRecordsStage — a job that fails must reach the
// failed terminal state with the step it stopped at, instead of staying running
// forever and blocking every later compile.
func TestRunJob_MarksJobFailedAndRecordsStage(t *testing.T) {
	repo := &fakeWikiRepo{
		job:            &model.CompileJob{ID: 5, TenantID: DefaultTenantID, Status: model.JobRunning, Stage: "queued"},
		createBuildErr: errors.New("mysql is down"),
	}
	uc, _ := newTestUsecase(t, repo)

	err := uc.runJob(context.Background(), repo.job)

	require.Error(t, err)
	require.Len(t, repo.updates, 1)
	assert.Equal(t, model.JobFailed, repo.updates[0].status)
	assert.Equal(t, "create build", repo.updates[0].stage)
	assert.Contains(t, repo.updates[0].message, "mysql is down")
}

// TestRunJob_KeepsCancelledJobTerminal — CancelJob persists `cancelled` as soon
// as the request arrives, so the pipeline failing afterwards must not rewrite it.
func TestRunJob_KeepsCancelledJobTerminal(t *testing.T) {
	repo := &fakeWikiRepo{
		job:            &model.CompileJob{ID: 6, TenantID: DefaultTenantID, Status: model.JobCancelled},
		createBuildErr: errors.New("mysql is down"),
	}
	uc, _ := newTestUsecase(t, repo)

	require.Error(t, uc.runJob(context.Background(), repo.job))

	assert.Empty(t, repo.updates)
	assert.Equal(t, model.JobCancelled, repo.job.Status)
}

// TestDeleteNode_RefusesWhileCompileIsRunning — the compiler reads the source
// set once, so a delete that lands mid-build is refused rather than raced.
func TestDeleteNode_RefusesWhileCompileIsRunning(t *testing.T) {
	repo := &fakeWikiRepo{hasActive: true}
	uc, _ := newTestUsecase(t, repo)

	err := uc.DeleteNode(context.Background(), encodeNodeID("raw", "docs/runbook.md"))

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrConflict)
	assert.Empty(t, repo.deletedSources)
}

func TestSyncOrganizationSources_RefusesWhileCompileIsRunning(t *testing.T) {
	repo := &fakeWikiRepo{hasActive: true}
	uc, _ := newTestUsecase(t, repo)

	result, err := uc.SyncOrganizationSources(context.Background(), []OrganizationSource{{ID: 1, Title: "Runbook", Path: "docs", Content: "body"}})

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrConflict)
	assert.Nil(t, result)
}

// TestSyncOrganizationSources_RepositoryDocumentKeepsOrganizationIdentity —
// repository documents enter this usecase through the organization projection.
// Their Qdrant document ID remains the stable Wiki identity, while the raw path
// follows the same path/title layout as manual and uploaded documents.
func TestSyncOrganizationSources_RepositoryDocumentKeepsOrganizationIdentity(t *testing.T) {
	repo := &fakeWikiRepo{}
	uc, files := newTestUsecase(t, repo)
	doc := OrganizationSource{ID: 99, Title: "main", Path: "cmd", Content: "package main"}

	created, err := uc.SyncOrganizationSources(context.Background(), []OrganizationSource{doc})
	require.NoError(t, err)
	assert.Equal(t, 1, created.Created)
	require.Len(t, repo.sources, 1)
	assert.Equal(t, "organization:99", repo.sources[0].SourceKey)
	assert.Equal(t, "organization", repo.sources[0].SourceType)
	assert.Equal(t, "cmd/main.md", repo.sources[0].RawPath)
	rawPath, err := files.resolve("raw", repo.sources[0].RawPath)
	require.NoError(t, err)
	body, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, "package main", string(body))

	unchanged, err := uc.SyncOrganizationSources(context.Background(), []OrganizationSource{doc})
	require.NoError(t, err)
	assert.Equal(t, 1, unchanged.Unchanged)

	doc.Content = "package main\n\nfunc main() {}"
	updated, err := uc.SyncOrganizationSources(context.Background(), []OrganizationSource{doc})
	require.NoError(t, err)
	assert.Equal(t, 1, updated.Updated)

	preserved, err := uc.SyncOrganizationSources(context.Background(), nil)
	require.NoError(t, err)
	assert.Zero(t, preserved.Deleted)
	assert.Empty(t, repo.deletedSources)
}

// TestPublish_RefusesBuildWhoseSourceWasDeleted — the source-set check races job
// creation, so the citation check at the activation boundary is what keeps a
// deleted source from being republished.
func TestPublish_RefusesBuildWhoseSourceWasDeleted(t *testing.T) {
	repo := &fakeWikiRepo{
		build: &model.WikiBuild{ID: 9, TenantID: DefaultTenantID, Status: model.BuildValidated},
		pages: []*model.WikiBuildPage{{
			BuildID:        9,
			TenantID:       DefaultTenantID,
			PageID:         "dns-overview",
			SourceRefsJSON: `[{"source_id":42,"source_version_id":1,"chunk_ordinal":0,"content_hash":"a"}]`,
		}},
		sourceErr: errs.ErrNotFound,
	}
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, files.Ensure(context.Background()))
	compiler := newBuildCompiler(repo, nil, nil, files, mustNotCallLLM(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	err = compiler.Publish(context.Background(), DefaultTenantID, 9)

	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrConflict)
	assert.False(t, repo.activated)
	assert.Equal(t, uint64(9), repo.deletedBuild)
}

// TestSyncOrganizationSources_PreservesSourcesMissingFromSnapshot — knowledge
// repository sync clears and repopulates Qdrant, so a snapshot can temporarily
// omit every document. Missing entries must not delete Raw sources or the active
// Wiki build, including sources beyond the first metadata page.
func TestSyncOrganizationSources_PreservesSourcesMissingFromSnapshot(t *testing.T) {
	const total = 205
	repo := &fakeWikiRepo{activeBuild: &model.WikiBuild{ID: 77, TenantID: DefaultTenantID, Status: model.BuildActive}}
	for i := 1; i <= total; i++ {
		repo.sources = append(repo.sources, &model.Source{
			ID:         uint64(i),
			TenantID:   DefaultTenantID,
			SourceKey:  fmt.Sprintf("organization:%d", i),
			SourceType: "organization",
			RawPath:    fmt.Sprintf("docs/%03d.md", i),
			Status:     model.SourcePending,
		})
	}
	uc, files := newTestUsecase(t, repo)
	rawPath, err := files.resolve("raw", repo.sources[0].RawPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(rawPath), 0o750))
	require.NoError(t, os.WriteFile(rawPath, []byte("existing raw"), 0o640))
	artifacts := newArtifactStore(files)
	wikiPath, _, err := artifacts.writePageBody(context.Background(), repo.activeBuild.ID, "existing-wiki", "existing wiki")
	require.NoError(t, err)
	wikiAbsolute, err := files.resolve("wiki", wikiPath)
	require.NoError(t, err)

	// The knowledge-base snapshot is temporarily empty during re-indexing.
	result, err := uc.SyncOrganizationSources(context.Background(), nil)

	require.NoError(t, err)
	assert.Zero(t, result.Deleted)
	assert.Empty(t, repo.deletedSources)
	assert.Zero(t, repo.deletedBuild, "sync must not invalidate the published Wiki")
	assert.FileExists(t, rawPath)
	assert.FileExists(t, wikiAbsolute)
}

// TestReconcile_SkipsSourcesWhoseVersionRowIsGone — reconcile used to return the
// missing-version error, which failed Wiki construction and left every route
// unregistered, so the SPA answered 404. One broken source must not stop the
// Wiki from starting, or nobody can re-sync the source that broke it.
func TestReconcile_SkipsSourcesWhoseVersionRowIsGone(t *testing.T) {
	repo := &fakeWikiRepo{}
	uc, files := newTestUsecase(t, repo)

	// The healthy source's snapshot exists on disk, its raw file does not.
	snapshot := "versions/" + shortHash("runbook body") + ".md"
	absolute, err := files.resolve("", snapshot)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(absolute), 0o750))
	require.NoError(t, os.WriteFile(absolute, []byte("runbook body"), 0o640))

	orphanVersionID := uint64(99)
	healthyVersionID := uint64(21)
	repo.sources = []*model.Source{
		{ID: 1, TenantID: DefaultTenantID, SourceKey: "organization:1", RawPath: "docs/orphan.md", CurrentVersionID: &orphanVersionID},
		{ID: 2, TenantID: DefaultTenantID, SourceKey: "organization:2", RawPath: "docs/runbook.md", CurrentVersionID: &healthyVersionID},
	}
	repo.versions = map[uint64]*model.SourceVersion{
		healthyVersionID: {ID: healthyVersionID, TenantID: DefaultTenantID, SnapshotPath: snapshot, SizeBytes: 12},
	}

	require.NoError(t, uc.Reconcile(context.Background(), DefaultTenantID))

	assert.Equal(t, []uint64{1}, repo.failedSources, "the source with no version row is marked failed")
	rawPath, err := files.resolve("raw", "docs/runbook.md")
	require.NoError(t, err)
	body, err := os.ReadFile(rawPath)
	require.NoError(t, err)
	assert.Equal(t, "runbook body", string(body), "a source after the broken one is still restored")
}

// TestCompile_SkipsWhenTheCorpusHasNoChunks — a corpus that chunks to nothing has
// no evidence to plan from. Asking the planner anyway would either invent pages
// without sources or fail on an empty plan, and publishing would wipe the Wiki.
func TestCompile_SkipsWhenTheCorpusHasNoChunks(t *testing.T) {
	repo := &fakeWikiRepo{createBuildID: 4}
	uc, files := newTestUsecase(t, repo)
	// Below the corpus minimum, so the document never reaches the chunker.
	seedMirroredSource(t, repo, files, 20, "tiny")
	job := &model.CompileJob{ID: 9, TenantID: DefaultTenantID, Status: model.JobRunning}

	require.NoError(t, uc.runJob(context.Background(), job))

	require.Len(t, repo.updates, 1)
	assert.Equal(t, model.JobSkipped, repo.updates[0].status)
	assert.False(t, repo.activated, "an empty corpus must not publish a build")
	assert.Equal(t, uint64(4), repo.deletedBuild, "the empty staging build must be dropped")
}

// TestCompile_CleansUpBuildAfterContextCancellation — cleanup has to survive the
// context that cancelled the compile, otherwise a timed-out build leaves its
// staging directory and rows behind forever.
func TestCompile_CleansUpBuildAfterContextCancellation(t *testing.T) {
	repo := &fakeWikiRepo{createBuildID: 5}
	_, files := newTestUsecase(t, repo)
	seedMirroredSource(t, repo, files, 400, strings.Repeat("content ", 50))
	compiler := newBuildCompiler(repo, nil, nil, files, mustNotCallLLM(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := compiler.Compile(ctx, DefaultTenantID)

	require.False(t, result.Success)
	assert.Equal(t, uint64(5), repo.deletedBuild)
}
