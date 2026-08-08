package knowledge

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRepoStore is a minimal RepoStore: only ListRepos returns data; the
// rest satisfy the interface. Enough for the code-browse read paths.
type fakeRepoStore struct{ repos []*model.Repository }

func (f *fakeRepoStore) ListRepos(context.Context) ([]*model.Repository, error) { return f.repos, nil }
func (f *fakeRepoStore) GetRepo(context.Context, uint64) (*model.Repository, error) {
	return nil, errs.ErrNotFound
}
func (f *fakeRepoStore) GetRepoByURL(context.Context, string) (*model.Repository, error) {
	return nil, errs.ErrNotFound
}
func (f *fakeRepoStore) CreateRepo(context.Context, *model.Repository) error          { return nil }
func (f *fakeRepoStore) UpdateRepoSync(context.Context, uint64, int, string) error    { return nil }
func (f *fakeRepoStore) DeleteRepo(context.Context, uint64) error                     { return nil }
func (f *fakeRepoStore) ListSSHIdentities(context.Context) ([]*model.SSHIdentity, error) {
	return nil, nil
}
func (f *fakeRepoStore) GetSSHIdentity(context.Context, uint64) (*model.SSHIdentity, error) {
	return nil, errs.ErrNotFound
}
func (f *fakeRepoStore) CreateSSHIdentity(context.Context, *model.SSHIdentity) error          { return nil }
func (f *fakeRepoStore) UpdateSSHIdentity(context.Context, uint64, string, string, string) error {
	return nil
}
func (f *fakeRepoStore) TouchSSHIdentityUsage(context.Context, uint64) error { return nil }
func (f *fakeRepoStore) DeleteSSHIdentity(context.Context, uint64) error     { return nil }
func (f *fakeRepoStore) ListHTTPSCredentials(context.Context) ([]*model.HTTPSCredential, error) {
	return nil, nil
}
func (f *fakeRepoStore) GetHTTPSCredential(context.Context, uint64) (*model.HTTPSCredential, error) {
	return nil, errs.ErrNotFound
}
func (f *fakeRepoStore) CreateHTTPSCredential(context.Context, *model.HTTPSCredential) error {
	return nil
}
func (f *fakeRepoStore) UpdateHTTPSCredential(context.Context, uint64, string, string, string, *string) error {
	return nil
}
func (f *fakeRepoStore) TouchHTTPSCredentialUsage(context.Context, uint64) error { return nil }
func (f *fakeRepoStore) DeleteHTTPSCredential(context.Context, uint64) error     { return nil }

// newCodeBrowseUC builds a Usecase whose repo #1 clone is a real git repo at
// cloneDir/1 seeded with sample files. Returns the uc + repo URL.
func newCodeBrowseUC(t *testing.T) (*Usecase, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cloneDir := t.TempDir()
	repoURL := "https://github.com/acme/widget.git"
	dir := filepath.Join(cloneDir, "1")
	if err := os.MkdirAll(filepath.Join(dir, "internal/pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {\n\tResolveEdgeID()\n}\n")
	write("internal/pkg/resolver.go", "package pkg\n\n// ResolveEdgeID maps device→edge.\nfunc ResolveEdgeID() uint64 {\n\treturn 0\n}\n")
	write("README.md", "# widget\n\ndocs here\n")
	// a binary file (NUL byte) to exercise the binary guard
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		// Commit so HEAD exists — the tools read via plumbing (git show /
		// grep / ls-tree against HEAD), which needs a commit, not just an index.
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	uc := &Usecase{
		repo:     &fakeRepoStore{repos: []*model.Repository{{ID: 1, URL: repoURL, Branch: "main"}}},
		cloneDir: cloneDir,
		log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	return uc, repoURL
}

func TestListRepoSources(t *testing.T) {
	uc, url := newCodeBrowseUC(t)
	ctx := context.Background()

	// resolve by URL substring; root listing has dirs-first ordering and hides .git
	got, err := uc.ListRepoSources(ctx, "widget", "")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	if got.Repo != url || got.RepoID != 1 {
		t.Fatalf("wrong repo resolved: %+v", got)
	}
	var names []string
	for _, e := range got.Entries {
		if e.Path == ".git" {
			t.Fatal(".git should be hidden")
		}
		names = append(names, e.Path)
	}
	if !got.Entries[0].IsDir {
		t.Fatalf("dirs should sort first, got %+v", got.Entries)
	}
	if !strings.Contains(strings.Join(names, ","), "main.go") {
		t.Fatalf("main.go missing: %v", names)
	}

	// resolve by numeric id, list a subdir
	sub, err := uc.ListRepoSources(ctx, "1", "internal/pkg")
	if err != nil {
		t.Fatalf("list subdir: %v", err)
	}
	if len(sub.Entries) != 1 || sub.Entries[0].Path != "internal/pkg/resolver.go" {
		t.Fatalf("subdir listing wrong: %+v", sub.Entries)
	}
}

func TestReadSource(t *testing.T) {
	uc, _ := newCodeBrowseUC(t)
	ctx := context.Background()

	// whole file
	f, err := uc.ReadSource(ctx, "widget", "internal/pkg/resolver.go", 0, 0)
	if err != nil {
		t.Fatalf("read whole: %v", err)
	}
	if !strings.Contains(f.Content, "func ResolveEdgeID") || f.StartLine != 1 {
		t.Fatalf("whole-file read wrong: %+v", f)
	}

	// line window (the func signature is on line 4)
	win, err := uc.ReadSource(ctx, "widget", "internal/pkg/resolver.go", 4, 4)
	if err != nil {
		t.Fatalf("read window: %v", err)
	}
	if win.StartLine != 4 || win.EndLine != 4 || !strings.Contains(win.Content, "ResolveEdgeID") {
		t.Fatalf("window read wrong: %+v", win)
	}

	// binary file refused
	if _, err := uc.ReadSource(ctx, "widget", "blob.bin", 0, 0); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("binary read should be ErrInvalid, got %v", err)
	}

	// path traversal refused
	if _, err := uc.ReadSource(ctx, "widget", "../../../../etc/passwd", 0, 0); err == nil {
		t.Fatal("path traversal should fail")
	}
}

func TestGrepSource(t *testing.T) {
	uc, _ := newCodeBrowseUC(t)
	ctx := context.Background()

	res, err := uc.GrepSource(ctx, "widget", "ResolveEdgeID", "", 0)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("expected ≥2 hits for ResolveEdgeID (call + def), got %+v", res.Hits)
	}
	for _, h := range res.Hits {
		if h.Line <= 0 || h.Path == "" {
			t.Fatalf("malformed hit: %+v", h)
		}
	}

	// path_glob narrows; no match → empty (not error)
	none, err := uc.GrepSource(ctx, "widget", "ResolveEdgeID", "*.md", 0)
	if err != nil {
		t.Fatalf("grep glob: %v", err)
	}
	if len(none.Hits) != 0 {
		t.Fatalf("*.md should have no ResolveEdgeID hits, got %+v", none.Hits)
	}
}

func TestResolveRepoClone_Errors(t *testing.T) {
	uc, _ := newCodeBrowseUC(t)
	ctx := context.Background()

	// no match → ErrNotFound
	if _, _, err := uc.resolveRepoClone(ctx, "nonexistent-repo"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// registered but not synced (clone dir absent) → ErrInvalid
	uc.repo = &fakeRepoStore{repos: []*model.Repository{{ID: 99, URL: "https://github.com/acme/unsynced.git"}}}
	if _, _, err := uc.resolveRepoClone(ctx, "unsynced"); !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("want ErrInvalid for unsynced, got %v", err)
	}
}
