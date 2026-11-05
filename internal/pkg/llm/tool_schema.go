package llm

import (
	"encoding/json"
	"fmt"
)

// modelToolParameters projects the wire schema to the object shape accepted by
// chat providers. Callers must still validate arguments against the original
// schema before execution; this projection is only for model presentation.
func modelToolParameters(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if root == nil {
		return nil, fmt.Errorf("schema must be an object")
	}
	if typ, ok := root["type"]; ok && string(typ) != `"object"` {
		return nil, fmt.Errorf("schema must have type object")
	}
	root["type"] = json.RawMessage(`"object"`)
	constraints := make(map[string]json.RawMessage)
	for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
		if value, ok := root[key]; ok {
			constraints[key] = value
			delete(root, key)
		}
	}
	if len(constraints) > 0 {
		// Branch-only fields must remain visible, including when the root
		// disallows additional properties. Keep branch conditions as guidance.
		properties := make(map[string]json.RawMessage)
		if value, ok := root["properties"]; ok {
			if err := json.Unmarshal(value, &properties); err != nil {
				return nil, fmt.Errorf("properties: %w", err)
			}
			if properties == nil {
				return nil, fmt.Errorf("properties must be an object")
			}
		}
		branches := make(map[string][]json.RawMessage)
		if err := collectToolProperties(constraints, branches); err != nil {
			return nil, err
		}
		for name, schemas := range branches {
			if _, exists := properties[name]; exists {
				continue
			}
			value := schemas[0]
			if len(schemas) > 1 {
				value, _ = json.Marshal(map[string]any{"anyOf": schemas}) // Raw schemas were decoded above.
			}
			properties[name] = value
		}
		root["properties"], _ = json.Marshal(properties)
		var description string
		if value, ok := root["description"]; ok {
			if err := json.Unmarshal(value, &description); err != nil {
				return nil, fmt.Errorf("description: %w", err)
			}
		}
		hint, _ := json.Marshal(constraints)
		if description != "" {
			description += "\n\n"
		}
		description += "Argument constraints (validated before execution): " + string(hint)
		root["description"], _ = json.Marshal(description)
	}
	if _, ok := root["properties"]; !ok {
		root["properties"] = json.RawMessage(`{}`)
	}
	return json.Marshal(root)
}

// Only composition branches define alternative arguments; never traverse
// arbitrary business values, nested object properties, or negative schemas.
func collectToolProperties(node map[string]json.RawMessage, out map[string][]json.RawMessage) error {
	var properties map[string]json.RawMessage
	if raw, ok := node["properties"]; ok {
		if err := json.Unmarshal(raw, &properties); err != nil {
			return fmt.Errorf("branch properties: %w", err)
		}
	}
	for name, raw := range properties {
		out[name] = append(out[name], raw)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		var branches []json.RawMessage
		if raw, ok := node[key]; ok {
			if err := json.Unmarshal(raw, &branches); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		for _, raw := range branches {
			if string(raw) == "true" || string(raw) == "false" {
				continue
			}
			var branch map[string]json.RawMessage
			if err := json.Unmarshal(raw, &branch); err != nil {
				return fmt.Errorf("%s branch: %w", key, err)
			}
			if err := collectToolProperties(branch, out); err != nil {
				return err
			}
		}
	}
	return nil
}
