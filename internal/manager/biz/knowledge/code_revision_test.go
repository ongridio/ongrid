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

// Tags published after the configured ref must be available in both sync paths.
func TestSyncFetchesReleaseTagsWithoutMovingConfiguredRef(t *testing.T) {
	u, _ := newCodeBrowseUC(t)
	ctx, remote := context.Background(), u.repoDir(1)
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	old := git(remote, "rev-parse", "HEAD")
	git(remote, "tag", "configured")
	clone := filepath.Join(t.TempDir(), "clone")
	git(remote, "clone", "--depth=1", "--branch", "configured", "file://"+remote, clone)
	if got := git(clone, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatal("fixture must be shallow")
	}
	git(remote, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "untagged history")
	historical := git(remote, "rev-parse", "HEAD")
	git(remote, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "release")
	release := git(remote, "rev-parse", "HEAD")
	git(remote, "tag", "release-light")
	git(remote, "-c", "user.name=test", "-c", "user.email=t@t", "tag", "-am", "release", "release-annotated")
	git(remote, "checkout", "-qb", "feature", old)
	git(remote, "-c", "user.name=test", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "feature only")
	feature := git(remote, "rev-parse", "HEAD")
	if !u.syncFastPath(ctx, clone, nil, "configured") {
		t.Fatal("fast sync failed")
	}
	for _, dir := range []string{clone, filepath.Join(t.TempDir(), "fresh")} {
		if dir != clone {
			if out, err := u.syncAtomicReplace(ctx, dir, nil, "file://"+remote, "configured"); err != nil {
				t.Fatalf("fresh sync: %v %s", err, out)
			}
		}
		if got := git(dir, "rev-parse", "HEAD"); got != old {
			t.Fatalf("configured ref moved: %s", got)
		}
		if got := git(dir, "rev-parse", "--is-shallow-repository"); got != "false" {
			t.Fatal("history remains shallow")
		}
		if got := git(dir, "rev-parse", "refs/remotes/origin/feature"); got != feature {
			t.Fatal("missing feature branch")
		}
		if got, err := sourceRevision(ctx, dir, historical); err != nil || got != historical {
			t.Fatalf("historical commit missing: %s %v", got, err)
		}
		for _, tag := range []string{"release-light", "release-annotated"} {
			if got, err := sourceRevision(ctx, dir, "refs/tags/"+tag); err != nil || got != release {
				t.Fatalf("%s: %s %v", tag, got, err)
			}
		}
	}
}
