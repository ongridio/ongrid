package marketplace

import (
	"context"

	model "github.com/ongridio/ongrid/internal/manager/model/marketplace"
)

// Repo is the persistence surface the usecase depends on. Concrete
// implementation lives in data/marketplace/store.
type Repo interface {
	Create(ctx context.Context, p *model.InstalledPack) error
	GetByPackID(ctx context.Context, tenantID uint64, packID string) (*model.InstalledPack, error)
	GetByManifestSHA(ctx context.Context, tenantID uint64, sha string) (*model.InstalledPack, error)
	List(ctx context.Context, tenantID uint64) ([]*model.InstalledPack, error)
	DeleteSoft(ctx context.Context, tenantID uint64, packID string) error
	SetBindings(ctx context.Context, tenantID uint64, packID, bindingsJSON string) error
}

// SkillRegistry is the narrow surface the usecase uses to hot-reload
// skills after install / uninstall. *chatruntime.SkillRegistry
// satisfies this structurally.
//
// Reload(skillsRoot) MUST be atomic (build new slice, then swap under
// a write lock) so an in-flight chat that has already called
// Resolve() observes a stable snapshot. In-process channels and the
// existing chatruntime.Skill values are unaffected by Reload — only
// the registry's internal slice flips.
type SkillRegistry interface {
	Reload(skillsRoot string, extras ...string) error
}

// AgentRegistry mirrors SkillRegistry for agent personas.
// *chatruntime.AgentRegistry satisfies this structurally.
type AgentRegistry interface {
	Reload(agentsRoot string, extras ...string) error
}

// CapabilityDeclaration is the user-approved capability snapshot
// stored as JSON in installed_skills.capabilities_json. The shape is
// designed for the SPA install-confirm dialog (UI) so
// every field has a one-to-one render target.
//
// Wire shape:
//
//	{
//	  "pack_id": "etcd-troubleshoot",
//	  "version": "0.4.2",
//	  "skills": [
//	    {
//	      "name": "etcd_health",
//	      "scope": "manager" | "edge",
//	      "edge_capabilities": [ {...}, {...} ],
//	      "requires": {
//	        "bins": ["etcdctl"],
//	        "config": ["etcd_endpoint"]
//	      },
//	      "tool_classes": ["read", "write"]
//	    }, ...
//	  ],
//	  "agent_count": 3,
//	  "summary": {
//	    "tool_classes": ["read", "write", "destructive"],
//	    "bins": ["etcdctl", "kubectl"],
//	    "config_keys": ["etcd_endpoint"]
//	  }
//	}
//
// summary is the deduped union across all skills — the dialog renders
// it as the bullet list ("• 网络访问: ..." / "• 文件读取: ..." /
// "• 可执行二进制: etcdctl") in UI.
type CapabilityDeclaration struct {
	PackID  string                  `json:"pack_id"`
	Version string                  `json:"version"`
	Skills  []SkillCapabilityRecord `json:"skills"`

	// AgentCount is the number of agent personas the pack ships.
	// Agents have no capability declaration today — they're just
	// system prompts — so they're surfaced as a count for the dialog.
	AgentCount int `json:"agent_count"`

	// Summary is the deduped union across Skills. Built by the
	// usecase, serialised verbatim to the SPA.
	Summary CapabilitySummary `json:"summary"`
}

// SkillCapabilityRecord is one row in CapabilityDeclaration.Skills.
// Mirrors the chatruntime.Skill metadata shape but normalised for
// the SPA renderer (no nested unknown_fields, tool classes already
// deduped to a string slice).
type SkillCapabilityRecord struct {
	Name             string           `json:"name"`
	Scope            string           `json:"scope"`
	EdgeCapabilities []map[string]any `json:"edge_capabilities,omitempty"`
	Requires         RequiresRecord   `json:"requires"`
	ToolClasses      []string         `json:"tool_classes"`
}

// RequiresRecord mirrors chatruntime.Requires but with empty slices
// elided in JSON for smaller wire payloads.
type RequiresRecord struct {
	Bins        []string               `json:"bins,omitempty"`
	Config      []string               `json:"config,omitempty"`
	Credentials []CredentialSlotRecord `json:"credentials,omitempty"`
}

// CredentialSlotRecord is one credential slot a skill declares
// (chatruntime.CredentialRequirement), normalised for the binding UI. The
// inject template is intentionally dropped — the operator only needs the
// slot key, a label, and the expected field names to pick which stored
// credential fills the slot; injection itself is resolved server-side at
// exec time from the bound credential's TYPE.
type CredentialSlotRecord struct {
	Slot   string   `json:"slot"`
	Label  string   `json:"label"`
	Fields []string `json:"fields,omitempty"`
}

// CapabilitySummary is the deduped union of every Skill's
// declared requirements. Used by the install-confirm dialog as the
// single bullet list.
type CapabilitySummary struct {
	ToolClasses []string `json:"tool_classes,omitempty"`
	Bins        []string `json:"bins,omitempty"`
	ConfigKeys  []string `json:"config_keys,omitempty"`
	// CredentialSlots is the deduped set of credential slots across all
	// skills — the binding dialog renders one "pick a credential" row per
	// slot here.
	CredentialSlots []CredentialSlotRecord `json:"credential_slots,omitempty"`
}

// InstallResult is what Install returns to the HTTP handler. The SPA
// wants the freshly-installed pack metadata + the warnings the loader
// surfaced (so the user can spot issues before clicking around).
type InstallResult struct {
	Pack         *model.InstalledPack  `json:"pack"`
	Capabilities CapabilityDeclaration `json:"capabilities"`
	Warnings     []LoadWarning         `json:"warnings"`
}

// LoadWarning mirrors chatruntime.LoadWarning so we don't leak that
// import out of biz/marketplace; the JSON shape is identical.
type LoadWarning struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Code   string `json:"code"`
}
