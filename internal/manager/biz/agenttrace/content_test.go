package agenttrace

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tracing"
)

func TestCapturedContentIsDecodedOnlyFromTrace(t *testing.T) {
	t.Setenv("ONGRID_AGENT_TRACE_CAPTURE_CONTENT", "true")
	raw, _ := json.Marshal(strings.Repeat("执行快照😀", 10000))
	snapshot := tracing.MaskAgentContent(string(raw))
	body := testBody(t, testSpan(1, 0, map[string]any{"gen_ai.operation.name": "invoke_agent", "input.value": `"本轮问题"`, "output.value": snapshot, "ongrid.agent.session.id": "deleted-chat", "ongrid.agent.content.capture": true}))
	body = bytes.ReplaceAll(body, []byte(`"name":"fixture"`), []byte(`"name":"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"`))
	trace, err := Normalize(fixtureTraceID, body)
	if err != nil {
		t.Fatal(err)
	}
	output := trace.Steps[0].Output
	if !output.Truncated || output.OriginalBytes != len(raw) || output.State != "available" || trace.Runs[0].InputPreview != "本轮问题" || trace.Runs[0].ChatSessionID != "deleted-chat" {
		t.Fatalf("captured snapshot not retained independently: %+v", trace)
	}
	if trace.Steps[0].Attributes["output.value"] != snapshot || !bytes.Contains(trace.Steps[0].Raw, []byte("ongrid.trace.content/v1")) {
		t.Fatal("display normalization changed recorded attributes")
	}
}

func TestDisabledCaptureIsInheritedWithoutFabricatingContent(t *testing.T) {
	body := testBody(t,
		testSpan(1, 0, map[string]any{"gen_ai.operation.name": "invoke_agent", "ongrid.agent.content.capture": false}),
		testSpan(2, 1, map[string]any{"gen_ai.operation.name": "chat"}),
		testSpan(3, 1, map[string]any{"gen_ai.operation.name": "execute_tool", "input.value": `"explicit"`}),
	)
	trace, err := Normalize(fixtureTraceID, body)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Runs[0].InputState != "capture_disabled" || trace.Steps[1].Input.State != "capture_disabled" || trace.Steps[2].Input.State != "available" {
		t.Fatalf("capture states are wrong: %+v", trace)
	}
}
