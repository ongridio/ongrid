package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestModelToolParametersPreservesContractGuidance(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object","description":"Find a trace.","additionalProperties":false,
		"properties":{"trace_id":{"type":"string"},"query":{"type":"string"},
		"options":{"type":"object","properties":{"mode":{"enum":["a","b"]}},"oneOf":[{"required":["mode"]}],"default":{"const":9007199254740993}}},
		"required":["options"],
		"oneOf":[{"required":["trace_id"]},{"required":["query"]}],
		"anyOf":[true,{"properties":{"count":{"type":"integer"}}}],
		"allOf":[{"not":{"required":["blocked"]}}],
		"not":{"required":["trace_id","query"]},"enum":[{"trace_id":"a"}],"const":{"trace_id":"a"}
	}`)
	before := string(raw)
	got, err := modelToolParameters(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != before {
		t.Fatal("original execution schema changed")
	}
	var original, projected map[string]json.RawMessage
	_ = json.Unmarshal(raw, &original)
	_ = json.Unmarshal(got, &projected)
	var description string
	_ = json.Unmarshal(projected["description"], &description)
	if !strings.HasPrefix(description, "Find a trace.") {
		t.Fatal(description)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "enum", "const", "not"} {
		if _, ok := projected[key]; ok {
			t.Errorf("unsupported root keyword %s", key)
		}
		if !strings.Contains(description, `"`+key+`":`) {
			t.Errorf("lost constraint guidance %s", key)
		}
	}
	for _, key := range []string{"required", "additionalProperties"} {
		if string(projected[key]) != string(original[key]) {
			t.Errorf("changed %s", key)
		}
	}
	var oldProps, newProps map[string]json.RawMessage
	_ = json.Unmarshal(original["properties"], &oldProps)
	_ = json.Unmarshal(projected["properties"], &newProps)
	for name, old := range oldProps {
		var oldValue, newValue any
		oldDecoder := json.NewDecoder(strings.NewReader(string(old)))
		oldDecoder.UseNumber()
		_ = oldDecoder.Decode(&oldValue)
		newDecoder := json.NewDecoder(strings.NewReader(string(newProps[name])))
		newDecoder.UseNumber()
		_ = newDecoder.Decode(&newValue)
		if !reflect.DeepEqual(oldValue, newValue) {
			t.Errorf("changed property %s: %s", name, newProps[name])
		}
	}
	if !strings.Contains(string(got), "9007199254740993") || newProps["count"] == nil {
		t.Fatal("lost numeric precision or branch-only parameter")
	}
}

func TestModelToolParametersBranchAlternatives(t *testing.T) {
	got, err := modelToolParameters(json.RawMessage(`{"oneOf":[{"properties":{"id":{"type":"integer"}}},{"allOf":[{"properties":{"id":{"type":"string"},"filter":{"type":"object","properties":{"nested":{"type":"boolean"}}}}}]}],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(got, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || len(schema.Properties) != 2 || !strings.Contains(string(schema.Properties["id"]), `"anyOf"`) {
		t.Fatalf("branch fields missing: %s", got)
	}
}

func TestModelToolParametersInvalidAndEmpty(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"type":"object"}`} {
		if got, err := modelToolParameters(json.RawMessage(raw)); err != nil || !strings.Contains(string(got), `"properties":{}`) {
			t.Errorf("empty schema %q: %s, %v", raw, got, err)
		}
	}
	for _, raw := range []string{`null`, `true`, `[]`, `{`, `{"type":"array"}`, `{"oneOf":{}}`, `{"oneOf":[42]}`} {
		if _, err := modelToolParameters(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid schema accepted: %s", raw)
		}
	}
}

func TestChatToolRoundTripWithCompatibleSchema(t *testing.T) {
	for _, model := range []string{"gpt-6.1-terra", "deepseek-v4-flash"} {
		t.Run(model, func(t *testing.T) {
			_, cfg := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Messages []struct{ Role, Content, ToolCallID string } `json:"messages"`
					Tools    []struct {
						Function struct{ Parameters map[string]json.RawMessage }
					} `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if len(req.Tools) != 1 || req.Tools[0].Function.Parameters["oneOf"] != nil {
					t.Error("provider received unsupported tool schema")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if req.Messages[len(req.Messages)-1].Role == "tool" {
					_, _ = w.Write(sampleChatResponse("result: ok", nil))
					return
				}
				_, _ = w.Write(sampleChatResponse("", []map[string]any{{"id": "call_probe", "type": "function", "function": map[string]any{"name": "probe", "arguments": `{"id":1}`}}}))
			})
			cfg.Model = model
			client := newTestClient(t, cfg, nil)
			req := ChatReq{Messages: []Message{{Role: "user", Content: "Use probe for id 1."}}, Tools: []ToolSchema{{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"},"name":{"type":"string"}},"oneOf":[{"required":["id"]},{"required":["name"]}]}`)}}}
			resp, err := client.Chat(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(resp.Assistant.ToolCalls) != 1 || resp.Assistant.ToolCalls[0].Name != "probe" || string(resp.Assistant.ToolCalls[0].Args) != `{"id":1}` {
				t.Fatalf("bad tool call: %+v", resp.Assistant)
			}
			req.Messages = append(req.Messages, resp.Assistant, Message{Role: "tool", ToolCallID: "call_probe", Content: "ok"})
			resp, err = client.Chat(context.Background(), req)
			if err != nil || resp.Assistant.Content != "result: ok" {
				t.Fatalf("tool result round trip: %+v, %v", resp, err)
			}
		})
	}
}
