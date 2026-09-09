package knowledge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestSourceRevisionPinsTagAcrossAllTools(t *testing.T) {
	u, _ := newCodeBrowseUC(t)
	dir, ctx := u.repoDir(1), context.Background()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	old := git("rev-parse", "HEAD")
	git("tag", "v1.0.0")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// new version only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=test", "-c", "user.email=t@t", "commit", "-qm", "new version")
	head := git("rev-parse", "HEAD")
	file, err := u.ReadSource(ctx, "1", "main.go", 1, 5, "refs/tags/v1.0.0")
	if err != nil || file.CommitSHA != old || !strings.Contains(file.Content, "ResolveEdgeID") {
		t.Fatalf("tag read: %+v %v", file, err)
	}
	grep, err := u.GrepSource(ctx, "1", "ResolveEdgeID", "main.go", 10, file.CommitSHA)
	if err != nil || len(grep.Hits) != 1 || grep.CommitSHA != old {
		t.Fatalf("pinned grep: %+v %v", grep, err)
	}
	listing, err := u.ListRepoSources(ctx, "1", "", "v1.0.0")
	if err != nil || listing.CommitSHA != old || len(listing.Entries) == 0 {
		t.Fatalf("pinned listing: %+v %v", listing, err)
	}
	if file, err = u.ReadSource(ctx, "1", "main.go", 0, 0, ""); err != nil || file.CommitSHA != head || !strings.Contains(file.Content, "new version only") {
		t.Fatalf("default HEAD: %+v %v", file, err)
	}
	for _, revision := range []string{"missing-tag", "HEAD~1", "v1.0.0^{tree}", "../escape", "refs/heads/main"} {
		if _, err := u.ReadSource(ctx, "1", "main.go", 0, 0, revision); err == nil {
			t.Fatalf("accepted revision %q", revision)
		}
	}
	if _, err := u.GrepSource(ctx, "1", "new version", "", 10, "missing-tag"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing tag must not fall back: %v", err)
	}
	if got := git("rev-parse", "HEAD"); got != head {
		t.Fatalf("source tools changed checkout: %s", got)
	}
}
