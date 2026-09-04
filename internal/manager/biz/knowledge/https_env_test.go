// https_env_test.go — phase 01-04 Task 1 tests for buildHTTPSEnv and
// buildGitAuthEnv HTTPS branch.
//
// Security invariants tested:
//   - Token value never appears in script body (T-04-01: no argv / file leak)
//   - askpass script reads the token from $GIT_PASSWORD at runtime
//   - Temp file permissions are owner-only (0o700: rwx------) (T-04-02)
//   - cleanup() deletes the temp file (T-04-02)
package knowledge

import (
	"context"
	"os"
	"strings"
	"testing"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
)

// ---------------------------------------------------------------------------
// buildHTTPSEnv tests
// ---------------------------------------------------------------------------

// TestBuildHTTPSEnv_EnvKeys verifies that buildHTTPSEnv returns the three
// required git environment variable keys.
func TestBuildHTTPSEnv_EnvKeys(t *testing.T) {
	cred := &model.HTTPSCredential{
		Username: "oauth2",
		Token:    "glpat-supersecret",
	}
	env, cleanup, err := buildHTTPSEnv(cred)
	if err != nil {
		t.Fatalf("buildHTTPSEnv returned error: %v", err)
	}
	defer cleanup()

	wantKeys := []string{"GIT_ASKPASS=", "GIT_USERNAME=", "GIT_PASSWORD="}
	for _, key := range wantKeys {
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, key) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("env missing key %q; got %v", key, env)
		}
	}
}

// TestBuildHTTPSEnv_AskpassPermissions verifies that the GIT_ASKPASS script
// is created with owner-only permissions (0o700: rwx------). git executes
// GIT_ASKPASS directly via execve; without the execute bit git reports
// "permission denied" and authentication fails.
func TestBuildHTTPSEnv_AskpassPermissions(t *testing.T) {
	cred := &model.HTTPSCredential{
		Username: "oauth2",
		Token:    "glpat-supersecret",
	}
	env, cleanup, err := buildHTTPSEnv(cred)
	if err != nil {
		t.Fatalf("buildHTTPSEnv returned error: %v", err)
	}
	defer cleanup()

	// Extract script path from GIT_ASKPASS env entry.
	var scriptPath string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			scriptPath = strings.TrimPrefix(e, "GIT_ASKPASS=")
			break
		}
	}
	if scriptPath == "" {
		t.Fatal("GIT_ASKPASS not found in env")
	}

	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat askpass script: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Errorf("askpass script permissions = %04o; want 0700 (owner rwx, group/other=0)", perm)
	}
}

// TestBuildHTTPSEnv_TokenNotInScriptContent verifies the T-04-01 invariant:
// the token value is never written into the script body. The script must read
// the token from the $GIT_PASSWORD environment variable at runtime.
func TestBuildHTTPSEnv_TokenNotInScriptContent(t *testing.T) {
	const secretToken = "glpat-toomanysecrets"
	cred := &model.HTTPSCredential{
		Username: "oauth2",
		Token:    secretToken,
	}
	env, cleanup, err := buildHTTPSEnv(cred)
	if err != nil {
		t.Fatalf("buildHTTPSEnv returned error: %v", err)
	}
	defer cleanup()

	var scriptPath string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			scriptPath = strings.TrimPrefix(e, "GIT_ASKPASS=")
			break
		}
	}

	content, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read askpass script: %v", err)
	}
	if strings.Contains(string(content), secretToken) {
		t.Errorf("token value %q found in askpass script body — token must be read from $GIT_PASSWORD at runtime, not hardcoded", secretToken)
	}
	// Script must reference $GIT_PASSWORD so git can get the token at runtime.
	if !strings.Contains(string(content), "GIT_PASSWORD") {
		t.Errorf("askpass script does not reference GIT_PASSWORD; script body:\n%s", content)
	}
}

