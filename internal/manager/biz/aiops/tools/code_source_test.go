package tools

import (
	"context"
	"encoding/json"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"log/slog"
	"strings"
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

func TestCodeToolsRequireExplicitRevision(t *testing.T) {
	for _, tool := range []basetool.BaseTool{NewListRepoSourcesTool(codeKnowledge{}, nil), NewReadSourceTool(codeKnowledge{}, nil), NewGrepSourceTool(codeKnowledge{}, nil)} {
		info, _ := tool.Info(context.Background())
		var schema struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(info.Parameters, &schema); err != nil {
			t.Fatal(err)
		}
		if !containsName(schema.Required, "revision") {
			t.Fatalf("%s schema permits omitted revision", info.Name)
		}
		for _, revision := range []string{"", " ", "HEAD", "refs/tags/v1", strings.Repeat("a", 40)} {
			args, _ := json.Marshal(map[string]string{"repo": "1", "path": "main.go", "pattern": "error", "revision": revision})
			_, err := tool.InvokableRun(context.Background(), string(args))
			if (err != nil) != (strings.TrimSpace(revision) == "") {
				t.Fatalf("%s revision %q: %v", info.Name, revision, err)
			}
		}
	}
}

type numberedCode struct{ codeKnowledge }

func (numberedCode) ReadSource(context.Context, string, string, int, int, string) (*knowledgebiz.SourceFile, error) {
	return &knowledgebiz.SourceFile{StartLine: 18, EndLine: 20, Content: "if coupon != \"\" {\n\ttotal -= discount\n", CommitSHA: "abc"}, nil
}
func TestReadSourceNumbersWindowIncludingBlankLines(t *testing.T) {
	out, err := NewReadSourceTool(numberedCode{}, nil).InvokableRun(context.Background(), `{"repo":"1","path":"main.go","revision":"refs/tags/v1"}`)
	if err != nil {
		t.Fatal(err)
	}
	var file knowledgebiz.SourceFile
	if err := json.Unmarshal([]byte(out), &file); err != nil {
		t.Fatal(err)
	}
	if file.Content != "18: if coupon != \"\" {\n19: \ttotal -= discount\n20: \n" || file.CommitSHA != "abc" {
		t.Fatalf("numbered source: %+v", file)
	}
}

func TestAPMSourceScopeRejectsVersionAndRepositoryEscapes(t *testing.T) {
	for _, tool := range []basetool.BaseTool{NewListRepoSourcesTool(codeKnowledge{}, nil), NewReadSourceTool(codeKnowledge{}, nil), NewGrepSourceTool(codeKnowledge{}, nil)} {
		for _, tc := range []struct {
			repo, revision, path, blocked string
			wantErr                       bool
		}{
			{"1", "commit", "src/main.go", "", false}, {"1", "refs/tags/v1", "src/main.go", "", false},
			{"2", "commit", "src/main.go", "", true}, {"1", "HEAD", "src/main.go", "", true},
			{"1", "other", "src/main.go", "", true}, {"1", "commit", "src/../secret", "", true},
			{"1", "commit", "other/main.go", "", true}, {"1", "commit", ":(top)*", "", true},
			{"1", "commit", "src/main.go", "missing version", true},
		} {
			ctx := basetool.WithAPMSource(context.Background(), &model.APMSourceScope{RepoID: "1", CommitSHA: "commit", Revision: "refs/tags/v1", SourceDirectory: "src", Error: tc.blocked})
			args, _ := json.Marshal(map[string]string{"repo": tc.repo, "revision": tc.revision, "path": tc.path, "subpath": tc.path, "path_glob": tc.path, "pattern": "err"})
			if _, err := tool.InvokableRun(ctx, string(args)); (err != nil) != tc.wantErr {
				t.Fatalf("%+v: %v", tc, err)
			}
		}
	}
}
