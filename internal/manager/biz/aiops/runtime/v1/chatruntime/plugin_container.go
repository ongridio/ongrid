package chatruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PluginManifest is the on-disk shape of `.claude-plugin/plugin.json`
// (claude-code) and `openclaw.plugin.json` (openclaw).
// ongrid recognizes both: the former is treated as the canonical, the
// latter is treated as a superset whose ongrid-irrelevant fields are
// preserved into UIMetadata["openclaw_legacy"].
//
// The struct is deliberately permissive — we model only the fields
// ongrid currently consumes; any other field is captured into Extras
// and (for openclaw) routed into Pack.UIMetadata["openclaw_legacy"].
type PluginManifest struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Description  string          `json:"description"`
	ConfigSchema json.RawMessage `json:"configSchema"`

	// Extras captures every JSON field not modeled above. We keep it
	// raw so we can stash openclaw-specific keys (providers / channels
	// / cliBackends / providerAuthChoices, ...) into Pack.UIMetadata.
	Extras map[string]json.RawMessage `json:"-"`
}

// ContainerKind labels which marker file we found in a candidate dir.
type ContainerKind string

const (
	// ContainerClaude is the `.claude-plugin/plugin.json` form.
	ContainerClaude ContainerKind = "claude"
	// ContainerOpenclaw is the `openclaw.plugin.json` form.
	ContainerOpenclaw ContainerKind = "openclaw"
	// ContainerBareSkills is the skills.sh / vercel-labs/skills form:
	// a directory containing one or more `skills/<name>/SKILL.md`
	// (or a root-level SKILL.md) and NO manifest file. The pack ID
	// and display name are synthesized from the directory name. This
	// is what `npx skills add owner/repo` produces.
	ContainerBareSkills ContainerKind = "bare_skills"
	// ContainerNone means no recognized layout was found.
	ContainerNone ContainerKind = ""
)

// DetectContainer probes a directory for plugin container markers.
// Returns the marker kind and absolute path to the manifest file
// (empty for ContainerBareSkills, which has no manifest). Resolution
// order: openclaw > claude-plugin > bare-skills > none.
//
// ongrid recognizes the openclaw and claude-plugin manifest forms
// plus the bare-skills (skills.sh / vercel-labs/skills) layout
// where there's no manifest and the pack is just `skills/<name>/SKILL.md`
// drops under a parent dir.
func DetectContainer(dir string) (ContainerKind, string, error) {
	openclawPath := filepath.Join(dir, "openclaw.plugin.json")
	if info, err := os.Stat(openclawPath); err == nil && !info.IsDir() {
		return ContainerOpenclaw, openclawPath, nil
	}
	claudePath := filepath.Join(dir, ".claude-plugin", "plugin.json")
	if info, err := os.Stat(claudePath); err == nil && !info.IsDir() {
		return ContainerClaude, claudePath, nil
	}
	if hasBareSkills(dir) {
		return ContainerBareSkills, "", nil
	}
	return ContainerNone, "", nil
}

