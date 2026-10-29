// Package externalmcp owns the fixed, scoped integration tool contract.
package externalmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
)

// Arguments is limited to validated JSON values at the MCP protocol boundary.
type Arguments map[string]any

type Target struct {
	Type string `json:"type"`
	ID   uint64 `json:"id,omitempty"`
	Key  string `json:"key"`
}
type Policy struct {
	Target Target
	Remote bool
	// LowRisk is opt-in; unclassified mutations still require confirmation.
	LowRisk bool
}

type Tool struct {
	Name         string
	Description  string
	Schema       json.RawMessage
	Scopes       []string
	Mutation     bool
	Sensitive    bool
	Verification string
	// ActionScopes narrows compound tools in both discovery and execution.
	ActionScopes   map[string][]string
	RequiredScopes func(Arguments) []string
	Validate       func(Arguments) error
	// ValidateNew runs time-sensitive checks after idempotency recovery, or before a dry run.
	ValidateNew func(Arguments) error
	Policy      func(Arguments) (Policy, error)
	Run         func(context.Context, Arguments) (any, error)
	resolved    *jsonschema.Resolved
}

func (t Tool) Visible(identity integrationauth.Identity) bool {
	if !identity.Allows(t.Scopes...) {
		return false
	}
	if len(t.ActionScopes) == 0 {
		return true
	}
	for _, scopes := range t.ActionScopes {
		if identity.Allows(scopes...) {
			return true
		}
	}
	return false
}

func (t Tool) Authorize(identity integrationauth.Identity, args Arguments) bool {
	if !t.Visible(identity) {
		return false
	}
	if t.RequiredScopes != nil && !identity.Allows(t.RequiredScopes(args)...) {
		return false
	}
	if len(t.ActionScopes) == 0 {
		return true
	}
	scopes, ok := t.ActionScopes[String(args, "action")]
	return ok && identity.Allows(scopes...)
}

func NewCatalog(tools []Tool) (map[string]*Tool, error) {
	out := make(map[string]*Tool, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || tool.Run == nil || (len(tool.Scopes) == 0 && len(tool.ActionScopes) == 0 && tool.Name != "get_operation" && tool.Name != "cancel_operation" && tool.Name != "list_api_operations") {
			return nil, fmt.Errorf("invalid external tool %q", tool.Name)
		}
		if _, ok := out[tool.Name]; ok {
			return nil, fmt.Errorf("duplicate external tool %q", tool.Name)
		}
		if tool.Mutation && tool.Policy == nil {
			return nil, fmt.Errorf("missing write policy for %s", tool.Name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			return nil, fmt.Errorf("%s schema: %w", tool.Name, err)
		}
		if schema.Type != "object" {
			return nil, fmt.Errorf("%s requires object schema", tool.Name)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%s schema resolve: %w", tool.Name, err)
		}
		const confirmation = " Routine writes execute without confirmation. High-risk writes return awaiting_approval and approval_url; present that link for one human confirmation. Do not approve through tools."
		if tool.Mutation && !strings.HasSuffix(tool.Description, confirmation) {
			tool.Description += confirmation
		}
		tool.resolved = resolved
		out[tool.Name] = &tool
	}
	return out, nil
}

func (t Tool) Parse(raw json.RawMessage) (Arguments, error) {
	var args Arguments
	if len(raw) > 60*1024 || json.Unmarshal(raw, &args) != nil || args == nil {
		return nil, errs.ErrInvalid
	}
	if err := t.resolved.Validate(args); err != nil {
		return nil, errs.ErrInvalid
	}
	if t.Validate != nil {
		if err := t.Validate(args); err != nil {
			return nil, err
		}
	}
	if t.Mutation {
		key := String(args, "idempotency_key")
		if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
			return nil, errs.ErrInvalid
		}
	}
	return args, nil
}

func String(a Arguments, key string) string  { s, _ := a[key].(string); return s }
func Number(a Arguments, key string) float64 { n, _ := a[key].(float64); return n }
func Bool(a Arguments, key string) bool      { b, _ := a[key].(bool); return b }

// InputSchema returns a private copy with inaccessible actions removed.
func (t Tool) InputSchema(identity integrationauth.Identity) json.RawMessage {
	if len(t.ActionScopes) == 0 {
		return t.Schema
	}
	var schema map[string]any
	if json.Unmarshal(t.Schema, &schema) != nil {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)
	action, _ := properties["action"].(map[string]any)
	values, _ := action["enum"].([]any)
	allowed := []any{}
	for _, value := range values {
		name, _ := value.(string)
		if scopes, ok := t.ActionScopes[name]; ok && identity.Allows(scopes...) {
			allowed = append(allowed, value)
		}
	}
	action["enum"] = allowed
	raw, _ := json.Marshal(schema) // The schema was decoded from valid JSON above.
	return raw
}
