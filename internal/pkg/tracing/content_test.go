package tracing

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAgentContentPolicy(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "")
	if got := MaskAgentContent(`"private"`); got != "" {
		t.Fatal("capture must be opt-in")
	}
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
	if got := MaskAgentContent(`"hello"`); got != `"hello"` {
		t.Fatalf("small text changed: %s", got)
	}
	if got := MaskAgentContent("model-name"); got != "model-name" {
		t.Fatalf("callback metadata was re-encoded: %s", got)
	}
	rows := []map[string]any{{"role": "system", "content": strings.Repeat("system-private", 10000)}}
	for i := 0; i < 20; i++ {
		rows = append(rows, map[string]any{"role": "user", "content": "message"})
	}
	rows = append(rows, map[string]any{"role": "assistant", "content": "latest", "reasoning_content": "private-reasoning", "tool_calls": []any{map[string]any{"arguments": `{"password":"secret-credential","id":9007199254740993}`}}})
	raw, _ := json.Marshal(rows)
	got := MaskAgentContent(string(raw))
	var snapshot struct {
		Schema        string           `json:"schema"`
		Value         []map[string]any `json:"value"`
		OriginalBytes int              `json:"original_bytes"`
		Omitted       []string         `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(got), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Schema != AgentContentSchema || len(snapshot.Value) != 13 || snapshot.Value[12]["content"] != "latest" || snapshot.OriginalBytes != len(raw) {
		t.Fatalf("unexpected compact history: %s", got)
	}
	if len(snapshot.Value[0]["sha256"].(string)) != 64 || !strings.Contains(got, "9007199254740993") {
		t.Fatal("system hash or exact numeric tool argument missing")
	}
	for _, private := range []string{"system-private", "private-reasoning", "secret-credential"} {
		if strings.Contains(got, private) {
			t.Fatalf("omitted content retained: %s", private)
		}
	}
	for _, input := range []string{`{"Authorization":"Bearer private-key","cookie":"private-cookie"}`, `"Authorization: Bearer private-token"`, `"https://user:private-password@example.com"`, `"data:image/png;base64,private-media"`} {
		if strings.Contains(MaskAgentContent(input), "private-") {
			t.Fatalf("credential/media not masked: %s", input)
		}
	}
}

func TestAgentContentLargeValuesStayBoundedAndValid(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
	for _, value := range []any{strings.Repeat("长内容😀", 10000), map[string]any{"result": strings.Repeat("\"\\\n", 50000)}, []string{strings.Repeat("x", 100000)}} {
		raw, _ := json.Marshal(value)
		got := MaskAgentContent(string(raw))
		if len(got) > AgentContentMaxBytes || !utf8.ValidString(got) || !json.Valid([]byte(got)) {
			t.Fatalf("invalid bounded snapshot: %d bytes", len(got))
		}
		var snapshot struct {
			Truncated     bool `json:"truncated"`
			OriginalBytes int  `json:"original_bytes"`
		}
		if err := json.Unmarshal([]byte(got), &snapshot); err != nil || !snapshot.Truncated || snapshot.OriginalBytes != len(raw) {
			t.Fatalf("truncation not reported: %+v / %v", snapshot, err)
		}
	}
}
