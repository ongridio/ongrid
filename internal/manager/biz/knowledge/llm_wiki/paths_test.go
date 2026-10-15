package llm_wiki

import (
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge/llm_wiki"
)

func TestSafeOrganizationPathPreservesFolders(t *testing.T) {
	got := safeOrganizationPath("产品/运维手册/故障排查.pdf")
	if got != "产品/运维手册/故障排查.md" {
		t.Fatalf("safeOrganizationPath() = %q", got)
	}
}

func TestSafeOrganizationPathDropsTraversal(t *testing.T) {
	got := safeOrganizationPath("../平台/../../密钥")
	if got != "平台/密钥.md" {
		t.Fatalf("safeOrganizationPath() = %q", got)
	}
}

func TestWikiPageRelativePathUsesPrimarySourceDirectory(t *testing.T) {
	got := wikiPageRelativePath("dns-diagnosis", []model.SourceRef{
		{SourceID: 2, SourcePath: "network/dns/troubleshooting.md"},
		{SourceID: 1, SourcePath: "network/dns/overview.md"},
	})
	if got != "network/dns/dns-diagnosis.md" {
		t.Fatalf("wikiPageRelativePath() = %q", got)
	}
}

func TestWikiPageRelativePathFallsBackForLegacyPages(t *testing.T) {
	got := wikiPageRelativePath("legacy-page", []model.SourceRef{{SourceID: 1}})
	if got != "legacy-page.md" {
		t.Fatalf("wikiPageRelativePath() = %q", got)
	}
}