// TestBuildHTTPSEnv_CleanupDeletesFile verifies that calling cleanup() removes
// the askpass temporary script (T-04-02).
func TestBuildHTTPSEnv_CleanupDeletesFile(t *testing.T) {
	cred := &model.HTTPSCredential{
		Username: "oauth2",
		Token:    "glpat-supersecret",
	}
	env, cleanup, err := buildHTTPSEnv(cred)
	if err != nil {
		t.Fatalf("buildHTTPSEnv returned error: %v", err)
	}

	var scriptPath string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			scriptPath = strings.TrimPrefix(e, "GIT_ASKPASS=")
			break
		}
	}

	// Verify file exists before cleanup.
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("askpass script should exist before cleanup: %v", err)
	}

	cleanup()

	// Verify file is gone after cleanup.
	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Errorf("askpass script should be removed by cleanup(); stat returned: %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildGitAuthEnv HTTPS branch tests
// ---------------------------------------------------------------------------

// TestBuildGitAuthEnv_HTTPSWithCredential verifies that buildGitAuthEnv
// returns a non-empty env when the repo has a matching HTTPS credential.
func TestBuildGitAuthEnv_HTTPSWithCredential(t *testing.T) {
	cred := &model.HTTPSCredential{
		ID:        1,
		Username:  "oauth2",
		Token:     "glpat-supersecret",
		HostsJSON: `["git.example.com"]`,
	}
	stub := &httpsTestRepo{creds: []*model.HTTPSCredential{cred}}
	u := &Usecase{repo: stub}

	env, cleanup, err := u.buildGitAuthEnv(context.Background(), "https://git.example.com/foo/bar.git")
	if err != nil {
		t.Fatalf("buildGitAuthEnv returned error: %v", err)
	}
	defer cleanup()

	if len(env) == 0 {
		t.Error("buildGitAuthEnv should return non-empty env for matching HTTPS credential")
	}

	hasAskpass := false
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_ASKPASS=") {
			hasAskpass = true
		}
	}
	if !hasAskpass {
		t.Errorf("env missing GIT_ASKPASS; got %v", env)
	}

	// Verify TouchHTTPSCredentialUsage was called.
	if !stub.touchCalled {
		t.Error("TouchHTTPSCredentialUsage should have been called on credential match")
	}
}

// TestBuildGitAuthEnv_HTTPSNoCredential verifies that buildGitAuthEnv
// returns nil env + noop cleanup when no credential matches the host
// (anonymous mode — public repos still work).
func TestBuildGitAuthEnv_HTTPSNoCredential(t *testing.T) {
	stub := &httpsTestRepo{creds: []*model.HTTPSCredential{}}
	u := &Usecase{repo: stub}

	env, cleanup, err := u.buildGitAuthEnv(context.Background(), "https://github.com/public/repo.git")
	if err != nil {
		t.Fatalf("buildGitAuthEnv returned unexpected error: %v", err)
	}
	if env != nil {
		t.Errorf("buildGitAuthEnv should return nil env for unmatched HTTPS host; got %v", env)
	}
	// cleanup must be safe to call (noop).
	cleanup()
}

// ---------------------------------------------------------------------------
// Stub RepoStore implementation
// ---------------------------------------------------------------------------

// httpsTestRepo is a minimal RepoStore stub that satisfies only the methods
// exercised by buildGitAuthEnv / pickHTTPSCredentialForHost. All other methods
// panic so tests that accidentally call them fail loudly.
type httpsTestRepo struct {
	creds       []*model.HTTPSCredential
	touchCalled bool
}

// ListHTTPSCredentials returns the pre-loaded credentials.
func (s *httpsTestRepo) ListHTTPSCredentials(_ context.Context) ([]*model.HTTPSCredential, error) {
	return s.creds, nil
}

// TouchHTTPSCredentialUsage records that it was called.
func (s *httpsTestRepo) TouchHTTPSCredentialUsage(_ context.Context, _ uint64) error {
	s.touchCalled = true
	return nil
}

// The remaining RepoStore methods are not exercised by these tests.

