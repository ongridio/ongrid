package tools

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

type recInstallProposer struct {
	url, srcType, ref, session string
}

func (r *recInstallProposer) ProposeInstall(_ context.Context, url, sourceType, ref, sessionID string, _ uint64) (string, error) {
	r.url, r.srcType, r.ref, r.session = url, sourceType, ref, sessionID
	return "approval-1", nil
}

func TestInferSourceType(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo":     "git",
		"https://github.com/owner/repo.git": "git",
		"git@github.com:owner/repo.git":     "git",
		"https://example.com/skill.tar.gz":  "tarball",
		"https://example.com/skill.tgz":     "tarball",
		"https://example.com/skill.TAR.GZ":  "tarball",
	}
	for in, want := range cases {
		if got := inferSourceType(in); got != want {
			t.Errorf("inferSourceType(%q) = %q, want %q", in, got, want)
		}
	}
}

// install_skill must thread the user-provided url + session id into the
// proposer, and auto-detect git vs tarball when type is omitted.
func TestInstallSkill_ProposeAndDetect(t *testing.T) {
	p := &recInstallProposer{}
	tool := NewInstallSkillTool(p, slog.Default())
	ctx := basetool.WithSessionID(context.Background(), "sess-9")
	if _, err := tool.InvokableRun(ctx, `{"url":"https://github.com/foo/bar"}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if p.url != "https://github.com/foo/bar" {
		t.Errorf("url = %q", p.url)
	}
	if p.srcType != "git" {
		t.Errorf("srcType = %q, want git (auto-detect)", p.srcType)
	}
	if p.session != "sess-9" {
		t.Errorf("session = %q, want sess-9", p.session)
	}
}

// Explicit type wins over auto-detection; missing url is an error.
func TestInstallSkill_ExplicitTypeAndValidation(t *testing.T) {
	p := &recInstallProposer{}
	tool := NewInstallSkillTool(p, slog.Default())
	if _, err := tool.InvokableRun(context.Background(), `{"url":"https://x/y","type":"tarball"}`); err != nil {
		t.Fatal(err)
	}
	if p.srcType != "tarball" {
		t.Errorf("explicit type not honored: %q", p.srcType)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"url":"  "}`); err == nil {
		t.Error("blank url should error")
	}
}
