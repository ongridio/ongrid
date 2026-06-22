// Package marketplace holds the persistence entity for the
// installed_skills lock table — /.
//
// One row per installed pack (pack = a directory shipping zero or more
// skills + agents + commands together). The `manifest_sha256` column
// is the disk-content lock used by both the install path (uniqueness
// per tenant) and the eventual boot-time verifier (mismatch → mark
// pack broken, do not load).
//
// Scope: single-tenant MVP. The `tenant_id` column is sized
// for a future multi-tenant world; today everything writes
// tenant_id=0 (or 1 — see biz/marketplace.Caller for the canonical
// resolution) and the unique index keeps install idempotent per
// (tenant, pack) pair.
package marketplace

import (
	"time"

	"gorm.io/plugin/soft_delete"
)

// InstalledPack is one row of the installed_skills table — a single
// installed pack on the manager (skill bundle / claude plugin /
// openclaw plugin).
//
// CapabilitiesJSON stores the merged capability declaration the user
// approved at install time (see biz/marketplace.CapabilityDeclaration
// for the in-memory shape). Persisted as a JSON-encoded string so the
// schema can grow without migrations.
type InstalledPack struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// TenantID is the owning user namespace. Single-tenant deployments
	// keep this at 0; the column exists so multi-tenancy doesn't need
	// a schema change later.
	TenantID uint64 `gorm:"not null;default:0;index;uniqueIndex:idx_tenant_pack,priority:1"`

	// PackID is the pack key (e.g. "etcd-troubleshoot"). Unique per
	// tenant — a reinstall must go through Uninstall first.
	PackID string `gorm:"size:128;not null;uniqueIndex:idx_tenant_pack,priority:2"`

	// DisplayName is the human-friendly title. Filled from the pack's
	// manifest `name` field.
	DisplayName string `gorm:"size:255"`

	// Version follows semver (e.g. "0.4.2"). Empty when the pack
	// didn't declare one.
	Version string `gorm:"size:64"`

	// Source is the install path family — see biz/marketplace.Source.Type:
	//   - "ongrid-builtin" shipped in the manager binary tree
	//   - "local" copied from a host path
	//   - "git" git clone --depth=1
	//   - "tarball" curl + tar -xz
	//   - "<registry-name>" proxied through a configured registry
	Source string `gorm:"size:64"`

	// SourceURL is the original location for diagnostics — the path
	// passed to install for "local", the URL for git/tarball, and
	// "<registry>:<pack_id>@<version>" for registry installs.
	SourceURL string `gorm:"size:512"`

	// InstallPath is the absolute directory the pack lives in on
	// disk. Uninstall rm -rfs this path (after a safety check that it
	// sits under cfg.TenantSkillsRoot / cfg.SystemSkillsRoot).
	InstallPath string `gorm:"size:512"`

	// ManifestSHA256 is hex of the manifest content (plugin.json
	// or openclaw.plugin.json). Used for:
	//   - duplicate detection at install time (same hash already
	//     installed for this tenant → reject "use Update")
	//   - boot-time verification (mismatch →
	//     mark broken, skip load)
	ManifestSHA256 string `gorm:"size:64"`

	// SignatureState mirrors — one of:
	//   - "verified" (cosign-verified; not implemented yet)
	//   - "unsigned" (no signature; default for v1 stub)
	//   - "failed" (signature present but verification failed)
	SignatureState string `gorm:"size:32"`

	// CapabilitiesJSON is the user-approved capability declaration:
	// merged edge_capabilities + requires + tool classes from every
	// skill in the pack. JSON-encoded biz/marketplace.CapabilityDeclaration.
	CapabilitiesJSON string `gorm:"type:text"`

	// BindingsJSON maps each credential SLOT a pack's skills declare
	// (requires.credentials[].slot) to the NAME of a stored vault credential
	// — the operator's "which credential fills this slot" choice (HLD-017).
	// JSON object {slot: credential_name}; empty until bound. At skill exec
	// ongrid resolves the slot's declared inject mapping against the bound
	// credential's fields.
	BindingsJSON string `gorm:"type:text"`

	// InstalledBy is the user_id (tenantctx.Tenant.UserID) who ran the install.
	InstalledBy uint64 `gorm:"not null;default:0"`

	// InstalledAt / UpdatedAt are wall times of the install /
	// most-recent metadata refresh.
	InstalledAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime"`

	// DeletedAt is the audit timestamp for uninstall. DeleteMarker is the
	// database constraint marker: active rows keep marker=0, deleted rows get
	// a non-zero marker so the same (tenant, pack) can be reinstalled later
	// without weakening active-row uniqueness.
	DeletedAt    *time.Time            `gorm:"column:deleted_at;index"`
	DeleteMarker soft_delete.DeletedAt `gorm:"column:delete_marker;not null;default:0;softDelete:milli,DeletedAtField:DeletedAt;uniqueIndex:idx_tenant_pack,priority:3"`
}

// TableName pins the table name so future package renames don't
// accidentally create a new schema.
func (InstalledPack) TableName() string { return "installed_skills" }

// SignatureState constants —
const (
	SigStateVerified = "verified"
	SigStateUnsigned = "unsigned"
	SigStateFailed   = "failed"
)
