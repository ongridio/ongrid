// Package mcp holds the persistence entity for external MCP (Model Context
// Protocol) server registrations (HLD-018). Each row is one upstream MCP
// server ongrid connects to as a *client*: its transport + endpoint (or
// stdio command), an optional credential-vault reference + header template
// for auth injection, a trust/enable flag, and a cached snapshot of the
// tools the server advertised on the last successful probe.
//
// Following the InstalledPack convention, the model fields carry NO json
// tags — they serialize as PascalCase and the frontend remaps. Sensitive
// auth never lives here in plaintext: only the credential NAME is stored;
// biz/mcp resolves it against the vault at connect time.
package mcp

import "time"

// Server is one registered external MCP server. Name is the tool prefix
// applied to every tool this server exposes, so it must be unique.
type Server struct {
	ID uint64 `gorm:"primaryKey;autoIncrement"`

	// Name is the unique label and tool-name prefix (e.g. "github" →
	// "github__create_issue").
	Name string `gorm:"size:64;not null;uniqueIndex"`

	// Transport selects the wire: "http" (Streamable HTTP) or "stdio"
	// (subprocess; not supported this phase).
	Transport string `gorm:"size:16;not null;default:http"`

	// Endpoint is the HTTP MCP URL (transport=http).
	Endpoint string `gorm:"size:512"`

	// Command + ArgsJSON describe the stdio subprocess (transport=stdio;
	// may be empty this phase). ArgsJSON is a JSON array of strings.
	Command  string `gorm:"size:512"`
	ArgsJSON string `gorm:"type:text"`

	// Credential is the credential-vault NAME whose fields fill the
	// HeaderTemplateJSON placeholders. Empty → no auth injection.
	Credential string `gorm:"size:128"`

	// HeaderTemplateJSON is a JSON map[string]string of HTTP headers with
	// {{field}} placeholders resolved from the credential's fields, e.g.
	// {"Authorization":"Bearer {{token}}"}.
	HeaderTemplateJSON string `gorm:"type:text"`

	// Trusted skips the human-approval gate on tool calls (HLD-018);
	// Enabled toggles the server in/out of the live toolbag.
	Trusted bool `gorm:"not null;default:false"`
	Enabled bool `gorm:"not null;default:true"`

	// ToolsCacheJSON is the JSON-encoded []mcpclient.Tool snapshot from the
	// last successful probe. Status/LastError record that probe's outcome.
	ToolsCacheJSON string `gorm:"type:longtext"`
	Status         string `gorm:"size:16"`
	LastError      string `gorm:"size:512"`

	CreatedBy uint64    `gorm:"not null;default:0"`
	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

// TableName pins the schema name across future package renames.
func (Server) TableName() string { return "mcp_servers" }
