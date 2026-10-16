// Package chatruntime is the in-process orchestration layer for the ongrid
// AIOps agent. It owns the data shape of a "skill bundle" and an
// "agent persona": both are SKILL.md / *.md files with YAML
// frontmatter + markdown body. This file defines just the in-memory structs;
// parsing lives in skill_parser.go / agent_parser.go.
//
// PR-2 of / / scaffolding only — no graph, no LLM
// wiring, no tool execution. Old internal/skill stays running for compat.
//
// Field naming convention: every YAML / JSON tag uses snake_case per
// — the spec explicitly aligns ongrid with claude-code agent
// frontmatter, openclaw SKILL.md, and the SKILL.md body we ship to users.
package chatruntime

import (
	"encoding/json"
	"time"
)

// ToolClass categorizes a tool's blast radius. Policies filter tools by
// class so the LLM never sees tools that aren't allowed by the channel /
// agent profile. Mirrors sealsuite-agent/internal/chatruntime.ToolClass.
type ToolClass string

const (
	// ClassRead is read-only — no side effects on the system or device.
	ClassRead ToolClass = "read"
	// ClassWrite mutates state (creates / updates resources) — requires a
	// permissionMode >= mutating-with-confirm.
	ClassWrite ToolClass = "write"
	// ClassDestructive is irreversible (deletes, restarts, exec-arbitrary)
	// — requires dual-sign-required (SOP)
	ClassDestructive ToolClass = "destructive"
)

// Policy is the per-request capability gate. The HTTP layer / agent
// persona constructs a Policy; ChatRuntime feeds it into ToolRegistry.Build
// and SkillRegistry.Resolve. PR-2 only defines the data shape — the
// actual filtering logic lives in skill_registry.go (and a future
// tool_registry.go in PR-3).
type Policy struct {
	// AllowedClasses lists which ToolClass values are permitted.
	// ["*"] means unrestricted. Empty defaults to read-only (matches
	// sealsuite-agent semantics — see Allows()).
	AllowedClasses []string

	// ConfirmationRequired lists classes that need a two-phase commit
	// before execution. PR-2 defines the field but does not implement
	// the gate (reviewer flow lands in a later PR).
	ConfirmationRequired []string

	// MaxOutputChars caps the final reply length. 0 = no cap.
	MaxOutputChars int

	// MaxIterations bounds the ReAct loop. 0 = use agent default.
	MaxIterations int
}

// Allows reports whether class is permitted under this policy.
// Empty AllowedClasses defaults to read-only (safe default).
func (p Policy) Allows(class ToolClass) bool {
	if len(p.AllowedClasses) == 0 {
		return class == ClassRead
	}
	for _, c := range p.AllowedClasses {
		if c == "*" || c == string(class) {
			return true
		}
	}
	return false
}

// RequiresConfirmation reports whether class must pass a confirmation
// gate (permissionMode = mutating-with-confirm).
func (p Policy) RequiresConfirmation(class ToolClass) bool {
	for _, c := range p.ConfirmationRequired {
		if c == "*" || c == string(class) {
			return true
		}
	}
	return false
}

// Activation controls when a skill is mounted into the toolBag. See
// (metadata.ongrid.activation) and
type Activation struct {
	// Mode = "always" (default) | "keyword".
	// + sealsuite chatruntime semantics.
	Mode string `yaml:"mode" json:"mode"`

	// Keywords trigger activation when Mode == "keyword". Case-insensitive
	// substring match against the user query.
	Keywords []string `yaml:"keywords" json:"keywords"`
}

// Requires expresses host-level dependencies of a skill. Matches openclaw
// SKILL.md `metadata.requires` shape. ongrid reads this verbatim per
type Requires struct {
	// Bins lists native binaries the skill expects on PATH (e.g. ["find", "du"]).
	// They go into the sandbox process whitelist.
	Bins []string `yaml:"bins" json:"bins"`

	// Config lists settings keys that must be filled by an admin before
	// the skill can run (e.g. ["accountsPath"]). Lands in system_settings.
	Config []string `yaml:"config" json:"config"`
}

