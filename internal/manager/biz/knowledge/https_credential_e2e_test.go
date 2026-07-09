// https_credential_e2e_test.go — phase 01-05 Task 1.
//
// End-to-end smoke test for HTTPS private-repo clone via GIT_ASKPASS injection.
// This test is skipped unless the following environment variables are set:
//
//	ONGRID_TEST_HTTPS_REPO  — full HTTPS URL of a real private git repo,
//	                          e.g. https://git.example.com/xxx/private.git
//	ONGRID_TEST_HTTPS_TOKEN — a valid Personal Access Token for that repo
//
// When both vars are set, the test:
//  1. Builds a fake RepoStore that returns a single HTTPSCredential for the
//     repo's host (using the env token).
//  2. Calls buildGitAuthEnv to get the GIT_ASKPASS env slice + cleanup func.
//  3. Runs "git clone --depth=1 <repoURL> <tmpDir>/checkout" via runGit.
//  4. Asserts clone succeeded, checkout dir is non-empty, cleanup deleted the
//     askpass tempfile, and the token does not appear in any error string.
//
// Security invariants verified:
//   - Token never leaks into error/output strings returned by runGit (T-05-03)
//   - GIT_ASKPASS tempfile is deleted by cleanup() after clone (T-05-02)
package knowledge

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

// ---------------------------------------------------------------------------
// E2E stub store — returns one HTTPSCredential for the target host
// ---------------------------------------------------------------------------

// e2eCredStore is a minimal RepoStore that serves one hard-wired
// HTTPSCredential for pickHTTPSCredentialForHost and records whether
// TouchHTTPSCredentialUsage was called. All other methods panic.
type e2eCredStore struct {
	cred        *model.HTTPSCredential
	touchCalled bool
}

func (s *e2eCredStore) ListHTTPSCredentials(_ context.Context) ([]*model.HTTPSCredential, error) {
	if s.cred == nil {
		return nil, nil
	}
	return []*model.HTTPSCredential{s.cred}, nil
}

func (s *e2eCredStore) TouchHTTPSCredentialUsage(_ context.Context, _ uint64) error {
	s.touchCalled = true
	return nil
}

// --- Remaining RepoStore methods (not exercised by this test) ---------------

func (s *e2eCredStore) ListRepos(_ context.Context) ([]*model.Repository, error) {
	return nil, nil
}
func (s *e2eCredStore) GetRepo(_ context.Context, _ uint64) (*model.Repository, error) {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) GetRepoByURL(_ context.Context, _ string) (*model.Repository, error) {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) CreateRepo(_ context.Context, _ *model.Repository) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) UpdateRepoSync(_ context.Context, _ uint64, _ int, _ string) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) DeleteRepo(_ context.Context, _ uint64) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) ListSSHIdentities(_ context.Context) ([]*model.SSHIdentity, error) {
	return nil, nil
}
func (s *e2eCredStore) GetSSHIdentity(_ context.Context, _ uint64) (*model.SSHIdentity, error) {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) CreateSSHIdentity(_ context.Context, _ *model.SSHIdentity) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) UpdateSSHIdentity(_ context.Context, _ uint64, _, _, _ string) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) TouchSSHIdentityUsage(_ context.Context, _ uint64) error {
	return nil
}
func (s *e2eCredStore) DeleteSSHIdentity(_ context.Context, _ uint64) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) GetHTTPSCredential(_ context.Context, _ uint64) (*model.HTTPSCredential, error) {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) CreateHTTPSCredential(_ context.Context, _ *model.HTTPSCredential) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) UpdateHTTPSCredential(_ context.Context, _ uint64, _, _, _ string, _ *string) error {
	panic("not implemented in e2eCredStore")
}
func (s *e2eCredStore) DeleteHTTPSCredential(_ context.Context, _ uint64) error {
	panic("not implemented in e2eCredStore")
}

// ---------------------------------------------------------------------------
// TestHTTPSE2E_ClonePrivateRepo — main e2e smoke test
// ---------------------------------------------------------------------------

