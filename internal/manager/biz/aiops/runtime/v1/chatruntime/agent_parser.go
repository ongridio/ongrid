package chatruntime

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ParseAgentMd reads an agent persona file (agents/<name>.md) at path.
// Same on-disk shape as SKILL.md but the body becomes the agent's
// system prompt. — required fields are name, description,
// when_to_use; the latter is strict per the spec because a coordinator
// can't decide whether to spawn an agent without it.
//
// Snake_case field names (this is the explicit
// rename of the original camelCase shape).
//
// Forward-compat: unknown frontmatter keys are preserved into
// Agent.UnknownFields so claude-code adding new persona fields (effort,
// memory, isolation, ...) doesn't break loading.
func ParseAgentMd(path string) (*Agent, []LoadWarning, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: read %s: %w", path, err)
	}
	frontmatter, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: %s: %w", path, err)
	}

	var warnings []LoadWarning

	var ag Agent
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &ag); err != nil {
			return nil, nil, fmt.Errorf("chatruntime: parse %s frontmatter: %w", path, err)
		}
	}

	rawMap := map[string]any{}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &rawMap); err != nil {
			return nil, nil, fmt.Errorf("chatruntime: parse %s frontmatter (raw): %w", path, err)
		}
	}
	ag.UnknownFields = retainUnknownAgentFields(rawMap)

	// Required fields — strict. Missing when_to_use is an
	// error not a warning: a coordinator can't choose this agent without
	// it.
	if strings.TrimSpace(ag.Name) == "" {
		return nil, warnings, fmt.Errorf("chatruntime: %s: frontmatter missing required field 'name'", path)
	}
	if strings.TrimSpace(ag.Description) == "" {
		return nil, warnings, fmt.Errorf("chatruntime: %s: frontmatter missing required field 'description'", path)
	}
	if strings.TrimSpace(ag.WhenToUse) == "" {
		return nil, warnings, fmt.Errorf("chatruntime: %s: frontmatter missing required field 'when_to_use'", path)
	}

	// Snake-case enforcement on name. We don't auto-normalize agent names
	// (they're spawn keys; rewriting silently would break references).
	if !skillNameRe.MatchString(ag.Name) {
		// Allow dash for agents — claude-code uses `incident-investigator`
		// style. Be permissive: warn rather than rewrite.
		warnings = append(warnings, LoadWarning{
			Path:   path,
			Code:   "name_non_snake",
			Reason: fmt.Sprintf("agent name %q is not snake_case (recommends underscores)", ag.Name),
		})
	}

	ag.SystemPrompt = strings.TrimRight(string(body), "\n")
	return &ag, warnings, nil
}

// retainUnknownAgentFields keeps frontmatter keys we don't model on
// Agent. Drives forward compatibility with claude-code agent schema
// additions (effort, isolation, mcp_servers, hooks, ...).
func retainUnknownAgentFields(raw map[string]any) map[string]any {
	known := map[string]struct{}{
		"name":              {},
		"description":       {},
		"when_to_use":       {},
		"tools":             {},
		"disallowed_tools":  {},
		"permission_mode":   {},
		"max_turns":         {},
		"model":             {},
		"critical_reminder": {},
		"initial_prompt":    {},
		"background":        {},
		"omit_claude_md":    {},
		"metadata":          {},
	}
	out := map[string]any{}
	for k, v := range raw {
		if _, ok := known[k]; ok {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
