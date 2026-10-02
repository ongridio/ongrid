package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/knowledge/llm_wiki"
	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
)

func TestTreeReadsPublishedBuildPages(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	files, err := biz.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	build, err := repo.CreateBuild(ctx, biz.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.ToSlash(filepath.Join("builds", fmt.Sprintf("%d", build.ID), "pages", "generated.md"))
	absPath := filepath.Join(root, "wiki", filepath.FromSlash(bodyPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatal(err)
	}
	generatedBody := "# Generated Wiki\n"
	if err := os.WriteFile(absPath, []byte(generatedBody), 0o640); err != nil {
		t.Fatal(err)
	}
	page := &model.WikiBuildPage{BuildID: build.ID, TenantID: biz.DefaultTenantID, PageID: "generated", PageType: "generated", Title: "Generated Wiki", BodyPath: bodyPath, BodySHA256: "hash", SourceRefsJSON: "[]"}
	if err := repo.CreatePage(ctx, page); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateBuildStatus(ctx, biz.DefaultTenantID, build.ID, model.BuildValidated, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActivateBuild(ctx, biz.DefaultTenantID, build.ID); err != nil {
		t.Fatal(err)
	}
	uc, err := biz.NewWithUsageRecorder(ctx, repo, files, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := uc.ListTree(ctx, "wiki", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.PageID != "generated" {
			continue
		}
		if node.RelativePath != "generated.md" || node.Name != "Generated Wiki" {
			t.Fatalf("node = %+v", node)
		}
		detail, err := uc.GetNode(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Content != generatedBody {
			t.Fatalf("content = %q", detail.Content)
		}
		return
	}
	t.Fatalf("generated page missing from tree: %+v", nodes)
}

func TestTreePlacesGeneratedPageUnderPrimarySourceDirectory(t *testing.T) {
	repo, _ := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	files, err := biz.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	build, err := repo.CreateBuild(ctx, biz.DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	bodyPath := filepath.ToSlash(filepath.Join("builds", fmt.Sprintf("%d", build.ID), "pages", "dns-diagnosis.md"))
	absPath := filepath.Join(root, "wiki", filepath.FromSlash(bodyPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absPath, []byte("# DNS diagnosis\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	page := &model.WikiBuildPage{
		BuildID:        build.ID,
		TenantID:       biz.DefaultTenantID,
		PageID:         "dns-diagnosis",
		PageType:       "generated",
		Title:          "DNS diagnosis",
		BodyPath:       bodyPath,
		BodySHA256:     "hash",
		SourceRefsJSON: `[{"source_id":7,"source_version_id":8,"chunk_ordinal":0,"content_hash":"source-hash","source_path":"network/dns/troubleshooting.md"}]`,
	}
	if err := repo.CreatePage(ctx, page); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateBuildStatus(ctx, biz.DefaultTenantID, build.ID, model.BuildValidated, ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.ActivateBuild(ctx, biz.DefaultTenantID, build.ID); err != nil {
		t.Fatal(err)
	}
	uc, err := biz.NewWithUsageRecorder(ctx, repo, files, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	rootNodes, err := uc.ListTree(ctx, "wiki", "")
	if err != nil {
		t.Fatal(err)
	}
	var network *biz.TreeNode
	for i := range rootNodes {
		if rootNodes[i].Kind == "folder" && rootNodes[i].RelativePath == "network" {
			network = &rootNodes[i]
			break
		}
	}
	if network == nil {
		t.Fatalf("network folder missing from tree: %+v", rootNodes)
	}

	networkNodes, err := uc.ListTree(ctx, "wiki", network.ID)
	if err != nil {
		t.Fatal(err)
	}
	var dns *biz.TreeNode
	for i := range networkNodes {
		if networkNodes[i].Kind == "folder" && networkNodes[i].RelativePath == "network/dns" {
			dns = &networkNodes[i]
			break
		}
	}
	if dns == nil {
		t.Fatalf("dns folder missing from tree: %+v", networkNodes)
	}

	dnsNodes, err := uc.ListTree(ctx, "wiki", dns.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range dnsNodes {
		if node.PageID == "dns-diagnosis" && node.RelativePath == "network/dns/dns-diagnosis.md" {
			return
		}
	}
	t.Fatalf("generated page missing from source directory: %+v", dnsNodes)
}
