package chatruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// commandFrontmatter is the parsed YAML frontmatter of a claude/cursor
// `commands/<name>.md` file. We model only the fields
// asks for; everything else lands in the prompt body unchanged.
//
// `allowed-tools` (claude-specific, kebab-cased — claude-code uses
// kebab-case in commands frontmatter, unlike SKILL.md / agents) is
// captured purely so we can echo it into the prompt body as a soft
// hint. ongrid does NOT enforce it as a hard restriction, since claude
// command tools (Bash / Edit / ...) don't map 1:1 to ongrid tool
// classes.
type commandFrontmatter struct {
	Description  string   `yaml:"description"`
	AllowedTools []string `yaml:"allowed-tools"`
}

// ConvertCommandFile parses a claude/cursor `commands/<name>.md` file
// at path and returns a Skill with mode=keyword. :
//
//   - keyword = ["/<name>", "<name>"] (file basename, optional snake-cased
//     alias when the basename contains dashes)
//   - frontmatter description → Skill.Description
//   - body → Skill.PromptBody (with `[能力: cmd_<name>]` header)
//   - tools: [] (commands are pure prompt injection — no new tools wired
//     by ongrid; the claude-side tool restriction is informational)
//   - name prefixed with `cmd_` to avoid collision with regular skills
//   - frontmatter `allowed-tools: [Bash, Edit]` (claude-specific) → appended
//     to the prompt body as a soft hint, NOT enforced.
//
// The basename rule mirrors how claude-code resolves slash commands:
// `commands/foo.md` → `/foo`. Nested subdirs are flattened
// (`commands/git/commit.md` → `/commit`); the loader (LoadPluginContainer)
// is responsible for walking commands/ recursively.
//
// Returns the parsed Skill, any non-fatal warnings, and a parse error
// when the file is unreadable / not markdown / missing required fields.
func ConvertCommandFile(path string) (*Skill, []LoadWarning, error) {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return nil, nil, fmt.Errorf("chatruntime: %s: command file must end in .md", path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: read %s: %w", path, err)
	}

	frontmatter, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: %s: %w", path, err)
	}

	var fm commandFrontmatter
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &fm); err != nil {
			return nil, nil, fmt.Errorf("chatruntime: parse %s frontmatter: %w", path, err)
		}
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = strings.TrimSpace(base)
	if base == "" {
		return nil, nil, fmt.Errorf("chatruntime: %s: command file has empty basename", path)
	}

	// Build keyword list
	// Always include "/<name>" and "<name>"; if the basename contains
	// dashes, also include the snake_cased alias so users typing either
	// "review-pr" or "review_pr" trigger activation.
	keywords := []string{"/" + base, base}
	if strings.ContainsAny(base, "-. ") {
		alias := normalizeSnakeName(base)
		if alias != "" && alias != base {
			keywords = append(keywords, alias)
		}
	}

	// Skill name uses cmd_ prefix to avoid colliding with regular skills.
	// If the basename isn't snake_case, normalize after the prefix so the
	// final name is always valid.
	nameSuffix := base
	if !skillNameRe.MatchString(nameSuffix) {
		nameSuffix = normalizeSnakeName(nameSuffix)
	}
	skillName := "cmd_" + nameSuffix

	description := strings.TrimSpace(fm.Description)
	if description == "" {
		description = fmt.Sprintf("Slash-command equivalent of `/%s` (claude commands import).", base)
	}

	// Compose the prompt body. The H1 is the canonical [能力: cmd_<name>]
	// tag so system_prompt assembly picks it up as-is.
	bodyText := strings.TrimRight(string(body), "\n")
	header := "[能力: " + skillName + "]"

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	if bodyText != "" {
		b.WriteString(bodyText)
		b.WriteString("\n")
	}
	if len(fm.AllowedTools) > 0 {
		// Soft hint, not a hard restriction. We surface the upstream
		// allowed-tools list verbatim so the LLM can take it as a clue
		// about the author's intent.
		b.WriteString("\n")
		b.WriteString(fmt.Sprintf("> 上游 (claude) 允许使用的工具: %s。ongrid 不强制此清单，仅作提示。\n",
			strings.Join(fm.AllowedTools, ", ")))
	}

	sk := &Skill{
		Name:        skillName,
		Description: description,
		Activation: Activation{
			Mode:     "keyword",
			Keywords: keywords,
		},
		PromptBody: strings.TrimRight(b.String(), "\n"),
		Metadata: SkillMetadata{
			Ongrid: OngridExt{
				Activation: Activation{
					Mode:     "keyword",
					Keywords: keywords,
				},
			},
		},
		Dir: filepath.Dir(path),
	}

	var warnings []LoadWarning
	if strings.TrimSpace(fm.Description) == "" {
		warnings = append(warnings, LoadWarning{
			Path:   path,
			Code:   "command_missing_description",
			Reason: fmt.Sprintf("command %q has no frontmatter description; using a generated default", base),
		})
	}

	return sk, warnings, nil
}
