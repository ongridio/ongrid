package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SkillManifest is the on-disk format for an external skill pack.
// Mirrors the skills.sh / openclaw layout: one JSON file next to the
// executable describes the LLM-facing surface; the entry runs as a
// subprocess and exchanges JSON via stdin/stdout.
type SkillManifest struct {
	// Name is the skill key (lower_snake). Required, validated against
	// the same rules native skills use.
	Name string `json:"name"`

	// Description is shown to humans (UI) and to the LLM (function
	// description). Required.
	Description string `json:"description"`

	// Schema is the raw JSON Schema for the args object. Optional —
	// when missing we hand the LLM an empty object schema.
	Schema json.RawMessage `json:"schema,omitempty"`

	// Entry is the path to the executable. Relative paths resolve
	// against the directory holding skill.json (typical layout: same
	// folder, with run.sh / run / etc.). Absolute paths must lie
	// underneath one of the configured external dirs — Loader rejects
	// anything that escapes via .. or symlink hop.
	Entry string `json:"entry"`

	// EnvAllow is the explicit list of env var names to forward into
	// the child. Empty list = no env at all (the child runs without
	// even PATH). To opt in to PATH, add "PATH" here.
	EnvAllow []string `json:"env_allow,omitempty"`

	// TimeoutSeconds caps the subprocess runtime. Zero falls back to
	// DefaultSubprocessTimeout (5m).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// Class follows the same {safe, mutating, dangerous} taxonomy as
	// native skills. Empty = ClassSafe.
	Class string `json:"class,omitempty"`

	// Category is a free-form group label. Defaults to "external" so
	// the UI can group all subprocess skills together.
	Category string `json:"category,omitempty"`
}

// LoaderConfig is what callers pass to NewLoader / LoadDirs.
type LoaderConfig struct {
	// Dirs is the allowlist of directories scanned for skill.json
	// files. Each directory's tree is walked recursively; we register
	// every skill.json we find. Non-absolute or non-existent paths are
	// skipped (with a log line) rather than erroring out — a fresh
	// install with no /etc/ongrid/skills directory should boot fine.
	Dirs []string

	// Logger receives one entry per registered / skipped manifest so
	// operators can audit what got picked up. May be nil (silent).
	Logger func(format string, args ...any)
}

// LoadDirs walks each directory in cfg.Dirs, parses every skill.json
// it finds, and registers a SubprocessSkill in the global registry.
// Returns the number of registered skills and the first fatal error
// (manifest parse / Register panic / etc.). Per-skill validation
// failures are logged and skipped — one bad pack should not block
// the entire boot.
func LoadDirs(cfg LoaderConfig) (int, error) {
	logf := cfg.Logger
	if logf == nil {
		logf = func(string, ...any) {}
	}
	registered := 0
	for _, raw := range cfg.Dirs {
		dir := strings.TrimSpace(raw)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			logf("skill loader: skip non-absolute dir %q", dir)
			continue
		}
		info, err := os.Stat(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				logf("skill loader: dir %s not present, skipping", dir)
				continue
			}
			logf("skill loader: stat %s: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			logf("skill loader: %s is not a directory, skipping", dir)
			continue
		}
		count, err := loadOneDir(dir, logf)
		registered += count
		if err != nil {
			return registered, err
		}
	}
	return registered, nil
}

// loadOneDir walks a single allowlist root and registers every
// skill.json under it. Returns count and the first error from
// filepath.Walk; per-manifest errors are logged and skipped.
func loadOneDir(root string, logf func(string, ...any)) (int, error) {
	count := 0
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logf("skill loader: walk %s: %v", path, err)
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) != "skill.json" {
			return nil
		}
		manifest, parseErr := parseManifest(path)
		if parseErr != nil {
			logf("skill loader: parse %s: %v", path, parseErr)
			return nil
		}
		ss, buildErr := buildSubprocessSkill(manifest, path, root)
		if buildErr != nil {
			logf("skill loader: build %s (%s): %v", manifest.Name, path, buildErr)
			return nil
		}
		// Skip duplicates instead of panicking — a redeploy that drops
		// a new manifest file before removing the old one shouldn't
		// crash the manager.
		if _, exists := Get(manifest.Name); exists {
			logf("skill loader: skill %q already registered, skipping %s", manifest.Name, path)
			return nil
		}
		// Use defer/recover guard; Register panics on validation. We
		// already validated above, but a future Register tightening
		// shouldn't take down boot.
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("skill loader: register %q panicked: %v", manifest.Name, r)
				}
			}()
			Register(ss)
			count++
			logf("skill loader: registered subprocess skill %q from %s", manifest.Name, path)
		}()
		return nil
	})
	return count, walkErr
}

// parseManifest reads and decodes a skill.json file.
func parseManifest(path string) (*SkillManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var m SkillManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &m, nil
}

// buildSubprocessSkill turns a parsed manifest into a SubprocessSkill,
// resolving the entry path against the manifest dir, enforcing the
// allowlist root, and validating the metadata.
func buildSubprocessSkill(m *SkillManifest, manifestPath, allowRoot string) (*SubprocessSkill, error) {
	if m.Name == "" {
		return nil, errors.New("name required")
	}
	if !validKey(m.Name) {
		return nil, fmt.Errorf("name %q must be lower_snake [a-z0-9_]", m.Name)
	}
	if m.Description == "" {
		return nil, errors.New("description required")
	}
	if m.Entry == "" {
		return nil, errors.New("entry required")
	}
	manifestDir := filepath.Dir(manifestPath)
	entry := m.Entry
	if !filepath.IsAbs(entry) {
		entry = filepath.Join(manifestDir, entry)
	}
	// Canonicalise so we can verify it lives under allowRoot. Use
	// EvalSymlinks so a manifest that puts a symlink to /bin/sh
	// outside the dir gets caught.
	absEntry, err := filepath.Abs(entry)
	if err != nil {
		return nil, fmt.Errorf("resolve entry: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absEntry)
	if err != nil {
		return nil, fmt.Errorf("eval entry symlinks: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(allowRoot)
	if err != nil {
		return nil, fmt.Errorf("eval allowlist root: %w", err)
	}
	if !pathHasPrefix(resolved, resolvedRoot) {
		return nil, fmt.Errorf("entry %s escapes allowlist root %s", resolved, resolvedRoot)
	}

	class := Class(m.Class)
	switch class {
	case "", ClassSafe, ClassMutating, ClassDangerous:
	default:
		return nil, fmt.Errorf("class %q invalid", m.Class)
	}
	if class == "" {
		class = ClassSafe
	}
	category := m.Category
	if category == "" {
		category = "external"
	}

	timeout := time.Duration(m.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultSubprocessTimeout
	}

	ss := &SubprocessSkill{
		Meta: Metadata{
			Key:           m.Name,
			Name:          m.Name,
			Description:   m.Description,
			Class:         class,
			Scope:         ScopeManager,
			Category:      category,
			ResultPreview: "{...} (subprocess skill output)",
		},
		Schema:   m.Schema,
		Entry:    resolved,
		EnvAllow: m.EnvAllow,
		Timeout:  timeout,
	}
	if err := ss.Metadata().Validate(); err != nil {
		return nil, err
	}
	return ss, nil
}

// pathHasPrefix returns true when child sits inside parent (or is the
// parent itself). Cleans both paths first so trailing slashes don't
// cause false negatives.
func pathHasPrefix(child, parent string) bool {
	child = filepath.Clean(child)
	parent = filepath.Clean(parent)
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}
