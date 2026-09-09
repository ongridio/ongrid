package tools

import (
	"context"
	"log/slog"
	"testing"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	knowledgebiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
)

// codeKnowledge satisfies BOTH KnowledgeSearcher and CodeBrowser — i.e. the
// real *knowledge.Usecase shape. The code tools register only when the wired
// knowledge service type-asserts to CodeBrowser.
type codeKnowledge struct{}

func (codeKnowledge) Search(context.Context, string, knowledgebiz.SearchOptions) ([]knowledgebiz.SearchHit, error) {
	return nil, nil
}
func (codeKnowledge) ListRepoSources(context.Context, string, string, string) (*knowledgebiz.RepoSourceListing, error) {
	return nil, nil
}
func (codeKnowledge) ReadSource(context.Context, string, string, int, int, string) (*knowledgebiz.SourceFile, error) {
	return nil, nil
}
func (codeKnowledge) GrepSource(context.Context, string, string, string, int, string) (*knowledgebiz.GrepResult, error) {
	return nil, nil
}

// searchOnlyKnowledge satisfies KnowledgeSearcher but NOT CodeBrowser.
type searchOnlyKnowledge struct{}

func (searchOnlyKnowledge) Search(context.Context, string, knowledgebiz.SearchOptions) ([]knowledgebiz.SearchHit, error) {
	return nil, nil
}

// TestBuildBaseTools_CodeToolsGatedOnCodeBrowser — HLD-012. When the knowledge
// service implements CodeBrowser, the three read-code tools register; when it's
// search-only, they don't (no half-wired state). Guards the "registered in the
// bag" half of the regression (the coordinator-roster half is in cmd/ongrid).
func TestBuildBaseTools_CodeToolsGatedOnCodeBrowser(t *testing.T) {
	codeTools := []string{"list_repo_sources", "read_source", "grep_source"}

	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())

	reg := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	reg.SetKnowledgeSearcher(codeKnowledge{})
	names := toolInfoNames(t, reg.BuildBaseTools().AllTools())
	for _, n := range append([]string{"query_knowledge"}, codeTools...) {
		if !containsName(names, n) {
			t.Errorf("with CodeBrowser knowledge, bag missing %q (have %v)", n, names)
		}
	}

	regSearchOnly := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	regSearchOnly.SetKnowledgeSearcher(searchOnlyKnowledge{})
	namesSO := toolInfoNames(t, regSearchOnly.BuildBaseTools().AllTools())
	if !containsName(namesSO, "query_knowledge") {
		t.Errorf("search-only knowledge should still register query_knowledge")
	}
	for _, n := range codeTools {
		if containsName(namesSO, n) {
			t.Errorf("search-only knowledge must NOT register code tool %q", n)
		}
	}
}
