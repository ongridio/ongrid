package basetool

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	validator "github.com/santhosh-tekuri/jsonschema/v5"
)

func TestModelSchemaPreservesDefinitionsAndNestedKeywords(t *testing.T) {
	cases := []string{
		`{"type":"object","properties":{"id":{"const":9007199254740993,"enum":[9007199254740993],"default":{"id":18446744073709551615},"examples":[{"id":9007199254740993}]}}}`,
		`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"id":{"$ref":"#/definitions/id"}},"definitions":{"id":{"type":"integer","minimum":1}},"required":["id"]}`,
		`{"type":"object","properties":{"payload":{"type":"object","definitions":{"id":{"type":"integer"}},"properties":{"id":{"$ref":"#/properties/payload/definitions/id"}},"unevaluatedProperties":false}}}`,
		`{"type":"object","properties":{"value":{"const":null}},"allOf":[{"x-vendor":{"id":9007199254740993}}],"additionalProperties":false}`,
		`{"type":"object","$defs":{"id":{"type":"integer"}},"properties":{"id":{"$ref":"#/$defs/id"}},"required":["id"]}`,
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			schema, err := ModelSchema(json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			decode := func(raw []byte) any {
				t.Helper()
				var value any
				d := json.NewDecoder(bytes.NewReader(raw))
				d.UseNumber()
				if err := d.Decode(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			if !reflect.DeepEqual(decode([]byte(raw)), decode(out)) {
				t.Fatalf("schema changed:\n%s\n%s", raw, out)
			}
			compiler := validator.NewCompiler()
			if err := compiler.AddResource("https://ongrid.invalid/model-schema", bytes.NewReader(out)); err != nil {
				t.Fatal(err)
			}
			if _, err := compiler.Compile("https://ongrid.invalid/model-schema"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestModelSchemaBooleanWhitespace(t *testing.T) {
	for _, raw := range []string{" true ", "\nfalse\t"} {
		schema, err := ModelSchema(json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bytes.TrimSpace([]byte(raw)), out) {
			t.Fatalf("changed boolean: %s", out)
		}
	}
}
