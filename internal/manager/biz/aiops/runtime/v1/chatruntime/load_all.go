package chatruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAllConfig is the input to LoadAll — the unified entry point for
// populating SkillRegistry + AgentRegistry from disk. PR-skill-load
// (simplified loader) collapses what used to be two
// separate walks (SkillRegistry.Load + AgentRegistry.Load) into one.
//
// Both roots are optional. A non-existent root is not an error (a
// fresh install with no skills/agents boots fine).
type LoadAllConfig struct {
	// SkillsRoot is the directory walked for SKILL.md files and plugin
	// containers. Plugin containers found here contribute their skills,
	// agents, and command-converted skills to the result.
	SkillsRoot string

	// AgentsRoot is the directory walked for *.md agent personas.
	// Plugin containers found here contribute their agents (and their
	// skills + command-skills, since a pack drop in either root is
	// equally valid).
	AgentsRoot string

	// ExtraSkillsRoots is a list of additional directories to walk
	// alongside SkillsRoot. Used to keep image-baked built-in skills
	// in one root and marketplace-installed packs in another, so
	// marketplace Reload() doesn't have to drop the built-ins to refresh
	// the user-installed set. Empty / non-existent entries are skipped
	// silently.
	ExtraSkillsRoots []string

	// ExtraAgentsRoots mirrors ExtraSkillsRoots for the agents side —
	// each entry is walked with the "loose *.md = agent persona" rule
	// so additional persona drops pile on top of AgentsRoot without
	// the loose .md files being silently filtered out as non-SKILL.md.
	ExtraAgentsRoots []string
}

// LoadAll walks SkillsRoot + AgentsRoot once, dispatching to the right
// parser per file / directory:
//
//   - <root>/.../SKILL.md → ParseSkillMd
//   - <root>/.../<dir>/.claude-plugin/plugin.json
//   - <root>/.../<dir>/openclaw.plugin.json → LoadPluginContainer (recursive)
//   - <agents-root>/.../*.md (not under a pack) → ParseAgentMd
//
// When a plugin container is detected, the walk SkipDir's into it and
// hands off to LoadPluginContainer; this prevents double-loading the
// same SKILL.md once from the outer walk and again from the recursive
// container load.
//
// The LoadResult.Pack field is left nil — LoadAll aggregates packs
// across multiple roots and the field is reserved for the
// single-container LoadPluginContainer flow.
func LoadAll(cfg LoadAllConfig) (*LoadResult, error) {
	res := &LoadResult{}

	if cfg.SkillsRoot != "" {
		more, err := walkLoadRoot(cfg.SkillsRoot, true)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: load skills root %q: %w", cfg.SkillsRoot, err)
		}
		res.Skills = append(res.Skills, more.Skills...)
		res.Agents = append(res.Agents, more.Agents...)
		res.Warnings = append(res.Warnings, more.Warnings...)
	}

	for _, extra := range cfg.ExtraSkillsRoots {
		if extra == "" {
			continue
		}
		more, err := walkLoadRoot(extra, true)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: load extra skills root %q: %w", extra, err)
		}
		res.Skills = append(res.Skills, more.Skills...)
		res.Agents = append(res.Agents, more.Agents...)
		res.Warnings = append(res.Warnings, more.Warnings...)
	}

	for _, extra := range cfg.ExtraAgentsRoots {
		if extra == "" {
			continue
		}
		more, err := walkLoadRoot(extra, false)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: load extra agents root %q: %w", extra, err)
		}
		res.Skills = append(res.Skills, more.Skills...)
		res.Agents = append(res.Agents, more.Agents...)
		res.Warnings = append(res.Warnings, more.Warnings...)
	}

	if cfg.AgentsRoot != "" {
		more, err := walkLoadRoot(cfg.AgentsRoot, false)
		if err != nil {
			return nil, fmt.Errorf("chatruntime: load agents root %q: %w", cfg.AgentsRoot, err)
		}
		res.Skills = append(res.Skills, more.Skills...)
		res.Agents = append(res.Agents, more.Agents...)
		res.Warnings = append(res.Warnings, more.Warnings...)
	}
	return res, nil
}

