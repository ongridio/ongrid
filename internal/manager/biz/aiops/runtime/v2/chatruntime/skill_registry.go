package chatruntime

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// SkillRegistry holds every Skill discovered under a skills root plus the
// non-fatal warnings accumulated during the walk. Mirrors the shape of
// sealsuite-agent's SkillRegistry but adapted to SKILL.md.
//
// PR-2 scope: load + Resolve. Tool-class filter logic happens here at
// the skill level (we drop tools whose Class isn't allowed by the
// policy); the actual factory binding will land in PR-3.
//
// Concurrency: a sync.RWMutex protects the slices. marketplace
// Install / Uninstall calls Reload while in-flight chats may be calling
// Resolve / All — Reload swaps a freshly-built slice atomically under
// the write lock and returning slices are *value copies* so callers
// already holding a result don't observe mutations.
type SkillRegistry struct {
	mu       sync.RWMutex
	skills   []*Skill
	warnings []LoadWarning
}

// NewSkillRegistry returns an empty registry.
func NewSkillRegistry() *SkillRegistry { return &SkillRegistry{} }

// Load walks skillsRoot looking for SKILL.md files and plugin containers
// and parses each one. Walks recursively; any depth under skillsRoot is OK.
// A non-existent skillsRoot is not an error (a fresh install with no skills
// boots fine).
//
// As of PR-skill-load (simplified loader) Load delegates to
// LoadAll, which also recurses into `.claude-plugin/` and
// `openclaw.plugin.json` containers and pulls their skills + commands
// (agents from packs in the skills root are also kept on the result, but
// they are ignored by this method since SkillRegistry only owns skills —
// callers wanting agents from packs should use LoadAll directly).
//
// Path safety: symlinks are resolved and verified to land back inside
// skillsRoot before parse. Per-file parse errors land as warnings rather
// than aborting; one bad SKILL.md should not take down boot.
func (r *SkillRegistry) Load(skillsRoot string) error {
	res, err := LoadAll(LoadAllConfig{SkillsRoot: skillsRoot})
	if err != nil {
		return fmt.Errorf("chatruntime: skill registry load: %w", err)
	}
	r.mu.Lock()
	r.skills = append([]*Skill(nil), res.Skills...)
	r.warnings = append([]LoadWarning(nil), res.Warnings...)
	r.mu.Unlock()
	return nil
}

// Reload replaces the registry contents with a fresh scan of skillsRoot
// (delegating through LoadAll so packs / plugin containers / warnings
// behave identically to the boot-time path). Used by marketplace
// Install/Uninstall to hot-reload without restart.
//
// Atomicity: builds the new slice outside the lock, then swaps under
// r.mu.Lock() in O(1). All previously-returned slices from All() /
// Warnings() / Resolve() are value copies and remain unchanged — an
// in-flight chat that already grabbed a Resolve() result keeps running
// against the snapshot it was given.
//
// Empty skillsRoot wipes the registry (i.e. behaves like Load("") which
// is a no-op, but explicitly: skills slice goes to nil so a subsequent
// All() observes "nothing"). A non-existent skillsRoot is not an error
// — same convention as Load.
//
// Variadic extras lets the marketplace pass (builtinRoot, userRoot) so
// the image-baked built-in skills survive a hot-reload triggered by a
// pack install — a single-root Reload would otherwise drop them.
func (r *SkillRegistry) Reload(skillsRoot string, extras ...string) error {
	res, err := LoadAll(LoadAllConfig{SkillsRoot: skillsRoot, ExtraSkillsRoots: extras})
	if err != nil {
		return fmt.Errorf("chatruntime: skill registry reload: %w", err)
	}
	newSkills := append([]*Skill(nil), res.Skills...)
	newWarnings := append([]LoadWarning(nil), res.Warnings...)
	r.mu.Lock()
	r.skills = newSkills
	r.warnings = newWarnings
	r.mu.Unlock()
	return nil
}

// All returns a copy of every loaded skill (so callers can't mutate
// the registry's internal slice).
func (r *SkillRegistry) All() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, len(r.skills))
	copy(out, r.skills)
	return out
}

// Warnings returns a copy of every warning recorded during Load.
func (r *SkillRegistry) Warnings() []LoadWarning {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]LoadWarning, len(r.warnings))
	copy(out, r.warnings)
	return out
}

// Add inserts a programmatically-constructed skill. Used by feature-flag
// gated skills that don't exist as SKILL.md on disk (e.g. a runtime
// proposal skill).
func (r *SkillRegistry) Add(sk *Skill) {
	if sk == nil {
		return
	}
	r.mu.Lock()
	r.skills = append(r.skills, sk)
	r.mu.Unlock()
}

// AddAll inserts a batch of skills. Nil entries are skipped. Used by
// LoadAll to merge plugin-container output into the registry.
func (r *SkillRegistry) AddAll(skills []*Skill) {
	for _, sk := range skills {
		r.Add(sk)
	}
}

// AddWarnings appends loader warnings (e.g. from LoadAll) so
// SkillRegistry.Warnings() exposes them alongside per-skill parse
// warnings.
func (r *SkillRegistry) AddWarnings(ws []LoadWarning) {
	if len(ws) == 0 {
		return
	}
	r.mu.Lock()
	r.warnings = append(r.warnings, ws...)
	r.mu.Unlock()
}

// Resolve filters loaded skills against (query, policy):
//
//  1. Activation filter: mode "always" → include; mode "keyword" →
//     include only when query (lowercased) contains any keyword.
//  2. Policy filter: drop tools whose Class is not in policy.AllowedClasses.
//     The skill survives as long as at least one tool survives. A skill
//     declaring zero tools is always kept after the activation filter
//     (it may be a pure-prompt skill).
//
// Returned skills are *copies* with their Tools slice already filtered.
// The original registry skills are not mutated.
func (r *SkillRegistry) Resolve(query string, policy Policy) []*Skill {
	queryLower := strings.ToLower(strings.TrimSpace(query))
	r.mu.RLock()
	skills := make([]*Skill, len(r.skills))
	copy(skills, r.skills)
	r.mu.RUnlock()
	var out []*Skill
	for _, sk := range skills {
		if !activationMatches(sk.Activation, queryLower) {
			continue
		}
		filtered := filterToolsByPolicy(sk.Tools, policy)
		if len(sk.Tools) > 0 && len(filtered) == 0 {
			// Skill had tools but none survived policy — drop the skill.
			continue
		}
		clone := *sk
		clone.Tools = filtered
		out = append(out, &clone)
	}
	return out
}

// activationMatches reports whether a skill's Activation matches the
// (already lowercased) query.
func activationMatches(a Activation, queryLower string) bool {
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	switch mode {
	case "", "always":
		return true
	case "keyword":
		for _, kw := range a.Keywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if kw == "" {
				continue
			}
			if strings.Contains(queryLower, kw) {
				return true
			}
		}
		return false
	default:
		// Unknown mode — fail open; the parse-time warning is the place
		// to surface this instead of silently disabling the skill.
		return true
	}
}

// filterToolsByPolicy drops tools whose Class is not allowed.
func filterToolsByPolicy(tools []ToolDecl, policy Policy) []ToolDecl {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ToolDecl, 0, len(tools))
	for _, t := range tools {
		// Empty class → assume read-only (safe default).
		class := t.Class
		if class == "" {
			class = ClassRead
		}
		if !policy.Allows(class) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// pathHasPrefix returns true when child sits inside parent (or is the
// parent itself). Cleans both paths first so trailing slashes don't
// cause false negatives. Mirrors internal/skill/loader.go for consistency.
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
