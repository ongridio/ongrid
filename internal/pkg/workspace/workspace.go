// Package workspace resolves agent working directories under a persistent
// root (HLD-019 Agent Workspace). The model is deliberately skill-agnostic:
// skills are stateless capabilities, while their files/products belong to the
// agent's working context. Two kinds:
//
//   - session scratch — sessions/<session-id>/ : one dir per chat session,
//     persistent across turns within the session (so a tool can write a file
//     in one command and read it back in the next), reclaimable when the
//     session is archived.
//   - named project   — projects/<name>/ : explicitly created, long-lived
//     (holds IaC state, git checkouts, etc.). [P2 — not yet wired.]
//
// A skill like terraform-runner then runs inside the agent's workspace
// (cwd = the session dir) instead of inventing its own /var/lib/ongrid/iac
// directory — the executor supplies the cwd, the skill stays directory-blind.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
)

// Manager resolves working directories under a configured root.
type Manager struct {
	root string
}

// New builds a Manager rooted at root. An empty root disables the workspace
// (Session/Project return "" so callers fall back to a transient temp dir).
func New(root string) *Manager { return &Manager{root: root} }

// Root reports the configured workspace root ("" = disabled).
func (m *Manager) Root() string { return m.root }

// Session returns the persistent working dir for a chat session, creating it
// if needed. Returns "" (no error) when the workspace is disabled (empty
// root) or the session id is empty/unsafe — the caller then falls back to a
// transient dir, preserving today's behavior.
func (m *Manager) Session(sessionID string) (string, error) {
	id := sanitizeID(sessionID)
	if m.root == "" || id == "" {
		return "", nil
	}
	dir := filepath.Join(m.root, "sessions", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("workspace: mkdir session %q: %w", id, err)
	}
	return dir, nil
}

// sanitizeID keeps only a filesystem-safe slug ([A-Za-z0-9._-]) so a session
// id can never escape the workspace root via path separators or "..". A
// result of "", "." or ".." is rejected (returns "").
func sanitizeID(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b = append(b, r)
		}
	}
	out := string(b)
	if out == "" || out == "." || out == ".." {
		return ""
	}
	return out
}