// OngridExt is the ongrid-private extension subtree under metadata.ongrid.
// — these fields are ignored by openclaw / claude-code, so
// the same SKILL.md file runs on multiple platforms.
type OngridExt struct {
	// Scope = "manager" (default) | "edge". Edge-scope skills run on the
	// edge agent via frontier tunnel (plugin runtime).
	Scope string `yaml:"scope" json:"scope"`

	// EdgeRuntime is a free-form hint for the edge plugin runtime
	// (e.g. "subprocess", "wasm"). Empty = subprocess default.
	EdgeRuntime string `yaml:"edge_runtime" json:"edge_runtime"`

	// EdgeCapabilities is the edge-side sandbox declaration — a list of
	// capability statements Kept as raw map slice so
	// future capability shapes don't break the parser.
	EdgeCapabilities []map[string]any `yaml:"edge_capabilities" json:"edge_capabilities"`

	// Activation overrides the skill-level Activation (one is canonical,
	// the other is for ergonomics). Loader prefers OngridExt.Activation
	// when both are set, with a warning.
	Activation Activation `yaml:"activation" json:"activation"`

	// MinOngridVersion gates loading on the running ongrid build (e.g.
	// ">=0.7.30"). Failed gate produces a warning, not an error, in PR-2.
	MinOngridVersion string `yaml:"min_ongrid_version" json:"min_ongrid_version"`
}

// SkillMetadata is the `metadata` frontmatter subtree common to both
// openclaw and claude-code SKILL.md files plus the ongrid extension.
type SkillMetadata struct {
	// OS gates loading on GOOS (— openclaw field). Empty = any.
	OS []string `yaml:"os" json:"os"`

	// Requires is the openclaw `metadata.requires` shape.
	Requires Requires `yaml:"requires" json:"requires"`

	// Ongrid is the ongrid-private extension subtree.
	Ongrid OngridExt `yaml:"ongrid" json:"ongrid"`
}

// ToolDecl is a single tool declared by a skill. PR-2 stores the
// declaration only — actual factory binding lives in PR-3.
type ToolDecl struct {
	// Name is the wire-level tool name the LLM sees.
	Name string `yaml:"name" json:"name"`

	// Impl is the registered factory key (e.g. "builtin:filetools.Find").
	Impl string `yaml:"impl" json:"impl"`

	// Class is the blast-radius classification — see ToolClass constants.
	Class ToolClass `yaml:"class" json:"class"`

	// Description is the LLM-facing summary.
	Description string `yaml:"description" json:"description"`

	// WhenToUse is the LLM-facing decision hint —
	// claude-code splits "what is it" (description) from "when to pick it"
	// (when_to_use). ongrid follows the same split at tool granularity.
	WhenToUse string `yaml:"when_to_use" json:"when_to_use"`

	// Confirmation = "required" | "" — flips the per-tool confirmation
	// gate even if the policy class would otherwise allow it.
	Confirmation string `yaml:"confirmation" json:"confirmation"`

	// TimeoutSeconds caps the per-call wall time. 0 = use runtime default.
	TimeoutSeconds int `yaml:"timeout_seconds" json:"timeout_seconds"`
}

// Provenance records where a Skill / Agent / Pack came from. Filled by
// the marketplace install path; empty for filesystem drops.
type Provenance struct {
	// Source = "fs" | "tgz" | "git" | "marketplace" | "" (unknown).
	Source string `yaml:"source" json:"source"`

	// SourceURL is the original location (git URL, tgz fetch URL,
	// marketplace slug). Diagnostic only.
	SourceURL string `yaml:"source_url" json:"source_url"`

	// InstalledAt is the wall time of install. Zero for built-ins.
	InstalledAt time.Time `yaml:"installed_at" json:"installed_at"`

	// ManifestSHA256 is hex of the SKILL.md / agent .md / plugin.json
	// content — used by signature verification.
	ManifestSHA256 string `yaml:"manifest_sha256" json:"manifest_sha256"`

	// SignatureState = "unsigned" | "valid" | "invalid" | "unknown".
	SignatureState string `yaml:"signature_state" json:"signature_state"`
}

