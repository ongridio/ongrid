package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShellRunnerEnvInjectionAndExit(t *testing.T) {
	r := NewShellRunner()
	// Env is fully replaced: the injected var is visible, and a manager-only
	// var (not in Spec.Env) must NOT leak through.
	t.Setenv("ONGRID_SECRET_KEY", "leaky")
	res, err := r.Run(context.Background(), Spec{
		Script: `echo "id=$MY_ID secret=${ONGRID_SECRET_KEY:-none}"; exit 3`,
		Env:    map[string]string{"MY_ID": "AKID123"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "id=AKID123") {
		t.Fatalf("injected env missing: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "secret=none") {
		t.Fatalf("manager env leaked into child: %q", res.Stdout)
	}
}

func TestShellRunnerTimeout(t *testing.T) {
	r := NewShellRunner()
	start := time.Now()
	res, err := r.Run(context.Background(), Spec{Script: "sleep 5", Timeout: 200 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if res.ExitCode != -1 {
		t.Fatalf("exit = %d, want -1 on timeout", res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout=200ms, returned after %s", elapsed)
	}
}

func TestShellRunnerOutputCap(t *testing.T) {
	r := NewShellRunner()
	res, err := r.Run(context.Background(), Spec{
		Script:         `head -c 100000 /dev/zero | tr '\0' 'x'`,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated || len(res.Stdout) > 1024 {
		t.Fatalf("output not capped: len=%d truncated=%v", len(res.Stdout), res.Truncated)
	}
}
