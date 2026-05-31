//go:build e2e

// Package testenv provides the e2e test harness: MySQL bring-up, manager
// binary spawn, fake external services, and the secret-loading helpers
// documented in tests/e2e/README.md.
//
// This file owns the secret model:
//
//   - real tokens NEVER live in the repo. testenv looks them up in
//     os.Getenv first, then tests/e2e/secrets.local.env (gitignored).
//   - tests that talk to real external services wrap their secret in
//     RequireSecret(t, name); missing secrets → t.Skip with a uniform
//     message, never a failure, never a leak.
//   - live-mode toggles (E2E_LIVE_SLACK=1 etc) are likewise read through
//     the same helpers so the dotenv file can carry them too.
//
// Read once per process, cached. Tests that mutate process env between
// runs should call Reload() — not common, exists for completeness.
package testenv

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// secretsFileName is the conventional path under tests/e2e/.
const secretsFileName = "secrets.local.env"

// store caches the parsed dotenv on first read so we don't open the file
// once per RequireSecret call. Guarded by mu.
var (
	mu     sync.Mutex
	loaded bool
	dotenv map[string]string
)

// Reload drops the dotenv cache. Tests rarely need this; it exists so
// "mutate the file inside a test then re-read" works for tooling tests.
func Reload() {
	mu.Lock()
	defer mu.Unlock()
	loaded = false
	dotenv = nil
}

// LookupSecret returns the value of `name` from os.Getenv first, then
// secrets.local.env. Returns ("", false) when neither has it. This is the
// non-skipping variant — callers that need t.Skip use RequireSecret.
func LookupSecret(name string) (string, bool) {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v, true
	}
	mu.Lock()
	if !loaded {
		dotenv = loadDotenv()
		loaded = true
	}
	v, ok := dotenv[name]
	mu.Unlock()
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// RequireSecret returns the value of `name` or calls t.Skip with the
// uniform message described in tests/e2e/README.md. Intended for live-
// mode tests that meaningfully need the real external endpoint.
func RequireSecret(t *testing.T, name string) string {
	t.Helper()
	v, ok := LookupSecret(name)
	if !ok {
		t.Skipf("SKIP: %s — needs %s (set in env or %s; see secrets.example.env for the template)",
			t.Name(), name, displaySecretsPath())
		return ""
	}
	return v
}

// LiveMode is a small helper for the suite-level toggles: returns true
// when the named E2E_LIVE_X flag is set to a truthy value, OR when
// E2E_LIVE_ALL is set. Tests typically use:
//
//	if !testenv.LiveMode("SLACK") {
//	    t.Skip("default mock-only run; set E2E_LIVE_SLACK=1 to enable")
//	}
//
// LiveMode does NOT call t.Skip itself — callers decide what to do, so a
// suite can run partially-live (mocks + one real). RequireSecret already
// handles the "I asked for live but the token is missing" path.
func LiveMode(label string) bool {
	if isTruthy(getRaw("E2E_LIVE_ALL")) {
		return true
	}
	return isTruthy(getRaw("E2E_LIVE_" + strings.ToUpper(label)))
}

// RedactSecret returns a short prefix + ellipsis so log lines that
// might carry a token don't dump it. Use when logging request shape /
// auth headers from inside a test.
func RedactSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return "***"
	}
	return s[:6] + "…"
}

// ─── internal helpers ──────────────────────────────────────────────

func getRaw(name string) string {
	v, _ := LookupSecret(name)
	return v
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// loadDotenv reads tests/e2e/secrets.local.env relative to THIS file's
// location so `go test ./tests/e2e/...` from any cwd still finds it.
// Tolerates a missing file (returns empty map) — the default run is
// "no secrets, all live-mode tests skip", that's by design.
func loadDotenv() map[string]string {
	out := map[string]string{}
	path := secretsFilePath()
	if path == "" {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		// Strip surrounding quotes if present, dotenv style.
		if n := len(v); n >= 2 && ((v[0] == '"' && v[n-1] == '"') || (v[0] == '\'' && v[n-1] == '\'')) {
			v = v[1 : n-1]
		}
		out[k] = v
	}
	return out
}

// secretsFilePath finds tests/e2e/secrets.local.env by walking up from
// this file's source location. Returns "" if not found. Falls back to
// the current working directory + ./tests/e2e for cases where the test
// binary has been moved (rare).
func secretsFilePath() string {
	_, src, _, ok := runtime.Caller(0)
	if ok {
		// src = .../tests/e2e/testenv/secrets.go
		// secrets file = .../tests/e2e/secrets.local.env
		base := filepath.Dir(filepath.Dir(src))
		p := filepath.Join(base, secretsFileName)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Fallback: cwd + tests/e2e/.
	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, "tests", "e2e", secretsFileName)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// displaySecretsPath is the human-readable form used in t.Skip messages.
// Prefer the workspace-relative form so the same string is meaningful
// regardless of where `go test` was invoked from.
func displaySecretsPath() string {
	p := secretsFilePath()
	if p == "" {
		return "tests/e2e/" + secretsFileName
	}
	// Trim the common prefix up to "tests/" so the message reads
	// "tests/e2e/secrets.local.env" instead of an absolute path.
	if i := strings.Index(p, "tests/e2e/"); i >= 0 {
		return p[i:]
	}
	return p
}
