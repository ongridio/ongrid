package discovery

import (
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// Actor is the caller, as the ownership rules need them.
//
// THE USER ID IS AN ONGRID ID. cantor compared its own `current_user.uid`
// against a rule's `owner`; the port compares the caller's Ongrid id against
// the same column, which holds Ongrid ids — cantor's user table is not migrated
// and its uid space is never reused.
type Actor struct {
	UserID model.ID

	// Email is the caller's display identity, used where cantor stored a
	// nickname — the pool's `accept_by` is the only such column. Empty is
	// normal and falls back to the numeric id.
	Email string

	// IsAdmin reports whether the caller may write a PLUGIN rule. cantor gated
	// that on a per-operation ACL — create/update/delete plugin — which is
	// resource-level authorization and lands with the rest of it. Until then
	// the gate is the deployment's admin role, which is stricter: a plugin rule
	// carries user-supplied code.
	IsAdmin bool
}

// Usecase is the auto-discovery module's entry point.
type Usecase struct {
	repo Repo

	// models answers the model-side questions a target needs: which model a
	// reference names, and which attribute of it identifies an instance. It is
	// nillable, so a deployment that wires no resolver gets a clear
	// ErrNotWiredYet from the target routes rather than a panic.
	models ModelResolver

	// instances answers the two questions the agent's poll asks of the instance
	// side: which instance an agent runs on, and whether an expression selects
	// one. Nillable for the same reason.
	instances InstanceQueries

	// pool is the discovery pool's persistence. Nillable so the rule and target
	// routes keep working in a deployment that wires no pool, and so a test can
	// keep the two layers apart.
	pool PoolRepo

	// writer is the instance side's accept path. It is a consumer-declared port
	// because acceptance is the one place this module writes an instance, and
	// the owning business package must stay the only one that knows how.
	writer InstanceWriter

	// attrs answers "which attributes does this model carry", which the pool
	// needs to keep its columns to the mapped subset.
	attrs AttributeResolver

	// Now is the clock, injectable so a test can reason about timestamps
	// without sleeping.
	Now func() time.Time
}

// NewUsecase builds the use case over its persistence.
func NewUsecase(repo Repo) *Usecase {
	return &Usecase{repo: repo, Now: time.Now}
}

// Rule is one auto-discovery rule as the API presents it.
//
// IT CARRIES NO SCRIPT. `plugin_script` is user Python; it is stored and it is
// never returned, because a read that hands back a deployment's own code is a
// read that will eventually be pasted somewhere. What a caller needs to know is
// whether there IS one — see HasPluginScript.
type Rule struct {
	ID   model.ID `json:"id"`
	Name string   `json:"name"`

	Type     string `json:"type,omitempty"`
	IsInner  bool   `json:"is_inner"`
	IsPlugin bool   `json:"is_plugin"`

	UniqueKey string `json:"unique_key,omitempty"`

	// AttributeCount is how many fields the rule reports, which is what a
	// console shows beside a provider.
	AttributeCount int `json:"attribute_count"`

	// HasPluginScript says a script is stored, without returning it.
	HasPluginScript bool `json:"has_plugin_script"`

	// Attributes is the rule's own field list, for a plugin rule that supplied
	// one.
	Attributes []AttributeField `json:"attributes,omitempty"`

	Option map[string]any `json:"option,omitempty"`

	// Categories is a cloud provider's resource categories, attached to the
	// list so the console does not have to ask once per row.
	Categories []Category `json:"categories,omitempty"`

	Owner *model.ID `json:"owner,omitempty"`
}

// AttributeField is one field a provider reports.
type AttributeField struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Desc string `json:"desc,omitempty"`

	// Example is whatever the provider wrote, passed through — see the catalog
	// package. It is a hint shown beside a field name, and a provider writes a
	// number for some of them.
	Example any `json:"example,omitempty"`
}

// Category groups a cloud provider's resources.
type Category struct {
	Category string   `json:"category"`
	Items    []string `json:"items"`
}