// hasBareSkills reports whether dir looks like a skills.sh-style pack —
// either a root-level SKILL.md or at least one `skills/<sub>/SKILL.md`.
// Scans at most one level deep under `skills/` to keep the probe cheap.
func hasBareSkills(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !info.IsDir() {
		return true
	}
	skillsDir := filepath.Join(dir, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if info, err := os.Stat(filepath.Join(skillsDir, e.Name(), "SKILL.md")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// LoadPluginContainer parses the marker manifest in dir, then recursively
// loads every SKILL.md / agent .md / command .md found under the pack
// root. / — claude-code-style bundle dropped into the
// skills root has all sub-content auto-registered.
//
// Sub-roots:
//   - claude pack: <dir>/skills/, <dir>/agents/, <dir>/commands/
//   - openclaw pack: paths from manifest.skills[] (relative); also
//     <dir>/skills/, <dir>/agents/, <dir>/commands/ when present
//
// Path safety: every sub-path is resolved with filepath.EvalSymlinks
// and verified to land back inside the pack root before parse — symlink
// hops out of the tree and `..` components are rejected with an
// "escapes_root" warning. The pack itself is loaded; only the offending
// file is dropped.
//
// Per-file parse failures are recorded as warnings (code "parse_failed")
// rather than aborting the load — a single bad SKILL.md should not take
// the whole pack offline.
//
// hooks/ subdirs are walked once for diagnostics (per-hook warning code
// "hooks_dropped"); .mcp.json triggers a single "mcp_unsupported"
// warning. Neither is acted upon
func LoadPluginContainer(dir string) (*LoadResult, error) {
	kind, manifestPath, err := DetectContainer(dir)
	if err != nil {
		return nil, fmt.Errorf("chatruntime: detect container in %s: %w", dir, err)
	}
	if kind == ContainerNone {
		return nil, fmt.Errorf("chatruntime: no recognized pack layout in %s (need .claude-plugin/plugin.json, openclaw.plugin.json, or at least one skills/<name>/SKILL.md)", dir)
	}

	var (
		pack     *Pack
		warnings []LoadWarning
		raw      []byte
	)
	if kind == ContainerBareSkills {
		// skills.sh-style drop: no manifest, synthesize one from the
		// directory name. SHA-256 over the empty manifest stays
		// deterministic across re-installs of the same dir.
		pack = synthesizeBareSkillsPack(dir)
	} else {
		raw, err = os.ReadFile(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: read %s: %w", manifestPath, err)
		}
		pack, warnings, err = parsePluginManifest(raw, kind)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: parse %s: %w", manifestPath, err)
		}
	}
	pack.Dir = mustAbs(dir)
	pack.ManifestSHA256 = sha256Hex(raw)

	// Resolve the pack root — used as the allowlist boundary for every
	// sub-file we touch. EvalSymlinks lets the user place the pack
	// itself behind a symlink (cp -r dance) without breaking the root,
	// while still rejecting symlinks under it that escape outward.
	resolvedRoot, rootErr := filepath.EvalSymlinks(dir)
	if rootErr != nil {
		// If we can't even resolve the root we can't safely walk;
		// return what we have (metadata only) plus a warning.
		warnings = append(warnings, LoadWarning{
			Path:   dir,
			Code:   "root_resolve_failed",
			Reason: fmt.Sprintf("eval symlinks on pack root: %v; sub-tree skipped", rootErr),
		})
		return &LoadResult{Pack: pack, Warnings: warnings}, nil
	}

	// Skills sub-tree.
	skillRoots := resolveSkillSubRoots(dir, kind, raw, &warnings)
	var skills []*Skill
	for _, sr := range skillRoots {
		more, w := walkSkillsRoot(sr, resolvedRoot)
		skills = append(skills, more...)
		warnings = append(warnings, w...)
	}

	// Agents sub-tree (claude only — openclaw doesn't ship agents/).
	agentsDir := filepath.Join(dir, "agents")
	agents, agentWarns := walkAgentsRoot(agentsDir, resolvedRoot)
	warnings = append(warnings, agentWarns...)

	// Commands sub-tree → keyword skills.
	commandsDir := filepath.Join(dir, "commands")
	commandSkills, commandWarns := walkCommandsRoot(commandsDir, resolvedRoot)
	skills = append(skills, commandSkills...)
	warnings = append(warnings, commandWarns...)

	// hooks/ + .mcp.json — diagnostics only.
	warnings = append(warnings, scanHooksDir(filepath.Join(dir, "hooks"), resolvedRoot)...)
	if info, err := os.Stat(filepath.Join(dir, ".mcp.json")); err == nil && !info.IsDir() {
		warnings = append(warnings, LoadWarning{
			Path:   filepath.Join(dir, ".mcp.json"),
			Code:   "mcp_unsupported",
			Reason: ".mcp.json found; ongrid does not run MCP servers yet",
		})
	}

	return &LoadResult{
		Pack:     pack,
		Skills:   skills,
		Agents:   agents,
		Warnings: warnings,
	}, nil
}

// resolveSkillSubRoots returns the directories we should walk for
// SKILL.md files. Claude packs always use <dir>/skills; openclaw packs
// MAY declare a manifest.skills[] array of relative paths — we follow
// those, plus the conventional <dir>/skills if it exists.
//
// Every returned path is verified to be a child of `dir` (string-level
// guard against `..` in manifest.skills[]); deeper symlink-vs-root
// validation happens during the walk itself.
func resolveSkillSubRoots(dir string, kind ContainerKind, manifestRaw []byte, warnings *[]LoadWarning) []string {
	var roots []string
	seen := map[string]bool{}

	add := func(p string) {
		clean := filepath.Clean(p)
		if seen[clean] {
			return
		}
		seen[clean] = true
		if info, err := os.Stat(clean); err == nil && info.IsDir() {
			roots = append(roots, clean)
		}
	}

	if kind == ContainerOpenclaw {
		// Pull the optional skills[] array. When present it's authoritative —
		// we follow only those paths (treats manifest.skills[]
		// and the conventional skills/ directory as equivalent forms; a pack
		// that lists both would otherwise double-load).
		var generic struct {
			Skills []string `json:"skills"`
		}
		hasManifestSkills := false
		if err := json.Unmarshal(manifestRaw, &generic); err == nil {
			for _, raw := range generic.Skills {
				if strings.TrimSpace(raw) == "" {
					continue
				}
				hasManifestSkills = true
				// Disallow absolute paths and `..` components in the
				// manifest declaration itself — purely string check;
				// EvalSymlinks during the walk catches symlink escape.
				if filepath.IsAbs(raw) || strings.Contains(raw, "..") {
					*warnings = append(*warnings, LoadWarning{
						Path:   raw,
						Code:   "skills_path_rejected",
						Reason: fmt.Sprintf("manifest skills[] entry %q is absolute or contains '..'; rejected", raw),
					})
					continue
				}
				add(filepath.Join(dir, raw))
			}
		}
		// Fall through to the conventional dir only if the manifest
		// didn't declare its own skills[] list.
		if !hasManifestSkills {
			add(filepath.Join(dir, "skills"))
		}
	} else if kind == ContainerBareSkills {
		// skills.sh layout: either a multi-skill repo with
		// skills/<name>/SKILL.md or a single-skill repo with a
		// root-level SKILL.md. Pick the right root — adding BOTH
		// causes the walker to double-load every skills/* file
		// (once via skills/, once via the recursive dir walk).
		if info, err := os.Stat(filepath.Join(dir, "skills")); err == nil && info.IsDir() {
			add(filepath.Join(dir, "skills"))
		} else {
			add(dir)
		}
	} else {
		// Claude pack: conventional <dir>/skills only.
		add(filepath.Join(dir, "skills"))
	}
	return roots
}

// walkSkillsRoot walks skillsDir for SKILL.md files, returning the
// parsed skills + non-fatal warnings. resolvedRoot is the eval-symlinked
// pack root used to detect escape via symlinks.
func walkSkillsRoot(skillsDir, resolvedRoot string) ([]*Skill, []LoadWarning) {
	info, err := os.Stat(skillsDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var skills []*Skill
	var warnings []LoadWarning

	walkErr := filepath.Walk(skillsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, LoadWarning{Path: path, Code: "walk_error", Reason: walkErr.Error()})
			return nil
		}
		if info == nil {
			return nil
		}
		// Symlinks (Lstat: ModeSymlink) need explicit policy — filepath.Walk
		// does NOT follow them. We must validate that the link target is
		// inside resolvedRoot; if it escapes, emit an escapes_root warning
		// so the test (and humans) see the rejection. If it lands inside,
		// we still don't auto-recurse (avoid loops); operators can land
		// SKILL.md files at real paths.
		if info.Mode()&os.ModeSymlink != 0 {
			if !pathSafeUnderRoot(path, resolvedRoot, &warnings) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) != "SKILL.md" {
			return nil
		}
		if !pathSafeUnderRoot(path, resolvedRoot, &warnings) {
			return nil
		}
		sk, ws, err := ParseSkillMd(path)
		warnings = append(warnings, ws...)
		if err != nil {
			warnings = append(warnings, LoadWarning{
				Path:   path,
				Code:   "parse_failed",
				Reason: err.Error(),
			})
			return nil
		}
		sk.Dir = filepath.Dir(path)
		// Default activation = always when neither
		// top-level nor metadata.ongrid.activation set a mode.
		if sk.Activation.Mode == "" {
			sk.Activation.Mode = "always"
		}
		if sk.Metadata.Ongrid.Activation.Mode == "" {
			sk.Metadata.Ongrid.Activation.Mode = sk.Activation.Mode
		}
		skills = append(skills, sk)
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, LoadWarning{Path: skillsDir, Code: "walk_error", Reason: walkErr.Error()})
	}
	return skills, warnings
}