func (s *httpsTestRepo) ListRepos(_ context.Context) ([]*model.Repository, error) {
	panic("not implemented")
}
func (s *httpsTestRepo) GetRepo(_ context.Context, _ uint64) (*model.Repository, error) {
	panic("not implemented")
}
func (s *httpsTestRepo) GetRepoByURL(_ context.Context, _ string) (*model.Repository, error) {
	panic("not implemented")
}
func (s *httpsTestRepo) CreateRepo(_ context.Context, _ *model.Repository) error {
	panic("not implemented")
}
func (s *httpsTestRepo) UpdateRepoSync(_ context.Context, _ uint64, _ int, _ string) error {
	panic("not implemented")
}
func (s *httpsTestRepo) DeleteRepo(_ context.Context, _ uint64) error {
	panic("not implemented")
}
func (s *httpsTestRepo) ListSSHIdentities(_ context.Context) ([]*model.SSHIdentity, error) {
	return nil, nil
}
func (s *httpsTestRepo) GetSSHIdentity(_ context.Context, _ uint64) (*model.SSHIdentity, error) {
	panic("not implemented")
}
func (s *httpsTestRepo) CreateSSHIdentity(_ context.Context, _ *model.SSHIdentity) error {
	panic("not implemented")
}
func (s *httpsTestRepo) UpdateSSHIdentity(_ context.Context, _ uint64, _, _, _ string) error {
	panic("not implemented")
}
func (s *httpsTestRepo) TouchSSHIdentityUsage(_ context.Context, _ uint64) error {
	return nil
}
func (s *httpsTestRepo) DeleteSSHIdentity(_ context.Context, _ uint64) error {
	panic("not implemented")
}
func (s *httpsTestRepo) GetHTTPSCredential(_ context.Context, _ uint64) (*model.HTTPSCredential, error) {
	panic("not implemented")
}
func (s *httpsTestRepo) CreateHTTPSCredential(_ context.Context, _ *model.HTTPSCredential) error {
	panic("not implemented")
}
func (s *httpsTestRepo) UpdateHTTPSCredential(_ context.Context, _ uint64, _, _, _ string, _ *string) error {
	panic("not implemented")
}
func (s *httpsTestRepo) DeleteHTTPSCredential(_ context.Context, _ uint64) error {
	panic("not implemented")
}

// TestHTTPSNoCredHint covers the locale-neutral English suffix appended to
// last_sync_error when a private HTTPS clone fails with no credential
// configured for its host (AUTH-04 / Phase 1 SC5). It must name the host,
// stay English, and stay silent for every not-a-missing-cred case.
func TestHTTPSNoCredHint(t *testing.T) {
	const authOut = "fatal: could not read Username for 'https://git.example.com': terminal prompts disabled"

	// Missing-credential HTTPS auth failure → hint names the host.
	got := httpsNoCredHint("https://git.example.com/team/repo.git", nil, authOut)
	if !strings.Contains(got, "host=git.example.com") {
		t.Fatalf("expected hint to contain host=git.example.com, got %q", got)
	}
	if !strings.Contains(got, "no HTTPS credential configured") {
		t.Fatalf("expected English no-credential guidance, got %q", got)
	}

	// Credential was injected (GIT_ASKPASS present) → no hint.
	withCred := []string{"GIT_ASKPASS=/tmp/ongrid-askpass-x.sh", "GIT_PASSWORD=secret"}
	if got := httpsNoCredHint("https://git.example.com/team/repo.git", withCred, "authentication failed"); got != "" {
		t.Fatalf("expected no hint when credential injected, got %q", got)
	}

	// SSH URL → no hint (SSH has its own flow).
	if got := httpsNoCredHint("git@git.example.com:team/repo.git", nil, authOut); got != "" {
		t.Fatalf("expected no hint for SSH URL, got %q", got)
	}

	// Non-auth failure (network) → no hint.
	netOut := "fatal: unable to access 'https://git.example.com/team/repo.git': Could not resolve host: git.example.com"
	if got := httpsNoCredHint("https://git.example.com/team/repo.git", nil, netOut); got != "" {
		t.Fatalf("expected no hint for non-auth failure, got %q", got)
	}
}
