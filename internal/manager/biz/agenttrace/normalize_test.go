package agenttrace

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
)

// Checked-in payloads come from capture_sdks.py and the locked real SDKs.
func TestRealSDKExports(t *testing.T) {
	for _, sdk := range []string{"langsmith", "openinference", "openllmetry", "langfuse"} {
		t.Run(sdk, func(t *testing.T) {
			body, err := os.ReadFile("testdata/sdk/" + sdk + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Resources []resourceSpans `json:"resourceSpans"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			var first rawSpan
			if err := json.Unmarshal(envelope.Resources[0].Scopes[0].Spans[0], &first); err != nil {
				t.Fatal(err)
			}
			trace, err := Normalize(canonicalID(first.TraceID, 16), body)
			if err != nil {
				t.Fatal(err)
			}
			if len(trace.Runs) != 1 {
				t.Fatalf("expected one graph run, got %+v", trace.Runs)
			}
			run := trace.Runs[0]
			if !slices.Contains(run.SDKs, sdk) || run.SessionID != "fixture-session" || !slices.Contains(run.Kinds, "llm") || !slices.Contains(run.Kinds, "tool") {
				t.Fatalf("lost SDK semantics: %+v", run)
			}
			if run.Usage.TotalTokens == nil || *run.Usage.TotalTokens != 58 || run.Usage.Conflict {
				t.Fatalf("real usage incorrect: %+v", run.Usage)
			}
			if len(trace.Issues) != 0 || len(run.Issues) != 0 {
				t.Fatalf("unexpected fixture diagnostics: %+v / %+v", trace.Issues, run.Issues)
			}
			for _, step := range trace.Steps {
				if sdk == "openinference" && step.Kind == "llm" {
					messages, ok := step.Input.Value.([]Attributes)
					if !ok || len(messages) != 1 || messages[0]["role"] != "user" || step.Input.Source != "llm.input_messages." || step.Attributes["input.value"] == nil {
						t.Fatalf("prefer semantic messages and retain serialized input: %+v", step.Input)
					}
				}
			}
		})
	}
}

const fixtureTraceID = "0102030405060708090a0b0c0d0e0f10"

func testAttributes(values map[string]any) []any {
	out := []any{}
	for key, value := range values {
		kind := "stringValue"
		switch value.(type) {
		case int, int64:
			kind = "intValue"
		case float64:
			kind = "doubleValue"
		case bool:
			kind = "boolValue"
		}
		out = append(out, map[string]any{"key": key, "value": map[string]any{kind: value}})
	}
	return out
}

func testSpan(id, parent int, attrs map[string]any) map[string]any {
	span := map[string]any{"traceId": fixtureTraceID, "spanId": fmt.Sprintf("%016x", id), "name": fmt.Sprintf("step-%d", id),
		"startTimeUnixNano": "1800000000000000000", "endTimeUnixNano": "1800000001250000000", "status": map[string]int{"code": 1}, "attributes": testAttributes(attrs)}
	if parent != 0 {
		span["parentSpanId"] = fmt.Sprintf("%016x", parent)
	}
	return span
}

func testBody(t *testing.T, spans ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource":   map[string]any{"attributes": testAttributes(map[string]any{"service.name": "support-agent", "service.namespace": "helpdesk", "deployment.environment.name": "test"})},
		"scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "fixture", "version": "1.0.0"}, "spans": spans}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAdaptersAndContent(t *testing.T) {
	cases := []struct {
		name, sdk, kind string
		attrs           map[string]any
	}{
		{"langsmith", "langsmith", "llm", map[string]any{"langsmith.span.kind": "llm", "gen_ai.request.model": "model", "gen_ai.prompt": `{"messages":[{"role":"user","content":"hello"}]}`, "gen_ai.usage.input_tokens": 10, "gen_ai.usage.output_tokens": 0, "langsmith.metadata.thread_id": "thread"}},
		{"openinference", "openinference", "llm", map[string]any{"openinference.span.kind": "LLM", "llm.model_name": "model", "llm.input_messages.1.message.role": "user", "llm.input_messages.1.message.content": "hello", "llm.input_messages.0.message.role": "system", "llm.token_count.prompt": 10, "llm.token_count.completion": 0, "session.id": "thread"}},
		{"openllmetry", "openllmetry", "llm", map[string]any{"traceloop.span.kind": "task", "gen_ai.operation.name": "chat", "gen_ai.request.model": "model", "traceloop.entity.input": `{"query":"hello"}`, "gen_ai.usage.prompt_tokens": 10, "gen_ai.usage.completion_tokens": 0, "traceloop.association.properties.session_id": "thread"}},
		{"langfuse", "langfuse", "llm", map[string]any{"langfuse.observation.type": "generation", "langfuse.observation.model.name": "model", "langfuse.observation.input": "hello", "langfuse.observation.usage_details": `{"input":10,"output":0}`, "session.id": "thread"}},
		{"gen_ai", "gen_ai", "llm", map[string]any{"gen_ai.operation.name": "chat", "gen_ai.request.model": "model", "gen_ai.input.messages": `[{"role":"user","parts":[{"type":"text","content":"hello"}]}]`, "gen_ai.usage.input_tokens": 10, "gen_ai.usage.output_tokens": 0, "gen_ai.conversation.id": "thread"}},
	}
	// Traceloop task is a workflow even when it records a model attribute.
	cases[2].kind = "workflow"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace, err := Normalize(fixtureTraceID, testBody(t, testSpan(1, 0, tc.attrs)))
			if err != nil {
				t.Fatal(err)
			}
			step := trace.Steps[0]
			if step.SDK != tc.sdk || step.Kind != tc.kind || step.Model != "model" || step.SessionID != "thread" {
				t.Fatalf("bad mapping: %+v", step)
			}
			if step.Input.State != "available" || step.Output.State != "not_recorded" || step.Usage.TotalTokens == nil || *step.Usage.TotalTokens != 10 || step.Usage.OutputTokens == nil || *step.Usage.OutputTokens != 0 {
				t.Fatalf("lost content or zero usage: %+v", step)
			}
			if step.DurationMS != 1250 || step.Scope.Version != "1.0.0" || len(step.Raw) == 0 || len(trace.Runs) != 1 {
				t.Fatalf("lost trace metadata: %+v", trace)
			}
		})
	}
}

func TestRunBoundariesRecoveredToolAndUnknownSpans(t *testing.T) {
	root := testSpan(1, 0, map[string]any{})
	agent := testSpan(2, 1, map[string]any{"openinference.span.kind": "AGENT"})
	tool := testSpan(3, 2, map[string]any{"openinference.span.kind": "TOOL"})
	tool["status"] = map[string]int{"code": 2}
	custom := testSpan(4, 2, map[string]any{"my.custom": "retained"})
	second := testSpan(5, 1, map[string]any{"openinference.span.kind": "AGENT"})
	trace, err := Normalize(fixtureTraceID, testBody(t, root, agent, tool, custom, second))
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Runs) != 2 || len(trace.Steps) != 5 {
		t.Fatalf("entry boundaries: %+v", trace)
	}
	for _, run := range trace.Runs {
		if run.EntrySpanID == "0000000000000002" && (run.Status != "ok" || run.ErrorCount != 1 || run.StepCount != 3) {
			t.Fatalf("retry changed run status: %+v", run)
		}
	}
}

func TestBrokenHierarchyAndBase64IDs(t *testing.T) {
	a := testSpan(1, 2, map[string]any{"langsmith.span.kind": "chain", "langsmith.trace.session_id": "project-is-not-session"})
	b := testSpan(2, 1, map[string]any{})
	c := testSpan(3, 99, map[string]any{"gen_ai.operation.name": "chat"})
	bytes, _ := hex.DecodeString("0000000000000003")
	c["spanId"] = base64.StdEncoding.EncodeToString(bytes)
	trace, err := Normalize(fixtureTraceID, testBody(t, a, b, c))
	if err != nil {
		t.Fatal(err)
	}
	issues := []string{}
	for _, step := range trace.Steps {
		issues = append(issues, step.Issues...)
		if step.SessionID != "" {
			t.Fatal("project ID became session")
		}
	}
	if !slices.Contains(issues, "parent_cycle") || !slices.Contains(issues, "missing_parent") || len(trace.Runs) != 2 {
		t.Fatalf("broken hierarchy not retained: %+v", trace)
	}
}

func TestPreservesEventsLinksAndTypedAttributes(t *testing.T) {
	span := testSpan(1, 0, map[string]any{"gen_ai.operation.name": "chat", "custom.bool": false, "custom.int": int64(9007199254740993)})
	span["events"] = []any{map[string]any{"name": "gen_ai.user.message", "timeUnixNano": "1800000000000000001", "attributes": testAttributes(map[string]any{"content": "hello"})}}
	span["links"] = []any{map[string]any{"traceId": fixtureTraceID, "spanId": "0000000000000009", "attributes": testAttributes(map[string]any{"reason": "handoff"})}}
	span["droppedAttributesCount"] = 1
	trace, err := Normalize(fixtureTraceID, testBody(t, span))
	if err != nil {
		t.Fatal(err)
	}
	step := trace.Steps[0]
	if fmt.Sprint(step.Attributes["custom.int"]) != "9007199254740993" || step.Attributes["custom.bool"] != false || len(step.Links) != 1 || step.Input.Source != "events" || !slices.Contains(step.Issues, "dropped_telemetry") {
		t.Fatalf("lost raw values: %+v", step)
	}
}

func TestUsageConflictsAndMissingCounts(t *testing.T) {
	u := decodeUsage(Attributes{"gen_ai.usage.input_tokens": "10", "llm.token_count.prompt": "11", "gen_ai.usage.output_tokens": "-1"})
	if !u.Conflict || u.OutputTokens != nil {
		t.Fatalf("invalid usage accepted: %+v", u)
	}
	u = decodeUsage(Attributes{"langfuse.observation.usage_details": `{"input":0,"output":2}`, "langfuse.observation.cost_details": `{"total":0.0002}`})
	if u.ReportedCost == nil || *u.ReportedCost != .0002 || u.Currency != "USD" || !u.Complete {
		t.Fatalf("bad reported cost: %+v", u)
	}
	u = aggregateUsage([]Step{{Usage: u}, {}})
	if u.TotalTokens != nil || u.ReportedCost != nil || u.Complete {
		t.Fatalf("missing incorrectly zero-filled: %+v", u)
	}
	attrs := map[string]any{"gen_ai.operation.name": "chat", "gen_ai.usage.input_tokens": 10, "gen_ai.usage.output_tokens": 2}
	trace, err := Normalize(fixtureTraceID, testBody(t, testSpan(1, 0, attrs), testSpan(2, 1, attrs)))
	if err != nil {
		t.Fatal(err)
	}
	if !trace.Runs[0].Usage.Conflict || trace.Runs[0].Usage.TotalTokens != nil {
		t.Fatalf("nested usage double counted: %+v", trace.Runs[0])
	}
}
