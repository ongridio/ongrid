package flow

import (
	"encoding/json"
	"strconv"
	"strings"
)

// CoerceArgsToSchema nudges resolved tool args toward the types their JSON
// Schema declares, so a {{ref}} that resolved to the "wrong" shape still works
// instead of erroring deep in the tool's unmarshal. Best-effort: any arg it
// can't confidently convert is left untouched. Covers the common flow-wiring
// mismatches: scalar→array, "[…]"-string→array, numeric-string→number,
// "true"/"false"→bool, json-string→object.
func CoerceArgsToSchema(argsStr string, schema json.RawMessage) string {
	var sc struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &sc) != nil || len(sc.Properties) == 0 {
		return argsStr
	}
	var args map[string]any
	if json.Unmarshal([]byte(argsStr), &args) != nil {
		return argsStr
	}
	changed := false
	for k, v := range args {
		p, ok := sc.Properties[k]
		if !ok {
			continue
		}
		if nv, did := coerceValue(v, p.Type); did {
			args[k] = nv
			changed = true
		}
	}
	if !changed {
		return argsStr
	}
	b, err := json.Marshal(args)
	if err != nil {
		return argsStr
	}
	return string(b)
}

func coerceValue(v any, typ string) (any, bool) {
	switch typ {
	case "array":
		if _, isArr := v.([]any); isArr {
			return v, false
		}
		if str, isStr := v.(string); isStr {
			var arr []any
			if json.Unmarshal([]byte(strings.TrimSpace(str)), &arr) == nil {
				return arr, true // "[1, 2]" / "[{{ref}}]"-resolved → real array
			}
			return []any{str}, true
		}
		if v == nil {
			return v, false
		}
		return []any{v}, true // wrap a scalar into a single-element array
	case "number":
		if str, isStr := v.(string); isStr {
			if n, err := strconv.ParseFloat(strings.TrimSpace(str), 64); err == nil {
				return n, true
			}
		}
	case "integer":
		if str, isStr := v.(string); isStr {
			if n, err := strconv.ParseInt(strings.TrimSpace(str), 10, 64); err == nil {
				return n, true
			}
		}
	case "boolean":
		if str, isStr := v.(string); isStr {
			switch strings.ToLower(strings.TrimSpace(str)) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	case "object":
		if str, isStr := v.(string); isStr {
			var m map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(str)), &m) == nil {
				return m, true
			}
		}
	}
	return v, false
}