// walkAgentsRoot walks agentsDir for *.md agent personas.
// README.md is skipped (documentation, not a persona) — same policy as
// AgentRegistry.Load.
func walkAgentsRoot(agentsDir, resolvedRoot string) ([]*Agent, []LoadWarning) {
	info, err := os.Stat(agentsDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var agents []*Agent
	var warnings []LoadWarning

	walkErr := filepath.Walk(agentsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, LoadWarning{Path: path, Code: "walk_error", Reason: walkErr.Error()})
			return nil
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(info.Name()), ".md") {
			return nil
		}
		if strings.EqualFold(info.Name(), "README.md") {
			return nil
		}
		if !pathSafeUnderRoot(path, resolvedRoot, &warnings) {
			return nil
		}
		ag, ws, err := ParseAgentMd(path)
		warnings = append(warnings, ws...)
		if err != nil {
			warnings = append(warnings, LoadWarning{
				Path:   path,
				Code:   "parse_failed",
				Reason: err.Error(),
			})
			return nil
		}
		ag.Dir = filepath.Dir(path)
		agents = append(agents, ag)
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, LoadWarning{Path: agentsDir, Code: "walk_error", Reason: walkErr.Error()})
	}
	return agents, warnings
}

// walkCommandsRoot walks commandsDir for *.md command files and
// converts each into a keyword skill. Nested subdirs
// are flattened — `commands/git/commit.md` becomes `cmd_commit`.
// README.md is skipped.
func walkCommandsRoot(commandsDir, resolvedRoot string) ([]*Skill, []LoadWarning) {
	info, err := os.Stat(commandsDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var skills []*Skill
	var warnings []LoadWarning

	walkErr := filepath.Walk(commandsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, LoadWarning{Path: path, Code: "walk_error", Reason: walkErr.Error()})
			return nil
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(info.Name()), ".md") {
			return nil
		}
		if strings.EqualFold(info.Name(), "README.md") {
			return nil
		}
		if !pathSafeUnderRoot(path, resolvedRoot, &warnings) {
			return nil
		}
		sk, ws, err := ConvertCommandFile(path)
		warnings = append(warnings, ws...)
		if err != nil {
			warnings = append(warnings, LoadWarning{
				Path:   path,
				Code:   "command_parse_failed",
				Reason: err.Error(),
			})
			return nil
		}
		skills = append(skills, sk)
		return nil
	})
	if walkErr != nil {
		warnings = append(warnings, LoadWarning{Path: commandsDir, Code: "walk_error", Reason: walkErr.Error()})
	}
	return skills, warnings
}

