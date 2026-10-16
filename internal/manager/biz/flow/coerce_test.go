package flow

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCoerceArgsPreservesWorkflowCompatibility(t *testing.T) {
	schema := json.RawMessage(`{"properties":{"ids":{"type":"array"},"limit":{"type":"integer"},"enabled":{"type":"boolean"},"options":{"type":"object"}}}`)
	got := CoerceArgsToSchema(`{"ids":7,"limit":"20","enabled":" TRUE ","options":"{\"x\":1}","unknown":"unchanged"}`, schema)
	var value map[string]any
	if err := json.Unmarshal([]byte(got), &value); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"ids": []any{float64(7)}, "limit": float64(20), "enabled": true, "options": map[string]any{"x": float64(1)}, "unknown": "unchanged"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("coercion = %#v", value)
	}
	for _, input := range []string{`invalid`, `{"limit":"not-a-number"}`, `{"ids":[1,2],"enabled":false}`} {
		if got := CoerceArgsToSchema(input, schema); got != input {
			t.Fatalf("changed %s to %s", input, got)
		}
	}
	if got := CoerceArgsToSchema(`{"ids":7}`, json.RawMessage(`invalid`)); got != `{"ids":7}` {
		t.Fatalf("invalid schema changed input: %s", got)
	}
}
