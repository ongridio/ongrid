package llm_wiki

import (
	"context"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePageInheritanceRepo struct {
	active *model.WikiBuild
	pages  []*model.WikiBuildPage
}

func (r *fakePageInheritanceRepo) GetActiveBuild(context.Context, uint64) (*model.WikiBuild, error) {
	if r.active == nil {
		return nil, errs.ErrNotFound
	}
	return r.active, nil
}

func (r *fakePageInheritanceRepo) ListPagesByBuild(context.Context, uint64, uint64) ([]*model.WikiBuildPage, error) {
	return r.pages, nil
}

func TestInheritUnselectedPages_PreservesUnaffectedHistoricalPages(t *testing.T) {
	ctx := context.Background()
	files, err := NewFileStore(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, files.Ensure(ctx))
	artifacts := newArtifactStore(files)

	const (
		tenantID   = uint64(3)
		activeID   = uint64(7)
		newBuildID = uint64(8)
	)

	oldBodies := map[string]string{
		"selected-page":   "# Selected\n\nold content\n",
		"unaffected-page": "# Unaffected\n\nhistorical content\n",
		"legacy-page":     "# Legacy\n\nuntracked content\n",
	}
	oldPages := make([]*model.WikiBuildPage, 0, len(oldBodies))
	for pageID, body := range oldBodies {
		_, bodyHash, writeErr := artifacts.writePageBody(ctx, activeID, pageID, body)
		require.NoError(t, writeErr)
		oldPages = append(oldPages, &model.WikiBuildPage{
			BuildID:    activeID,
			TenantID:   tenantID,
			PageID:     pageID,
			PageType:   "generated",
			Title:      pageID,
			BodySHA256: bodyHash,
		})
	}
	for _, page := range oldPages {
		switch page.PageID {
		case "selected-page":
			page.SourceRefsJSON = `[{"source_id":10,"source_version_id":100,"chunk_ordinal":0,"content_hash":"a"}]`
		case "unaffected-page":
			page.SourceRefsJSON = `[{"source_id":20,"source_version_id":200,"chunk_ordinal":0,"content_hash":"b"}]`
		default:
			page.SourceRefsJSON = `[]`
		}
	}

	repo := &fakePageInheritanceRepo{
		active: &model.WikiBuild{ID: activeID, TenantID: tenantID, Status: model.BuildActive},
		pages:  oldPages,
	}
	generated := []*model.WikiBuildPage{{BuildID: newBuildID, TenantID: tenantID, PageID: "fresh-page"}}

	inherited, err := inheritUnselectedPages(ctx, repo, artifacts, tenantID, newBuildID, []uint64{10}, generated)
	require.NoError(t, err)
	require.Len(t, inherited, 2)

	byID := make(map[string]*model.WikiBuildPage, len(inherited))
	for _, page := range inherited {
		byID[page.PageID] = page
		assert.Equal(t, newBuildID, page.BuildID)
		body, readErr := artifacts.readPageBody(ctx, newBuildID, page.PageID)
		require.NoError(t, readErr)
		assert.Equal(t, oldBodies[page.PageID], body)
	}
	assert.NotContains(t, byID, "selected-page")
	assert.Contains(t, byID, "unaffected-page")
	assert.Contains(t, byID, "legacy-page")
}

func TestPageDependsOnSources(t *testing.T) {
	selected := map[uint64]struct{}{10: {}}
	tests := []struct {
		name    string
		raw     string
		want    bool
		wantErr bool
	}{
		{name: "empty refs", raw: "", want: false},
		{name: "unselected source", raw: `[{"source_id":20}]`, want: false},
		{name: "selected source", raw: `[{"source_id":10}]`, want: true},
		{name: "mixed sources", raw: `[{"source_id":20},{"source_id":10}]`, want: true},
		{name: "invalid json", raw: `{`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pageDependsOnSources(tt.raw, selected)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
