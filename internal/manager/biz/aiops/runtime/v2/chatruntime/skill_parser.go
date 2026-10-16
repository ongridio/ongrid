package chatruntime

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterDelimiter is the YAML frontmatter fence used by SKILL.md /
// agents/*.md and every other Markdown frontmatter dialect.
const frontmatterDelimiter = "---"

// skillNameRe is the snake_case validator. says the name is
// lowercase + digits + underscore; this matches what openclaw enforces too.
var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

// nonSnakeRe matches characters that should be normalized to underscore
// in a non-conforming name (dashes, dots, spaces).
var nonSnakeRe = regexp.MustCompile(`[^a-z0-9_]+`)

// h1Re matches the leading `#`-style markdown H1 header.
var h1Re = regexp.MustCompile(`^#\s+.+\s*$`)

// ParseSkillMd reads a SKILL.md file at path and returns the parsed
// Skill plus any non-fatal warnings. Required fields
// are name + description; missing them produces an error rather than a
// warning (a SKILL.md without a description is unusable).
//
// Behavioral notes:
//   - Frontmatter is preserved into Skill.UnknownFields for any keys we
//     don't model — explicitly requires this so upstream
//     schema additions don't break ongrid loading.
//   - The body H1 is auto-normalized to `[能力: <name>]`
//   - name is snake_case validated; non-conforming names are normalized
//     and a warning with code "name_normalized" is emitted.
//
// The returned Skill.Dir is the directory containing path. PromptBody is
// the markdown body verbatim (after H1 normalization). Tag-less fields
// (Provenance, Dir) stay zero-valued — the install path fills them.
func ParseSkillMd(path string) (*Skill, []LoadWarning, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: read %s: %w", path, err)
	}
	frontmatter, body, err := splitFrontmatter(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("chatruntime: %s: %w", path, err)
	}

	var warnings []LoadWarning

	// Decode into the typed struct first, then again into a map so we
	// can compute "unknown" keys for forward compatibility.
	var sk Skill
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &sk); err != nil {
			return nil, nil, fmt.Errorf("chatruntime: parse %s frontmatter: %w", path, err)
		}
	}

	rawMap := map[string]any{}
	if len(frontmatter) > 0 {
		if err := yaml.Unmarshal(frontmatter, &rawMap); err != nil {
			return nil, nil, fmt.Errorf("chatruntime: parse %s frontmatter (raw): %w", path, err)
		}
	}
	sk.UnknownFields = retainUnknownSkillFields(rawMap)

	// Required fields —
	if strings.TrimSpace(sk.Name) == "" {
		return nil, warnings, fmt.Errorf("chatruntime: %s: frontmatter missing required field 'name'", path)
	}
	if strings.TrimSpace(sk.Description) == "" {
		return nil, warnings, fmt.Errorf("chatruntime: %s: frontmatter missing required field 'description'", path)
	}

	// Snake-case enforcement with auto-normalization.
	if !skillNameRe.MatchString(sk.Name) {
		original := sk.Name
		sk.Name = normalizeSnakeName(sk.Name)
		warnings = append(warnings, LoadWarning{
			Path:   path,
			Code:   "name_normalized",
			Reason: fmt.Sprintf("name %q is not snake_case; normalized to %q", original, sk.Name),
		})
	}

	// Body: normalize the leading H1 to [能力: <name>]
	sk.PromptBody = normalizeSkillBodyH1(body, sk.Name)

	// Reconcile metadata.ongrid.activation vs top-level activation.
	// — both are accepted; ongrid one wins on conflict, with
	// a warning so the author knows.
	if sk.Metadata.Ongrid.Activation.Mode != "" {
		if sk.Activation.Mode != "" && sk.Metadata.Ongrid.Activation.Mode != sk.Activation.Mode {
			warnings = append(warnings, LoadWarning{
				Path:   path,
				Code:   "activation_conflict",
				Reason: "both top-level activation and metadata.ongrid.activation are set; using metadata.ongrid.activation",
			})
		}
		sk.Activation = sk.Metadata.Ongrid.Activation
	}

	return &sk, warnings, nil
}

// splitFrontmatter splits a SKILL.md / agent .md byte slice into
// (frontmatterYAML, bodyMarkdown). When no frontmatter is present (no
// leading `---` line) it returns frontmatter=nil and body=full input.
//
// A malformed frontmatter (opening `---` but no closing `---`) is an error.
func splitFrontmatter(raw []byte) ([]byte, []byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	// Peek the first non-empty line. If it isn't `---`, treat the whole
	// thing as body (no frontmatter).
	var firstLine string
	var leading []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			leading = append(leading, line)
			continue
		}
		firstLine = line
		break
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan: %w", err)
	}
	if strings.TrimRight(firstLine, " \t") != frontmatterDelimiter {
		// No frontmatter — return the entire raw as body.
		return nil, raw, nil
	}

	var fmLines []string
	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimRight(line, " \t") == frontmatterDelimiter {
			closed = true
			break
		}
		fmLines = append(fmLines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan: %w", err)
	}
	if !closed {
		return nil, nil, fmt.Errorf("frontmatter started with --- but never closed")
	}

	var bodyLines []string
	for scanner.Scan() {
		bodyLines = append(bodyLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan: %w", err)
	}

	frontmatter := []byte(strings.Join(fmLines, "\n"))
	body := []byte(strings.Join(bodyLines, "\n"))
	_ = leading
	return frontmatter, body, nil
}

// normalizeSkillBodyH1 rewrites the leading H1 (`# Title`) to
// `[能力: <name>]` If there's no H1, prepends one.
func normalizeSkillBodyH1(body []byte, name string) string {
	const tagPrefix = "[能力: "
	header := tagPrefix + name + "]"

	text := string(body)
	if strings.TrimSpace(text) == "" {
		return header
	}

	lines := strings.Split(text, "\n")
	// Skip leading blanks to find the first content line; we drop those
	// leading blanks so the canonical header is always the first byte.
	idx := 0
	for idx < len(lines) && strings.TrimSpace(lines[idx]) == "" {
		idx++
	}
	if idx >= len(lines) {
		return header
	}
	lines = lines[idx:]
	first := lines[0]
	if h1Re.MatchString(strings.TrimSpace(first)) {
		// Replace the H1 with the canonical tag.
		lines[0] = header
	} else if strings.HasPrefix(strings.TrimSpace(first), tagPrefix) {
		// Already normalized — leave as is.
	} else {
		// Prepend the canonical tag as a new first line.
		newLines := []string{header, ""}
		newLines = append(newLines, lines...)
		lines = newLines
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// normalizeSnakeName converts dashes / dots / spaces / etc. to underscores
// and lowercases the result. Used when the author wrote `hello-world` etc.
func normalizeSnakeName(in string) string {
	out := strings.ToLower(strings.TrimSpace(in))
	out = nonSnakeRe.ReplaceAllString(out, "_")
	out = strings.Trim(out, "_")
	if out == "" {
		out = "unnamed"
	}
	return out
}

// retainUnknownSkillFields returns a map of frontmatter keys that we
// don't model on the Skill struct. wants these preserved so
// upstream openclaw / claude-code schema additions don't break loading.
func retainUnknownSkillFields(raw map[string]any) map[string]any {
	known := map[string]struct{}{
		"name":           {},
		"version":        {},
		"description":    {},
		"when_to_use":    {},
		"activation":     {},
		"config_section": {},
		"tools":          {},
		"metadata":       {},
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
