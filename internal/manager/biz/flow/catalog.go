// catalog.go — the tool-node palette source. The flow engine's `tool`
// node can dispatch ANY registered BaseTool by name; this catalog
// surfaces that universe to the canvas so every tool becomes a
// drag-and-drop, form-driven node instead of a hand-typed tool name.
//
// The catalog is read-only metadata (name / description / when-to-use /
// class / category / JSON-Schema params). It is produced in
// cmd/ongrid/main.go over the live tools.Registry, so biz/flow stays
// free of the aiops/tools import — same seam pattern as AgentRunner /
// ToolInvoker / Notifier.
package flow

import "encoding/json"

// ToolMeta describes one BaseTool for the node palette.
type ToolMeta struct {
	Name          string          // wire name (goes into tool node config.tool)
	DisplayZh     string          // Chinese display label (falls back to Name)
	Description   string          // one-line "what it does" (English)
	DescriptionZh string          // Chinese one-liner (falls back to Description)
	WhenToUse     string          // disambiguation hint
	Class         string          // read / write / destructive
	Category      string          // UI grouping (topology/host/observability/…)
	Parameters    json.RawMessage // JSON Schema of the args object (form source)
}

// ToolCatalog exposes the registered tool universe. Implemented in
// main.go over tools.Registry.BuildBaseTools().AllTools(). nil-safe:
// when unwired the flow-tools API returns an empty list.
type ToolCatalog interface {
	ListTools() []ToolMeta
}

// WithToolCatalog wires the palette source. Returns the usecase for
// chaining at construction.
func (u *Usecase) WithToolCatalog(c ToolCatalog) *Usecase {
	u.catalog = c
	return u
}

// ListTools returns the tool-node palette. Empty when no catalog is
// wired (LLM/tools runtime unavailable) — the canvas still works, the
// tool drawer just shows no presets.
func (u *Usecase) ListTools() []ToolMeta {
	if u.catalog == nil {
		return nil
	}
	return u.catalog.ListTools()
}
