// Package host_files's sandbox.go declares the path / binary allow-list
// gates that wrap the three filesystem-inspection handlers. Real
// kernel-level isolation (seccomp / cgroups / chroot) is out of scope —
// runtime documents the long-term wasmtime/MCP path.
// Until that lands, defense-in-depth here is two-fold:
//
//  1. Path validator: every path arriving from the LLM is canonicalised
//     (filepath.Abs + EvalSymlinks) and required to live under one of
//     AllowedReadPaths. `..` traversal, symlinks pointing outside, and
//     bare relative paths all reject. Empty path also rejects — every
//     handler that accepts a path requires it explicitly.
//
//  2. Binary allow-list: only commands in AllowedBinaries are resolvable.
//     The handler asks the sandbox for
//     a binary by short name; the sandbox returns the absolute path
//     resolved at startup. Anything not in the allow-list errors.
package host_files

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// SandboxConfig defines what the host_files plugin is allowed to access.
// Defaults are defined below and may be overridden at edge boot.
//
// Path policy (revised 2026-05-08): read operations default to "ALLOW
// everything except DeniedReadPaths". The previous allowlist semantics
// (only /data /var /opt /home /tmp /srv) was over-conservative — it
// blocked legitimate diagnostic targets like `du /` (root scan to find
// where disk went) and `find /usr -size +100M`. Read-only by definition
// can't damage the host, so the policy is now denylist-driven:
//
//   - DeniedReadPaths: virtual filesystems that du/find can't usefully
//     traverse anyway (/proc /sys /dev /run) plus a few high-sensitivity
//     leak vectors (/etc/shadow /root/.ssh ...). When the requested path
//     is exactly or under any denied prefix, the call is refused.
//
//   - AllowedReadPaths: kept as an OPTIONAL allowlist. When non-empty
//     the path must additionally match one of these; when empty the
//     denylist is the only constraint. Operators who want stricter
//     containment than the default can still set this.
//
// AllowedBinaries maps short name → resolved absolute path discovered
// via exec.LookPath at construction time.
type SandboxConfig struct {
	// DeniedReadPaths is the canonical absolute prefix denylist applied
	// to every read operation. Empty allows everything (use only in
	// dev / tests).
	DeniedReadPaths []string

	// AllowedReadPaths is an optional additional allowlist. When
	// non-empty the path must match in addition to passing the denylist.
	// Default is empty (denylist alone).
	AllowedReadPaths []string

	AllowedBinaries map[string]string
}

// DefaultDeniedReadPaths is the conservative baseline: virtual
// filesystems (du / find against these never finishes) + a small set of
// high-sensitivity files that an LLM should never need to read for a
// legitimate diagnostic.
var DefaultDeniedReadPaths = []string{
	// Virtual / pseudo filesystems — du / find traverse forever
	"/proc",
	"/sys",
	"/dev",
	"/run",
	// Hot password / key material
	"/etc/shadow",
	"/etc/gshadow",
	"/etc/sudoers",
	"/etc/sudoers.d",
	// Per-user SSH and GPG private state — globbed at validation time
	// via dynamic prefix match, but listed here for documentation.
	"/root/.ssh",
	"/root/.gnupg",
	// kernel-image / firmware blobs aren't writable but are large and
	// uninteresting; not denied — operators may want to inspect.
}

// DefaultSandboxConfig allows reads except for DefaultDeniedReadPaths.
// Binary discovery best-efforts each candidate; missing binaries simply
// do not appear in AllowedBinaries (calls to ResolveBinary then error
// cleanly with a message the LLM can render).
func DefaultSandboxConfig() *SandboxConfig {
	return &SandboxConfig{
		DeniedReadPaths: append([]string(nil), DefaultDeniedReadPaths...),
		// AllowedReadPaths intentionally empty — denylist alone.
		// `df` is added for the coverage hint in du_summary — read-only,
		// no path argument unless we pass one explicitly, fits the same
		// "host triage primitive" envelope as du/find/stat.
		AllowedBinaries: discoverBinaries([]string{"find", "du", "df", "stat", "ls"}),
	}
}

// Validate performs startup-time invariants on the sandbox.
//
// Required:
//   - find + du resolved (stat is Go-native via os.Stat so it is not required)
//
// AllowedReadPaths can be empty (denylist alone is the policy);
// DeniedReadPaths can be empty in dev / tests but defaults to the
// virtual-fs + sensitive-file baseline (see DefaultDeniedReadPaths).
//
// On failure Register propagates the error to main.go; main.go logs and
// continues without the host_files capability — better to boot the edge
// without the tool than to crash the whole agent.
func (s *SandboxConfig) Validate() error {
	if s == nil {
		return errors.New("host_files sandbox: config is nil")
	}
	for _, name := range []string{"find", "du"} {
		if _, ok := s.AllowedBinaries[name]; !ok {
			return fmt.Errorf("host_files sandbox: required binary %q not found in PATH or fallback locations", name)
		}
	}
	return nil
}