// scanHooksDir walks the hooks/ subdir (if present) and emits one
// warning per hook file plus one summary warning. ongrid never executes
// hooks.
func scanHooksDir(hooksDir, resolvedRoot string) []LoadWarning {
	info, err := os.Stat(hooksDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	var warnings []LoadWarning
	// Top-level summary warning so callers grepping for "hooks_unsupported"
	// keep working (back-compat with PR-2 test).
	warnings = append(warnings, LoadWarning{
		Path:   hooksDir,
		Code:   "hooks_unsupported",
		Reason: "hooks/ subdir found; ongrid does not run plugin hooks",
	})

	var dropped []string
	_ = filepath.Walk(hooksDir, func(path string, info os.FileInfo, _ error) error {
		if info == nil || info.IsDir() {
			return nil
		}
		if !pathSafeUnderRoot(path, resolvedRoot, &warnings) {
			return nil
		}
		dropped = append(dropped, path)
		return nil
	})
	sort.Strings(dropped)
	for _, p := range dropped {
		warnings = append(warnings, LoadWarning{
			Path:   p,
			Code:   "hooks_dropped",
			Reason: "hook file ignored; ongrid never executes plugin hooks",
		})
	}
	return warnings
}

// pathSafeUnderRoot validates that path, after resolving symlinks, sits
// inside resolvedRoot. Failures append an "escapes_root" or
// "symlink_error" warning to *warnings and return false so the caller
// can skip the offending file.
//
// Implementation note: we use filepath.EvalSymlinks rather than a pure
// string-prefix check. A string check would miss symlink hops (the
// classic /tmp/evil → /etc dodge), and absent EvalSymlinks any
// `<dir>/skills/<x>` symlink that resolves outside the pack would be
// silently followed by ParseSkillMd. The trade-off is one extra
// stat-per-file plus a hard fail when the path itself can't be
// resolved (broken symlink, race-deleted file) — surfaced as
// symlink_error rather than crashing the load.
func pathSafeUnderRoot(path, resolvedRoot string, warnings *[]LoadWarning) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		*warnings = append(*warnings, LoadWarning{
			Path:   path,
			Code:   "symlink_error",
			Reason: err.Error(),
		})
		return false
	}
	if !pathHasPrefix(resolved, resolvedRoot) {
		*warnings = append(*warnings, LoadWarning{
			Path:   path,
			Code:   "escapes_root",
			Reason: fmt.Sprintf("path traversal: resolved path %s escapes pack root %s", resolved, resolvedRoot),
		})
		return false
	}
	return true
}