// Skill is a parsed SKILL.md descriptor in memory. The on-disk format
// is YAML frontmatter + markdown body. The body is captured
// verbatim into PromptBody and gets prepended with `[能力: <name>]`
// during system-prompt assembly.
type Skill struct {
	// Name is the snake_case unique key. Required.
	Name string `yaml:"name" json:"name"`

	// Version is optional; "" if author didn't declare one.
	Version string `yaml:"version" json:"version"`

	// Description is the one-liner shown to the LLM at listing time.
	// Required.
	Description string `yaml:"description" json:"description"`

	// WhenToUse is the longer "when should the LLM pick this" text.
	// Recommended.
	WhenToUse string `yaml:"when_to_use" json:"when_to_use"`

	// PromptBody is the markdown body of SKILL.md verbatim (after H1
	// normalization ). Loaded outside frontmatter so
	// the YAML tag is `-`.
	PromptBody string `yaml:"-" json:"prompt_body"`

	// Activation is the top-level activation hint (matches sealsuite
	// shape). also allows it under metadata.ongrid; the
	// ongrid one wins when both are set.
	Activation Activation `yaml:"activation" json:"activation"`

	// ConfigSection is the legacy openclaw-style settings binding key
	// (e.g. for "config_section: feilian.openapi"). Kept for compat.
	ConfigSection string `yaml:"config_section" json:"config_section"`

	// Tools is the per-skill tool declaration list.
	Tools []ToolDecl `yaml:"tools" json:"tools"`

	// Metadata is the `metadata` subtree (os, requires, ongrid.*).
	Metadata SkillMetadata `yaml:"metadata" json:"metadata"`

	// Provenance is filled by the install path; empty for built-ins
	// and direct fs drops.
	Provenance Provenance `yaml:"-" json:"provenance"`

	// Dir is the absolute directory the skill was loaded from. Used
	// by factories that need to resolve relative paths (openapi.yaml,
	// domains/*.md, etc.).
	Dir string `yaml:"-" json:"dir"`

	// UnknownFields preserves any frontmatter keys we don't recognize.
	// explicitly calls for this so that openclaw / claude-code
	// adding new fields doesn't break ongrid loading. Map values are
	// kept as decoded YAML scalars / sequences / mappings.
	UnknownFields map[string]any `yaml:"-" json:"unknown_fields"`
}

// Agent is a parsed agent persona (frontmatter markdown).
// — same on-disk shape as SKILL.md but the body is the agent's system
// prompt rather than skill prose. Snake_case YAML tags
type Agent struct {
	// Name is the agent identifier used at spawn time. Required.
	Name string `yaml:"name" json:"name"`

	// Description is the human listing string. Required.
	Description string `yaml:"description" json:"description"`

	// WhenToUse is the coordinator's spawn-decision hint. Required —
	// calls this the strict field a coordinator reads.
	WhenToUse string `yaml:"when_to_use" json:"when_to_use"`

	// Tools is the explicit tool whitelist. Empty = inherit from policy.
	Tools []string `yaml:"tools" json:"tools"`

	// DisallowedTools is the blacklist (applied after whitelist). Black
	// wins over white
	DisallowedTools []string `yaml:"disallowed_tools" json:"disallowed_tools"`

	// PermissionMode = "read-only" | "mutating-with-confirm" | "dual-sign-required".
	PermissionMode string `yaml:"permission_mode" json:"permission_mode"`

	// MaxTurns caps the worker's internal ReAct loop.
	MaxTurns int `yaml:"max_turns" json:"max_turns"`

	// Model is the LLM identifier (e.g. "anthropic/claude-sonnet-4-6").
	// Empty = inherit coordinator default.
	Model string `yaml:"model" json:"model"`

	// CriticalReminder is the system-reminder block injected on every
	// turn. — anti-drift mechanism.
	CriticalReminder string `yaml:"critical_reminder" json:"critical_reminder"`

	// InitialPrompt is prepended to the first user message at spawn.
	InitialPrompt string `yaml:"initial_prompt" json:"initial_prompt"`

	// Background = true forces async execution (long-running workers).
	Background bool `yaml:"background" json:"background"`

	// OmitClaudeMd skips inheriting the global system context. Used
	// for tightly-scoped reviewer agents.
	OmitClaudeMd bool `yaml:"omit_claude_md" json:"omit_claude_md"`

	// SystemPrompt is the markdown body — the actual system prompt
	// the worker LLM sees. Loaded outside frontmatter.
	SystemPrompt string `yaml:"-" json:"system_prompt"`

	// Source tells the API/UI where this persona came from:
	//   "builtin" — shipped in the binary (programmatic Add)
	//   "disk" — loaded from agents/*.md
	//   "user" — created by the user via /v1/agents/custom (Phase 3)
	// "user" agents are editable + deletable from the UI; the others
	// stay read-only.
	Source string `yaml:"-" json:"source,omitempty"`

	// Metadata mirrors the SKILL.md metadata subtree — agents may
	// declare os / requires / ongrid extensions too.
	Metadata SkillMetadata `yaml:"metadata" json:"metadata"`

	// Provenance fills on install.
	Provenance Provenance `yaml:"-" json:"provenance"`

	// Dir is the directory of the .md file.
	Dir string `yaml:"-" json:"dir"`

	// UnknownFields preserves unrecognized frontmatter keys.
	UnknownFields map[string]any `yaml:"-" json:"unknown_fields"`
}