// ValidatePath returns nil if `path` is under one of the AllowedReadPaths
// after lexical cleaning AND, when the path exists, symlink resolution.
// Empty path → error. Outside allow-list → error. Symlink-to-outside →
// error. The function is canonical-tolerant: it does not require the
// path to exist (find_large_files may scan a path the user just got
// wrong; we let the find subprocess surface that, not the validator).
//
// Decision matrix:
//   - Path exists + EvalSymlinks succeeds → the resolved form must be
//     in the symlink-resolved allow-list. This is the strict path: it
//     catches adversarial symlinks (/tmp/escape -> /etc) and tolerates
//     OS-canonical differences (macOS /var → /private/var, t.TempDir()
//     under /var/folders → /private/var/folders).
//   - Path doesn't exist (or resolution fails) → fall back to the
//     lexical check against the raw allow-list. This is the loose
//     path; it accepts not-yet-created paths under a whitelisted
//     prefix without failing the call up front.
func (s *SandboxConfig) ValidatePath(path string) error {
	if s == nil {
		return errors.New("host_files sandbox: not configured")
	}
	if path == "" {
		return errors.New("host_files sandbox: path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("host_files sandbox: path %q is not absolute", path)
	}
	clean := filepath.Clean(path)

	// Resolve symlinks when possible so an adversarial /tmp/escape →
	// /etc/shadow gets caught by the denylist on the resolved form.
	resolved := clean
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = r
	}

	// Step 1: denylist check (always applies). Reject when either the
	// raw path or its resolved form is exactly or under a denied prefix.
	if denied, hit := s.matchesDenied(clean, resolved); denied {
		return fmt.Errorf("host_files sandbox: path %q is denied for read (matches deny prefix %q — virtual filesystem or sensitive material)",
			path, hit)
	}
	// Step 2: per-user .ssh / .gnupg are denied as a glob — the static
	// list catches /root, this catches /home/<user>/.
	if hit, denied := matchesPerUserSensitive(resolved); denied {
		return fmt.Errorf("host_files sandbox: path %q (resolves to %q) is denied: %s — per-user secret material",
			path, resolved, hit)
	}

	// Step 3: optional allowlist. When AllowedReadPaths is non-empty the
	// path must additionally land under one of those prefixes.
	if len(s.AllowedReadPaths) > 0 {
		if !s.lexicalInAllowList(clean) && !s.resolvedInAllowList(resolved) {
			return fmt.Errorf("host_files sandbox: path %q is outside the operator-set allow-list (%s)",
				path, strings.Join(s.AllowedReadPaths, " "))
		}
	}
	return nil
}

// matchesDenied returns (true, prefix) when clean OR resolved is exactly
// equal to or under any DeniedReadPaths prefix.
func (s *SandboxConfig) matchesDenied(clean, resolved string) (bool, string) {
	for _, denied := range s.DeniedReadPaths {
		d := filepath.Clean(denied)
		for _, candidate := range []string{clean, resolved} {
			if candidate == d {
				return true, d
			}
			if strings.HasPrefix(candidate, d+string(filepath.Separator)) {
				return true, d
			}
		}
	}
	return false, ""
}

// matchesPerUserSensitive catches /home/<user>/.ssh and /home/<user>/.gnupg
// (covers any user; /root is handled by the static DeniedReadPaths list).
func matchesPerUserSensitive(p string) (string, bool) {
	if !strings.HasPrefix(p, "/home/") {
		return "", false
	}
	parts := strings.Split(p, string(filepath.Separator))
	// /home/<user>/.ssh → ["", "home", "<user>", ".ssh", ...]
	if len(parts) < 4 {
		return "", false
	}
	switch parts[3] {
	case ".ssh", ".gnupg":
		return "/home/" + parts[2] + "/" + parts[3], true
	}
	return "", false
}

// lexicalInAllowList performs the cheap prefix-match against the raw
// (unresolved) allow-list. Trailing separator on the prefix prevents
// /var-evil from matching /var.
func (s *SandboxConfig) lexicalInAllowList(p string) bool {
	for _, allowed := range s.AllowedReadPaths {
		a := filepath.Clean(allowed)
		if p == a {
			return true
		}
		if strings.HasPrefix(p, a+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvedInAllowList performs the prefix-match against the
// symlink-resolved allow-list — i.e. each entry passed through
// EvalSymlinks. Used as a second pass when the requested path's
// resolved form differs from its cleaned form.
func (s *SandboxConfig) resolvedInAllowList(p string) bool {
	for _, allowed := range s.AllowedReadPaths {
		a, err := filepath.EvalSymlinks(filepath.Clean(allowed))
		if err != nil {
			a = filepath.Clean(allowed)
		}
		if p == a {
			return true
		}
		if strings.HasPrefix(p, a+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ResolveBinary returns the absolute path of `name` from the allow-list
// or an error. Used by the handlers when they need to spawn find/du.
// The lookup is O(1) (map) so calling this on every handler invocation
// is fine — re-lookup also defends against a node where /usr/bin/find
// got replaced after agent startup (if the original entry no longer
// exists exec.Command will fail at run time and we propagate cleanly).
func (s *SandboxConfig) ResolveBinary(name string) (string, error) {
	if s == nil {
		return "", errors.New("host_files sandbox: not configured")
	}
	abs, ok := s.AllowedBinaries[name]
	if !ok {
		return "", fmt.Errorf("host_files sandbox: binary %q not in allow-list", name)
	}
	return abs, nil
}

// discoverBinaries resolves each name via exec.LookPath, falling back to
// the conventional Linux/Darwin locations when LookPath fails (root's
// PATH on a stripped systemd unit may not include /usr/local/bin).
// Missing binaries simply do not appear in the map; callers see a clean
// "binary not in allow-list" error.
func discoverBinaries(names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, n := range names {
		if abs, err := exec.LookPath(n); err == nil {
			out[n] = abs
			continue
		}
		for _, prefix := range []string{"/usr/bin", "/bin", "/usr/local/bin", "/sbin", "/usr/sbin"} {
			candidate := filepath.Join(prefix, n)
			if _, err := exec.LookPath(candidate); err == nil {
				out[n] = candidate
				break
			}
		}
	}
	return out
}