// parsePluginManifest decodes the manifest JSON and produces a Pack.
// openclawLegacyKeys are the openclaw-only fields ongrid stuffs into
// UIMetadata["openclaw_legacy"] for SPA display.
func parsePluginManifest(raw []byte, kind ContainerKind) (*Pack, []LoadWarning, error) {
	var manifest PluginManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, fmt.Errorf("decode: %w", err)
	}

	// Re-decode into a generic map to capture extras.
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, nil, fmt.Errorf("decode generic: %w", err)
	}

	known := map[string]struct{}{
		"id":           {},
		"name":         {},
		"version":      {},
		"description":  {},
		"configSchema": {},
	}
	extras := map[string]json.RawMessage{}
	for k, v := range generic {
		if _, ok := known[k]; ok {
			continue
		}
		extras[k] = v
	}
	manifest.Extras = extras

	var warnings []LoadWarning

	pack := &Pack{
		ID:             manifest.ID,
		DisplayName:    manifest.Name,
		Version:        manifest.Version,
		Description:    manifest.Description,
		ConfigSchema:   manifest.ConfigSchema,
		SignatureState: "unsigned",
	}

	// id/name/version/description go straight into Pack.
	// Required: id is the only must-have.
	if pack.ID == "" {
		// Some claude-code plugin.json files use `name` as the id when
		// id is absent — fall back to that for compat.
		if manifest.Name != "" {
			pack.ID = manifest.Name
			warnings = append(warnings, LoadWarning{
				Code:   "id_fallback_from_name",
				Reason: "plugin manifest missing 'id'; using 'name' as id",
			})
		} else {
			return nil, warnings, fmt.Errorf("manifest missing required field 'id'")
		}
	}

	// openclaw-only fields are routed into ui_metadata.openclaw_legacy
	// so the SPA can render them, but ongrid's runtime ignores them.
	if kind == ContainerOpenclaw && len(extras) > 0 {
		legacy := map[string]any{}
		for k, v := range extras {
			// Keep raw JSON; SPA may want the structure verbatim.
			var decoded any
			if err := json.Unmarshal(v, &decoded); err == nil {
				legacy[k] = decoded
			} else {
				legacy[k] = string(v)
			}
		}
		pack.UIMetadata = map[string]any{
			"openclaw_legacy": legacy,
		}
		warnings = append(warnings, LoadWarning{
			Code:   "openclaw_legacy_preserved",
			Reason: fmt.Sprintf("preserved %d openclaw-specific manifest fields under ui_metadata.openclaw_legacy", len(legacy)),
		})
	} else if len(extras) > 0 {
		// Claude-plugin manifest with extra unrecognized keys — keep
		// them too, but under ui_metadata.extra.
		extraMap := map[string]any{}
		for k, v := range extras {
			var decoded any
			if err := json.Unmarshal(v, &decoded); err == nil {
				extraMap[k] = decoded
			} else {
				extraMap[k] = string(v)
			}
		}
		pack.UIMetadata = map[string]any{
			"extra": extraMap,
		}
	}

	return pack, warnings, nil
}

// synthesizeBareSkillsPack builds a minimal Pack for a skills.sh-style
// drop where there's no plugin.json / openclaw.plugin.json manifest.
// ID = sanitized directory basename (lowercase, hyphens), DisplayName
// = same, Version = "0.0.0". The pack-level ConfigSchema / UIMetadata
// stay empty — the individual SKILL.md files are the canonical metadata
// source for this layout.
func synthesizeBareSkillsPack(dir string) *Pack {
	base := strings.TrimSpace(filepath.Base(strings.TrimRight(dir, string(filepath.Separator))))
	id := bareSkillsPackID(base)
	return &Pack{
		ID:          id,
		DisplayName: base,
		Version:     "0.0.0",
		Description: "Bare skills.sh-style pack (no manifest).",
	}
}

// bareSkillsPackID normalises a directory basename into a pack id.
// Rules mirror the SKILL.md skillNameRe: lowercase, digits, hyphens;
// non-conformant chars collapse to a single hyphen; leading/trailing
// hyphens trimmed; empty fallback = "untitled-skill-pack".
func bareSkillsPackID(base string) string {
	if base == "" {
		return "untitled-skill-pack"
	}
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled-skill-pack"
	}
	return out
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func mustAbs(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}