// Pack is a parsed plugin container — a directory shipping zero or more
// skills + agents + commands together. ongrid recognizes both
// claude-code's `.claude-plugin/plugin.json` and openclaw's
// `openclaw.plugin.json`. PR-2 only parses the manifest
// metadata; recursive load of skills/ agents/ commands/ subdirs is a
// later PR (TODO in plugin_container.go).
type Pack struct {
	// ID is the pack key (e.g. "acme-tools"). Required.
	ID string `yaml:"id" json:"id"`

	// DisplayName is the human-friendly title.
	DisplayName string `yaml:"name" json:"display_name"`

	// Version follows semver (e.g. "1.2.3").
	Version string `yaml:"version" json:"version"`

	// Description is a one-liner.
	Description string `yaml:"description" json:"description"`

	// ConfigSchema is a JSON Schema describing tenant-level config that
	// admins fill in via the SPA install dialog. Optional.
	ConfigSchema json.RawMessage `yaml:"config_schema" json:"config_schema"`

	// UIMetadata holds platform-specific extras the SPA may show. We
	// stuff openclaw legacy fields under UIMetadata["openclaw_legacy"]
	// so future SPA can render them as-is.
	UIMetadata map[string]any `yaml:"ui_metadata" json:"ui_metadata"`

	// Source is the install source ("fs" | "tgz" | "git" | "marketplace").
	Source string `yaml:"source" json:"source"`

	// ManifestSHA256 is hex of the manifest content.
	ManifestSHA256 string `yaml:"manifest_sha256" json:"manifest_sha256"`

	// SignatureState = "unsigned" | "valid" | "invalid" | "unknown".
	SignatureState string `yaml:"signature_state" json:"signature_state"`

	// Dir is the absolute pack root.
	Dir string `yaml:"-" json:"dir"`
}

// LoadResult is what plugin_container / registry loaders return after a
// directory walk: the discovered Pack (if any), the skills + agents
// found inside, and accumulated warnings (one per skipped / normalized
// entry).
type LoadResult struct {
	Pack     *Pack         `json:"pack"`
	Skills   []*Skill      `json:"skills"`
	Agents   []*Agent      `json:"agents"`
	Warnings []LoadWarning `json:"warnings"`
}

// LoadWarning is a non-fatal load issue. Code is a stable identifier
// (e.g. "name_normalized", "missing_when_to_use") so SPA / tests can
// filter without scraping Reason text.
type LoadWarning struct {
	// Path is the file the warning applies to (absolute or relative
	// to the loader's root — loader convention).
	Path string `json:"path"`

	// Reason is human-readable English text.
	Reason string `json:"reason"`

	// Code is the stable identifier.
	Code string `json:"code"`
}