// walkLoadRoot walks root once. wantSkills selects whether we treat
// loose SKILL.md (true) or loose *.md (false → agent personas) as the
// primary content type — i.e. the root's "purpose". Plugin containers
// found in either root contribute everything they ship.
func walkLoadRoot(root string, wantSkills bool) (*LoadResult, error) {
	res := &LoadResult{}

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return nil, fmt.Errorf("stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", root)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("eval symlinks %q: %w", root, err)
	}

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			res.Warnings = append(res.Warnings, LoadWarning{
				Path: path, Code: "walk_error", Reason: walkErr.Error(),
			})
			return nil
		}
		if info == nil {
			return nil
		}

		// A symlink at the top of a tree is a common drop pattern (e.g.
		// /var/lib/ongrid/system/skills/<pack> → real-bundle). filepath.Walk
		// won't follow it on its own, so we Stat-resolve it: if it lands
		// inside resolvedRoot AND points to a directory that hosts a
		// plugin container, treat it as a container.
		if info.Mode()&os.ModeSymlink != 0 {
			if !pathSafeUnderRoot(path, resolvedRoot, &res.Warnings) {
				return nil
			}
			target, err := os.Stat(path)
			if err != nil || !target.IsDir() {
				return nil
			}
			kind, _, _ := DetectContainer(path)
			if kind != ContainerNone {
				lr, err := LoadPluginContainer(path)
				if err != nil {
					res.Warnings = append(res.Warnings, LoadWarning{
						Path: path, Code: "container_load_failed", Reason: err.Error(),
					})
					return nil
				}
				res.Skills = append(res.Skills, lr.Skills...)
				res.Agents = append(res.Agents, lr.Agents...)
				res.Warnings = append(res.Warnings, lr.Warnings...)
			}
			return nil
		}

		if info.IsDir() {
			// Plugin container marker: hand off to LoadPluginContainer
			// and SkipDir so the outer walk doesn't double-process the
			// same SKILL.md / agent .md.
			kind, _, _ := DetectContainer(path)
			if kind != ContainerNone {
				lr, err := LoadPluginContainer(path)
				if err != nil {
					res.Warnings = append(res.Warnings, LoadWarning{
						Path: path, Code: "container_load_failed", Reason: err.Error(),
					})
					return filepath.SkipDir
				}
				res.Skills = append(res.Skills, lr.Skills...)
				res.Agents = append(res.Agents, lr.Agents...)
				res.Warnings = append(res.Warnings, lr.Warnings...)
				return filepath.SkipDir
			}
			return nil
		}

		// File handlers — only the root's "primary" content type is
		// loaded loose. Skills root walks SKILL.md; agents root walks
		// loose *.md (one persona per file).
		base := info.Name()
		if wantSkills {
			if base != "SKILL.md" {
				return nil
			}
			if !pathSafeUnderRoot(path, resolvedRoot, &res.Warnings) {
				return nil
			}
			sk, ws, err := ParseSkillMd(path)
			res.Warnings = append(res.Warnings, ws...)
			if err != nil {
				res.Warnings = append(res.Warnings, LoadWarning{
					Path: path, Code: "parse_failed", Reason: err.Error(),
				})
				return nil
			}
			sk.Dir = filepath.Dir(path)
			res.Skills = append(res.Skills, sk)
			return nil
		}

		// Agent root — *.md files are personas; README.md skipped.
		if !strings.EqualFold(filepath.Ext(base), ".md") {
			return nil
		}
		if strings.EqualFold(base, "README.md") {
			return nil
		}
		if !pathSafeUnderRoot(path, resolvedRoot, &res.Warnings) {
			return nil
		}
		ag, ws, err := ParseAgentMd(path)
		res.Warnings = append(res.Warnings, ws...)
		if err != nil {
			res.Warnings = append(res.Warnings, LoadWarning{
				Path: path, Code: "parse_failed", Reason: err.Error(),
			})
			return nil
		}
		ag.Dir = filepath.Dir(path)
		res.Agents = append(res.Agents, ag)
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return res, nil
}
