// Package skill defines ongrid's L2 device-direct capability framework.
//
// A "skill" is one self-contained device-side capability — probe a TCP
// port, tail a file, run a command, capture pcap, etc. Skills bundle:
//   - metadata (name, description, params schema, permission class)
//   - an Executor that runs on the edge
//
// The framework auto-derives the LLM tool registration, the HTTP API,
// the UI form, the permission gate, and the audit log from skill
// metadata — adding a new skill only requires writing one file and
// registering it once in init().
//
// Skills replace the per-capability tunnel methods (MethodGetHostLoad
// etc.) with a single dispatcher RPC: edge agent registers one
// "execute_skill" handler that dispatches by skill key. Manager-side
// AI tools and HTTP routes are equally generic.
//
// Permission classes ("代差" design):
//   - safe: read-only, no side effects (probe / read file / inspect)
//   - mutating: modifies edge state but reversible (kill process, restart service)
//   - dangerous: irreversible / cluster-impacting (rm, reboot, drop table)
//
// AI agents may invoke safe skills directly. Mutating require human
// approval. Dangerous require RSA-signed SOP + dual approval. The base
// framework only enforces the class flag; the per-class policy lives in
// the manager-side service layer.
package skill

import (
	"context"
	"encoding/json"
	"errors"
)

// Class is a skill's permission class. The zero value is safe (most
// permissive) so a skill author who forgets to set Class doesn't ship a
// dangerous unguarded capability — but skills with side effects should
// always set their Class explicitly.
type Class string

const (
	ClassSafe      Class = "safe"
	ClassMutating  Class = "mutating"
	ClassDangerous Class = "dangerous"
)

// Scope decides which side runs the skill body.
//
//   - ScopeHost (default; zero value): the skill executes on a target
//     edge agent. Manager dispatches via tunnel MethodExecuteSkill; the
//     LLM tool wrapper requires an `edge_id` argument.
//   - ScopeManager: the skill executes in-process on the manager. No
//     edge_id is required; useful for tools that talk to the public
//     internet (web_search), call external APIs, or run subprocess
//     skill packs.
type Scope string

const (
	ScopeHost    Scope = "host"
	ScopeManager Scope = "manager"
)

// Param describes a single skill parameter. The schema is intentionally
// small (matches what we render in the auto-generated UI form and what
// LLMs need in the function-calling JSON Schema).
type Param struct {
	// Type is one of "string" | "int" | "float" | "bool" | "duration" |
	// "enum" | "array". Anything else gets rejected at registration.
	Type string
	// Required flips between "must be supplied" and "may use Default".
	Required bool
	// Default is used by the UI form initial value and by Execute when
	// the caller omits the param. Must be omitted (nil) when Required.
	Default any
	// Desc is shown to the human in the UI form and to the LLM in the
	// tool description.
	Desc string
	// Enum is the allowed value set when Type == "enum". Ignored
	// otherwise.
	Enum []string
	// ItemType is the element type when Type == "array". Must be one of
	// the scalar Type values (no nested arrays for now — the N+15 batch
	// shapes ("device_ids":[1,2], "paths":["/var"]) are all flat). Ignored
	// when Type != "array".
	ItemType string
}

// ParamSchema is the ordered map of param name -> spec. Order matters
// for UI rendering; we keep it as a slice instead of map[string]Param.
type ParamSchema []ParamDef

// ParamDef is one entry in ParamSchema. Name and Param are flattened
// here to keep the auto-generated UI/LLM schema readable.
type ParamDef struct {
	Name string
	Param
}

