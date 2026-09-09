package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryURLValidation(t *testing.T) {
	for _, raw := range []string{"https://example.com/a.git", "http://git.internal/a.git", "git@git.internal:team/a.git", "ssh://git@git.internal:2222/team/a.git"} {
		if err := validateRepositoryURL(raw); err != nil {
			t.Fatalf("valid URL rejected: %s %v", raw, err)
		}
	}
	for _, raw := range []string{"", "/etc", "file:///etc", "ext::sh -c anything", "https://token@example.com/a.git", "ssh://git:password@example.com/a.git", "https://example.com/a.git?token=secret", "git@-host:a.git", "git@host:", "https://example.com/a.git\n"} {
		if err := validateRepositoryURL(raw); err == nil {
			t.Fatalf("invalid URL accepted: %q", raw)
		}
	}
}

func TestMissingRefWithSSHWarning(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = check-ref-format ]; then exit 0; fi\nprintf '%s\\n' \"Warning: Permanently added host to known hosts.\" >&2\nexit 2\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	u := &Usecase{}
	err := u.validateRepository(context.Background(), "https://example/repo.git", "missing")
	if err == nil || !strings.Contains(err.Error(), "branch or tag") || !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "known hosts") {
		t.Fatalf("wrong missing-ref error: %v", err)
	}
}
