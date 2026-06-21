// Package secret holds the persistence entity for the credential vault
// (HLD-017). Following the n8n model, a stored credential is a NAMED,
// multi-FIELD instance (e.g. "tencent-prod" → {secret_id, secret_key,
// region}), encrypted at rest. The consuming skill / external MCP server
// declares in its manifest WHERE each field is injected (env var / file via
// {{field}} placeholders); a per-skill binding picks WHICH instance fills
// the slot. ongrid never hard-codes a credential's semantics — the manifest
// owns the injection mapping, the binding owns the choice.
//
// At-rest: the field map is JSON-encoded then sealed by pkg/secretbox
// (AES-256-GCM, key from ONGRID_SECRET_KEY) before it touches the DB, so a
// DB dump alone never yields plaintext (cf. n8n's encryption posture).
package secret

import "time"

// Secret is one named credential instance — a bag of fields stored as a
// single encrypted blob.
type Secret struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// Name is the unique human label the binding layer references
	// (e.g. "tencent-prod", "github-bot"). NOT an env var name anymore —
	// the manifest maps this instance's fields to env vars.
	Name string `gorm:"size:128;not null;uniqueIndex"`

	// Type is the credential TYPE name (biz/secret.CredType, e.g.
	// "tencentcloud" / "aws" / "custom"). Drives the create-form fields and
	// the default inject rule. Empty → treated as "custom".
	Type string `gorm:"size:64"`

	// Data is the AES-GCM-sealed JSON object of the credential's fields
	// (map[string]string). Reuses the original `value` column so no schema
	// churn from the single-value era. NEVER returned by a read API.
	Data string `gorm:"column:value;type:text;not null"`

	// Description is an optional human note.
	Description string `gorm:"size:512"`

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

// TableName pins the schema name across future package renames.
func (Secret) TableName() string { return "secrets" }