// TestHTTPSE2E_ClonePrivateRepo performs an end-to-end validation of the full
// HTTPS credential injection chain:
//
//	POST /v1/knowledge/https-credentials  (model tier, in-memory here)
//	→ pickHTTPSCredentialForHost
//	→ buildGitAuthEnv / buildHTTPSEnv (GIT_ASKPASS script)
//	→ git clone --depth=1 <private-repo>
//
// The test only runs when both env vars are present; otherwise it skips.
// This keeps regular "go test ./..." CI-safe and network-free.
func TestHTTPSE2E_ClonePrivateRepo(t *testing.T) {
	repoURL := os.Getenv("ONGRID_TEST_HTTPS_REPO")
	token := os.Getenv("ONGRID_TEST_HTTPS_TOKEN")
	if repoURL == "" || token == "" {
		t.Skip("set ONGRID_TEST_HTTPS_REPO + ONGRID_TEST_HTTPS_TOKEN to run")
	}

	// Parse host from the repo URL.
	parsed, err := url.Parse(repoURL)
	if err != nil {
		t.Fatalf("invalid ONGRID_TEST_HTTPS_REPO URL %q: %v", repoURL, err)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		t.Fatalf("could not extract host from ONGRID_TEST_HTTPS_REPO=%q", repoURL)
	}

	// Build a fake store with one credential covering the target host.
	store := &e2eCredStore{
		cred: &model.HTTPSCredential{
			ID:        1,
			Name:      "e2e-test",
			HostsJSON: `["` + host + `"]`,
			Username:  "oauth2",
			Token:     token,
		},
	}
	u := &Usecase{repo: store}
	ctx := context.Background()

	// Step 1: Build git auth env via the full credential injection chain.
	gitEnv, cleanup, err := u.buildGitAuthEnv(ctx, repoURL)
	if err != nil {
		// Token must not appear in the error message (T-05-03).
		if strings.Contains(err.Error(), token) {
			t.Errorf("token leaked into buildGitAuthEnv error: %v", err)
		}
		t.Fatalf("buildGitAuthEnv failed: %v", err)
	}
	if len(gitEnv) == 0 {
		t.Fatal("buildGitAuthEnv returned empty env for a matched credential; GIT_ASKPASS not set")
	}

	// Record the askpass temp file path before cleanup so we can assert deletion.
	var askpassPath string
	for _, e := range gitEnv {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			askpassPath = strings.TrimPrefix(e, "GIT_ASKPASS=")
			break
		}
	}
	if askpassPath == "" {
		t.Fatal("GIT_ASKPASS not found in gitEnv")
	}

	// Step 2: Clone the private repo with --depth=1 into a temp directory.
	tmpDir := t.TempDir()
	checkoutDir := filepath.Join(tmpDir, "checkout")

	out, cloneErr := runGit(ctx, "", gitEnv, "clone", "--depth=1", repoURL, checkoutDir)

	// Step 3: Run cleanup BEFORE any t.Fatal so it always executes.
	cleanup()

	// Step 4: Assert clone succeeded.
	if cloneErr != nil {
		// Token must not appear in any git output or error text (T-05-03).
		if strings.Contains(out, token) {
			t.Errorf("token leaked into git output (redacted for safety)")
		}
		t.Fatalf("git clone failed: %v\noutput: %s", cloneErr, out)
	}

	// Token must not appear in git output even on success.
	if strings.Contains(out, token) {
		t.Errorf("token leaked into git clone output (T-05-03): output contains token value")
	}

	// Step 5: Assert checkout directory is non-empty (proves clone wrote files).
	entries, err := os.ReadDir(checkoutDir)
	if err != nil {
		t.Fatalf("ReadDir checkout: %v", err)
	}
	if len(entries) == 0 {
		t.Error("checkout directory is empty after clone; expected at least one file/directory")
	}

	// Step 6: Assert askpass tempfile was deleted by cleanup() (T-05-02).
	if _, statErr := os.Stat(askpassPath); !os.IsNotExist(statErr) {
		t.Errorf("GIT_ASKPASS tempfile %q still exists after cleanup(); expected deletion (T-05-02)", askpassPath)
	}

	// Step 7: Assert TouchHTTPSCredentialUsage was called (usage tracking).
	if !store.touchCalled {
		t.Error("TouchHTTPSCredentialUsage was not called; usage tracking is broken")
	}

	t.Logf("E2E clone SUCCESS: %d entries in checkout, askpass cleaned up, token not leaked", len(entries))
}