// Metadata is everything the framework needs to auto-wire a skill.
type Metadata struct {
	// Key is the stable identifier used in dedupe keys, audit logs,
	// HTTP routes, and LLM tool names. lower_snake.
	Key string
	// Name is the human-readable label shown in the UI.
	Name string
	// Description is the rich text shown to humans AND to the LLM as
	// the tool's "description" field. Should explain what the skill
	// does and any preconditions.
	Description string
	// Class gates who can invoke this skill (see package doc).
	Class Class
	// Scope decides which side runs the skill body. Zero value is
	// ScopeHost (the historical default — every skill written before
	// scope existed targets an edge).
	Scope Scope
	// Category is a free-form group label for UI organization
	// ("network" / "filesystem" / "process" / "diagnostic" / "telemetry").
	Category string
	// Params declares the input schema. Generated as JSON Schema for
	// LLM tools and as a form for the UI.
	Params ParamSchema
	// ResultPreview is a one-line hint about the result shape, shown in
	// the UI and to the LLM. Keep terse; the actual result JSON varies.
	ResultPreview string
}

// Validate checks the metadata is internally consistent. Called by the
// registry on Register; surfaces author errors at startup, not at first
// invocation.
func (m Metadata) Validate() error {
	if m.Key == "" {
		return errors.New("skill: metadata.Key required")
	}
	if !validKey(m.Key) {
		return errors.New("skill: metadata.Key must be lower_snake [a-z0-9_]")
	}
	if m.Name == "" {
		return errors.New("skill: metadata.Name required")
	}
	if m.Description == "" {
		return errors.New("skill: metadata.Description required")
	}
	switch m.Class {
	case "", ClassSafe, ClassMutating, ClassDangerous:
	default:
		return errors.New("skill: metadata.Class unknown: " + string(m.Class))
	}
	switch m.Scope {
	case "", ScopeHost, ScopeManager:
	default:
		return errors.New("skill: metadata.Scope unknown: " + string(m.Scope))
	}
	for _, p := range m.Params {
		if p.Name == "" {
			return errors.New("skill: param missing Name")
		}
		switch p.Type {
		case "string", "int", "float", "bool", "duration", "enum":
		case "array":
			switch p.ItemType {
			case "string", "int", "float", "bool", "duration", "enum":
			case "":
				return errors.New("skill: param " + p.Name + " Type=array requires ItemType")
			default:
				return errors.New("skill: param " + p.Name + " unsupported ItemType " + p.ItemType)
			}
		default:
			return errors.New("skill: param " + p.Name + " unsupported Type " + p.Type)
		}
		if p.Required && p.Default != nil {
			return errors.New("skill: param " + p.Name + " required and Default both set")
		}
	}
	return nil
}

// RawSchemaProvider is an optional Executor extension. When implemented,
// the framework uses the supplied JSON Schema verbatim instead of
// deriving one from Metadata.Params. Used by hand-written tools whose
// schemas have arrays / nested objects / oneOf shapes that the
// declarative ParamSchema can't express.
//
// The returned schema must be a valid JSON Schema object describing the
// param envelope (top-level "type":"object" with "properties" + "required"),
// matching what OpenAI function-calling expects.
type RawSchemaProvider interface {
	JSONSchema() json.RawMessage
}

// Executor is the skill implementation. Each skill registers exactly one
// Executor; that Executor lives on whichever side actually does the work
// (edge for device-direct skills, manager for cloud-side skills). The
// framework dispatches transparently.
//
// The params blob has already been validated against Params() schema by
// the caller side before reaching Execute, but Execute must still treat
// params as untrusted (defensive unmarshal) — the schema only checks
// shape, not value semantics (e.g. it doesn't verify a "target" string
// resolves to a routable host).
type Executor interface {
	Metadata() Metadata
	Execute(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// EffectiveClass returns the metadata Class, defaulting to ClassSafe for
// authors who left it blank. We default to safe rather than dangerous
// so a missing field triggers permissive behavior — but the framework
// also logs a warning at registration time so this can't slip silently.
func (m Metadata) EffectiveClass() Class {
	if m.Class == "" {
		return ClassSafe
	}
	return m.Class
}

// EffectiveScope returns the metadata Scope, defaulting to ScopeHost so
// pre-existing skills (written before Scope existed) keep their device-
// direct semantics.
func (m Metadata) EffectiveScope() Scope {
	if m.Scope == "" {
		return ScopeHost
	}
	return m.Scope
}

func validKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
