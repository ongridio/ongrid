package knowledge

import (
	"context"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

func TestEmbedScannedFiles_UsesOnePipelineForRepoAndVault(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		sourceType string
		repoID     *uint64
		wantRepoID bool
	}{
		{name: "repo", sourceType: model.SourceRepo, repoID: ptrU64(7), wantRepoID: true},
		{name: "vault", sourceType: model.SourceVault, wantRepoID: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			vec := &fakeVec{}
			u := &Usecase{embed: fakeEmbed{}, vec: vec}
			files := []scannedFile{{URL: "docs/guide.md", Title: "Guide", Content: "body"}}

			if err := u.embedScannedFiles(context.Background(), files, testCase.sourceType, testCase.repoID, testTime()); err != nil {
				t.Fatalf("embed scanned files: %v", err)
			}
			if len(vec.upserts) != 1 || len(vec.upserts[0]) != 1 {
				t.Fatalf("upserts = %d batches/%d points, want one batch with one point", len(vec.upserts), len(vec.upserts[0]))
			}
			payload := vec.upserts[0][0].Payload
			if payload["source_type"] != testCase.sourceType {
				t.Fatalf("source_type = %v, want %q", payload["source_type"], testCase.sourceType)
			}
			_, hasRepoID := payload["repo_id"]
			if hasRepoID != testCase.wantRepoID {
				t.Fatalf("repo_id present = %v, want %v", hasRepoID, testCase.wantRepoID)
			}
		})
	}
}

func TestEmbedScannedFiles_RejectsMismatchedVectorCount(t *testing.T) {
	u := &Usecase{embed: shortEmbed{}, vec: &fakeVec{}}
	files := []scannedFile{{URL: "guide.md", Title: "Guide", Content: "body"}}

	if err := u.embedScannedFiles(context.Background(), files, model.SourceVault, nil, testTime()); err == nil {
		t.Fatal("accepted an embedding response with fewer vectors than inputs")
	}
}

type shortEmbed struct{}

func (shortEmbed) Dim() int { return 4 }

func (shortEmbed) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}

func testTime() time.Time {
	return time.Unix(0, 0).UTC()
}
