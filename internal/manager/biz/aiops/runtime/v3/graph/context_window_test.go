package graph

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	model "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	aiops "github.com/ongridio/ongrid/internal/manager/model/aiops"
	openai "github.com/sashabaranov/go-openai"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/pkg/llm"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

func currentContextMessage() *schema.Message {
	m := schema.UserMessage("current question")
	m.Extra = map[string]any{"current_context_turn": true}
	return m
}

type interruptedSummary struct {
	*scriptedChatModel
	failures int
	calls    int
}

func (m *interruptedSummary) Generate(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls <= m.failures {
		return nil, fmt.Errorf("transport: %w", io.ErrUnexpectedEOF)
	}
	return m.scriptedChatModel.Generate(ctx, messages, opts...)
}

func TestTransientSummaryRecoveryAt256K(t *testing.T) {
	for _, failures := range []int{1, 2} {
		raw := &interruptedSummary{scriptedChatModel: newScriptedChatModel(schema.AssistantMessage("handoff saved", nil)), failures: failures}
		state := &ContextWindow{Capability: llm.WindowCapability{Total: 256000}}
		saves := 0
		state.Save = func(_ context.Context, _ string, notes string, meta aiops.ContextCompaction) error {
			saves++
			if failures == 2 && (meta.Mode != "excerpt" || !strings.Contains(notes, "例外 NR-583A")) {
				t.Fatalf("missing middle exception: %s", notes)
			}
			if failures == 1 && meta.Mode != "summary" {
				t.Fatal(meta)
			}
			return nil
		}
		msg := schema.UserMessage(strings.Repeat("采样", 33333) + "例外 NR-583A" + strings.Repeat("采样", 33333))
		msg.Extra = map[string]any{"context_row": "86fa0760-2504-408a-a742-bc53cbd4622f"}
		m := windowModel{raw: raw, inner: raw, window: state}
		_, _, u, err := m.prepare(context.Background(), []*schema.Message{msg, currentContextMessage()}, nil)
		if err != nil || raw.calls != 2 || saves != 1 || u.LastCompaction == nil || u.LastCompaction.After != u.Estimated || u.Status != "compacted" {
			t.Fatalf("calls=%d saves=%d usage=%+v err=%v", raw.calls, saves, u, err)
		}
	}
}

func TestSummaryRetrySkipsRejectedAndRateLimitedRequests(t *testing.T) {
	for _, status := range []int{400, 401, 403, 429} {
		raw := newScriptedChatModel()
		upstream := &openai.APIError{HTTPStatusCode: status}
		raw.st.genErr = upstream
		m := &windowModel{raw: raw, window: &ContextWindow{}}
		_, err := m.generateSummary(context.Background(), []*schema.Message{schema.UserMessage("handoff")}, nil)
		if !errors.Is(err, upstream) || raw.st.generateCalls.Load() != 1 {
			t.Fatalf("HTTP %d: calls=%d err=%v", status, raw.st.generateCalls.Load(), err)
		}
	}
}

func TestContextCompactsOnceAcrossModelSteps(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("User wants to diagnose disk errors.", nil))
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 6000}}
	saves := 0
	state.Save = func(_ context.Context, id, summary string, _ aiops.ContextCompaction) error {
		saves++
		if id == "" || !strings.Contains(summary, "disk") {
			t.Fatal(id, summary)
		}
		return nil
	}
	m := &windowModel{raw: raw, inner: raw, window: state}
	var in []*schema.Message
	in = append(in, schema.SystemMessage("policy"))
	for i := 0; i < 60; i++ {
		u := schema.UserMessage(fmt.Sprintf("investigation %02d: %s", i, strings.Repeat("log ", 60)))
		u.Extra = map[string]any{"context_row": fmt.Sprint(i)}
		a := schema.AssistantMessage("completed", nil)
		a.ReasoningContent = "PRIVATE_REASONING"
		in = append(in, u, a)
	}
	in = append(in, schema.UserMessage("<system-reminder>constraints</system-reminder>"), currentContextMessage())
	view, opts, u, err := m.prepare(context.Background(), in, nil)
	if err != nil || saves != 1 || u.Estimated > u.InputBudget || u.Before <= u.Estimated || len(view) >= len(in) {
		t.Fatalf("prepare: usage=%+v saves=%d err=%v", u, saves, err)
	}
	if model.GetCommonOptions(&model.Options{}, opts...).MaxTokens == nil || view[0].Role != schema.System || view[len(view)-1] != in[len(in)-1] {
		t.Fatal("lost current turn or output reserve")
	}
	for _, batch := range raw.st.inputsLog {
		for _, v := range batch {
			if strings.Contains(v.Content, "PRIVATE_REASONING") {
				t.Fatal("private reasoning in summary request")
			}
		}
	}
	calls := raw.st.generateCalls.Load()
	_, _, _, err = m.prepare(context.Background(), in, nil)
	if err != nil || raw.st.generateCalls.Load() != calls || saves != 1 {
		t.Fatalf("second step repeated compaction: %v", err)
	}
	if len(in) != 123 || !strings.Contains(in[1].Content, "00") {
		t.Fatal("source history changed")
	}
}

func TestContextCompactsCompleteStepsOnly(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("Earlier tool results reviewed.", nil))
	m := &windowModel{raw: raw, inner: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 5000}}}
	in := []*schema.Message{schema.SystemMessage("policy"), currentContextMessage()}
	for i := 0; i < 10; i++ {
		id := fmt.Sprint(i)
		in = append(in, schema.AssistantMessage("", []schema.ToolCall{{ID: id, Function: schema.FunctionCall{Name: "read"}}}), schema.ToolMessage(strings.Repeat("hello ", 230), id))
	}
	got, _, u, e := m.prepare(context.Background(), in, nil)
	if e != nil || m.window.stepCut == 0 || u.Before <= u.Estimated || got[1] != in[1] || got[len(got)-1] != in[len(in)-1] {
		t.Fatalf("steps: %+v %v", u, e)
	}
	for j := 3; j < len(got); j += 2 {
		if len(got[j].ToolCalls) != 1 || got[j].ToolCalls[0].ID != got[j+1].ToolCallID {
			t.Fatalf("detached tool at %d", j)
		}
	}
}

func TestContextToolReferenceAndOversizedCurrentInput(t *testing.T) {
	s := strings.Repeat("sample ", 900)
	text := fmt.Sprintf(`{"kind":"tool_result","result_id":"e659aa59-7b05-4e83-9bf4-d214996de221","storage_status":"ready","capture_status":"partial","preview":%q}`, s)
	msg := schema.ToolMessage(text, "call")
	ref := savedView(msg)
	if ref == msg || strings.Contains(ref.Content, s) || !strings.Contains(ref.Content, "partial") || msg.Content != text {
		t.Fatal("evidence projection lost semantics")
	}
	raw := newScriptedChatModel(schema.AssistantMessage("unused", nil))
	m := &windowModel{inner: raw, raw: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 1024}}}
	user := currentContextMessage()
	user.Content = s
	_, _, u, e := m.prepare(context.Background(), []*schema.Message{user}, nil)
	if e == nil || raw.st.generateCalls.Load() != 0 || u.Status != "over_limit" {
		t.Fatalf("oversized user request: %+v %v", u, e)
	}
}

func TestContextRejectionRetriesOnlyBeforeGeneration(t *testing.T) {
	ctx := context.Background()
	raw := newScriptedChatModel(schema.AssistantMessage("Previous findings.", nil))
	history := []*schema.Message{schema.SystemMessage("policy")}
	for i := 0; i < 12; i++ {
		history = append(history, schema.UserMessage(strings.Repeat("old ", 160)), schema.AssistantMessage("done", nil))
	}
	history = append(history, currentContextMessage())
	for _, test := range []struct {
		name  string
		err   error
		calls int
	}{
		{"explicit limit", &openai.APIError{HTTPStatusCode: 400, Code: "context_length_exceeded", Message: "maximum context length is 2048 tokens"}, 2},
		{"unknown failure", errors.New("connection lost"), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := &windowModel{inner: raw, raw: raw, window: &ContextWindow{Capability: llm.WindowCapability{Total: 16000}}}
			calls := 0
			_, _, err := windowCall(ctx, m, history, nil, func(_ context.Context, msgs []*schema.Message, _ ...model.Option) (string, error) {
				calls++
				if calls == 1 {
					return "", test.err
				}
				if len(msgs) >= len(history) {
					t.Fatal("same request retried")
				}
				return "done", nil
			})
			if calls != test.calls || (calls == 2 && err != nil) || (calls == 1 && !errors.Is(err, test.err)) {
				t.Fatalf("calls=%d, error=%v", calls, err)
			}
		})
	}
}

func TestRejectedSummaryDoesNotAdvanceCursor(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("invented d533978a-3423-447c-99b0-9b696977ecb2", nil))
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 4096}, Save: func(context.Context, string, string, aiops.ContextCompaction) error {
		t.Fatal("invalid summary persisted")
		return nil
	}}
	m := &windowModel{inner: raw, raw: raw, window: state}
	in := []*schema.Message{schema.SystemMessage("policy"), schema.UserMessage(strings.Repeat("log ", 2100)), currentContextMessage()}
	_, _, u, e := m.prepare(context.Background(), in, nil)
	if e != nil || u.Status != "deferred" || state.historyCut != 0 || state.Summary != "" {
		t.Fatalf("failed summary: %+v, %v", u, e)
	}
}

func TestDurableContextCompactionAt256K(t *testing.T) {
	const turnID = "dd2960a7-739d-4017-87e6-f74a79b07284"
	const resultID = "0958a974-44f8-4e1d-99cf-e1c060ddebba"
	const invented = "71fb1a41-8fa0-4140-9ed0-cad30df46ea7"
	const prior = "Approval is pending."
	for _, tc := range []struct {
		name     string
		reply    *schema.Message
		modelErr error
		fallback bool
	}{
		{name: "complete", reply: schema.AssistantMessage("Host online; inspect the saved incident list.", nil)},
		{name: "provider incomplete", modelErr: fmt.Errorf("provider: %w", llm.ErrIncompleteResponse), fallback: true},
		{name: "exhausted output", reply: &schema.Message{Content: "truncated " + invented, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{CompletionTokens: 8192}}}, fallback: true},
		{name: "unverified evidence", reply: schema.AssistantMessage(invented, nil), fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newScriptedChatModel(tc.reply)
			raw.st.genErr = tc.modelErr
			state := &ContextWindow{Capability: llm.WindowCapability{Total: 1050000}, Summary: prior}
			var saved string
			state.Save = func(_ context.Context, boundary, notes string, meta aiops.ContextCompaction) error {
				if (meta.Mode == "excerpt") != tc.fallback || meta.Before <= meta.After || meta.SavedAt.IsZero() {
					t.Fatalf("incorrect saved checkpoint metadata: %+v", meta)
				}
				saved = notes
				return nil
			}
			model := &windowModel{raw: raw, inner: raw, window: state}
			source := schema.UserMessage("Investigate CPU. " + strings.Repeat("采样", 66000) + " Keep the node online.")
			source.Extra = map[string]any{"context_row": turnID}
			call := schema.AssistantMessage("", []schema.ToolCall{{ID: "call_1", Function: schema.FunctionCall{Name: "query_incidents"}}})
			call.ReasoningContent = "private reasoning"
			result := schema.ToolMessage(`{"open":0,"acknowledged":4}`, "call_1")
			result.ToolName = "query_incidents"
			result.Extra = map[string]any{"result_id": resultID}
			current := currentContextMessage()
			view, _, usage, err := model.prepare(context.Background(), []*schema.Message{source, call, result, current}, nil)
			if limit := raw.st.lastMaxTokens; limit == nil || *limit != usage.OutputReserve {
				t.Fatalf("summary did not receive the reserved completion budget: %v", limit)
			}
			if err != nil || usage.Window != 256000 || usage.Status != "compacted" || usage.After >= usage.Before ||
				len(view) != 2 || view[1] != current || saved != state.Summary || !strings.Contains(saved, resultID) ||
				strings.Contains(saved, invented) || strings.Contains(saved, call.ReasoningContent) {
				t.Fatalf("compaction: %+v err=%v notes=%q", usage, err, saved)
			}
			if tc.fallback {
				for _, want := range []string{prior, turnID, "Keep the node online.", "Archived conversation excerpts"} {
					if !strings.Contains(saved, want) {
						t.Fatalf("missing %q in archived notes", want)
					}
				}
			}
		})
	}
}

func TestArchivedExcerptsRequireSuccessfulCheckpoint(t *testing.T) {
	const previous = "Keep previous decisions."
	source := schema.UserMessage(strings.Repeat("disk sample ", 750))
	source.Extra = map[string]any{"context_row": "0ac81d0d-f936-42c8-b9d2-cac19041d4db"}
	raw := newScriptedChatModel()
	raw.st.genErr = llm.ErrIncompleteResponse
	saves := 0
	state := &ContextWindow{Capability: llm.WindowCapability{Total: 4096}, Summary: previous}
	state.Save = func(context.Context, string, string, aiops.ContextCompaction) error {
		saves++
		return errors.New("checkpoint superseded")
	}
	m := &windowModel{raw: raw, window: state}
	in := []*schema.Message{source, currentContextMessage()}
	_, _, usage, err := m.prepare(context.Background(), in, nil)
	if err != nil || saves != 1 || usage.Status != "deferred" || state.historyCut != 0 || state.Summary != previous {
		t.Fatalf("lost history on failed save: %+v, err=%v, saves=%d", usage, err, saves)
	}
}

func TestHistoricalDraftDoesNotPinLaterExchanges(t *testing.T) {
	draft := schema.ToolMessage(`{"kind":"config_draft","draft_hash":"sha256:pending"}`, "draft")
	call := schema.AssistantMessage("", []schema.ToolCall{{ID: "draft", Function: schema.FunctionCall{Name: "draft_config_change"}}})
	request := schema.UserMessage(strings.Repeat("巡检", 85000))
	for _, tc := range []struct {
		name       string
		following  []*schema.Message
		wantPinned bool
	}{
		{"awaiting next confirmation", nil, true},
		{"user cancelled", []*schema.Message{schema.UserMessage("取消草案"), schema.AssistantMessage("已取消。", nil)}, false},
		{"apply completed", []*schema.Message{schema.UserMessage("确认创建"), schema.AssistantMessage("配置已应用。", nil)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := newScriptedChatModel(schema.AssistantMessage("The draft was discussed.", nil), schema.AssistantMessage("The draft was discussed.", nil))
			w := &ContextWindow{Selected: 256000, Capability: llm.WindowCapability{Total: 256000}}
			m := windowModel{inner: raw, raw: raw, window: w}
			in := []*schema.Message{request, call, draft}
			in = append(in, tc.following...)
			_, _, u, err := m.prepare(t.Context(), append(in, currentContextMessage()), nil)
			if tc.wantPinned {
				if err == nil || w.historyCut != 0 || u.Status != "over_limit" {
					t.Fatal(u, err)
				}
			} else if err != nil || w.historyCut < 3 || u.Status != "compacted" {
				t.Fatal(u, err)
			}
		})
	}
}

func TestRepeatedCheckpointsKeepFirstToolReference(t *testing.T) {
	const original = "0958a974-44f8-4e1d-99cf-e1c060ddebba"
	previous := ""
	for cycle := 0; cycle < 18; cycle++ {
		raw := newScriptedChatModel(schema.AssistantMessage("The inventory was checked.", nil))
		if cycle > 1 {
			raw.st.genErr = llm.ErrIncompleteResponse
		}
		saved := ""
		w := &ContextWindow{Capability: llm.WindowCapability{Total: 256000}, Summary: previous, Save: func(_ context.Context, _ string, notes string, _ aiops.ContextCompaction) error {
			saved = notes
			return nil
		}}
		m := windowModel{inner: raw, raw: raw, window: w}
		user := schema.UserMessage(strings.Repeat("巡检", 66000))
		user.Extra = map[string]any{"context_row": fmt.Sprintf("0ac81d0d-f936-42c8-b9d2-%012d", cycle)}
		in := []*schema.Message{user}
		if cycle == 0 {
			call := schema.AssistantMessage("", []schema.ToolCall{{ID: "a", Function: schema.FunctionCall{Name: "query_devices"}}})
			result := schema.ToolMessage("inventory", "a", schema.WithToolName("query_devices"))
			result.Extra = map[string]any{"result_id": original}
			in = append(in, call, result)
		}
		_, _, u, err := m.prepare(t.Context(), append(in, currentContextMessage()), nil)
		if err != nil || u.Status != "compacted" || !strings.Contains(saved, "query_devices: "+original) || u.LastCompaction == nil {
			t.Fatalf("cycle %d: result reference lost or unsaved: %+v %v", cycle, u, err)
		}
		if cycle > 1 && (u.LastCompaction.Mode != "excerpt" || !strings.Contains(saved, user.Extra["context_row"].(string))) {
			t.Fatalf("cycle %d: missing fallback or latest pointer", cycle)
		}
		previous = saved
	}
}

func TestResultIndexDoesNotConsumeLegacyProse(t *testing.T) {
	legacy := "Saved handoff" + resultReferenceHeader + "The primary dashboard is unavailable. Check again at 19:00."
	const id = "0958a974-44f8-4e1d-99cf-e1c060ddebba"
	for _, input := range []string{legacy, legacy + resultReferenceHeader + "query_devices: " + id} {
		notes, refs := splitResultReferences(input)
		if notes != legacy || (input == legacy) != (refs == "") {
			t.Fatal(notes, refs)
		}
	}
}

func TestExplicitLimitRetriesShortInputWithSmallerOutput(t *testing.T) {
	raw := newScriptedChatModel()
	m := &windowModel{inner: raw, raw: raw, window: &ContextWindow{}}
	input := []*schema.Message{schema.SystemMessage("rules"), currentContextMessage()}
	calls := 0
	got, u, err := windowCall(t.Context(), m, input, nil, func(_ context.Context, msgs []*schema.Message, opts ...model.Option) (string, error) {
		calls++
		out := *model.GetCommonOptions(&model.Options{}, opts...).MaxTokens
		if calls == 1 {
			return "", &openai.APIError{HTTPStatusCode: 400, Code: "context_length_exceeded", Message: "maximum context length is 4096 tokens"}
		}
		if len(msgs) != len(input) || out != 512 {
			t.Fatalf("retry: %d messages, %d output tokens", len(msgs), out)
		}
		return "ok", nil
	})
	_, want, _ := (llm.WindowCapability{Total: 4096}).Budget(0)
	if err != nil || got != "ok" || calls != 2 || u.InputBudget != want {
		t.Fatalf("calls=%d usage=%+v err=%v", calls, u, err)
	}
}

type savedTestTool struct {
	*fakeBaseTool
	id string
}

func (t *savedTestTool) InvokableRun(ctx context.Context, args string, opts ...basetool.InvokeOption) (string, error) {
	toolcapture.Link(ctx, t.id)
	return t.fakeBaseTool.InvokableRun(ctx, args, opts...)
}

func TestLiveToolLinksSurviveConsecutiveCompactionAt256K(t *testing.T) {
	const firstID = "7ad95cd8-86e5-4aae-984f-80c580d43531"
	const secondID = "9442aef2-b08f-433c-9f03-823560551c40"
	first := &savedTestTool{&fakeBaseTool{name: "query_devices", class: "read", runResp: `{"device_id":72}`}, firstID}
	second := &savedTestTool{&fakeBaseTool{name: "get_metrics", class: "read", runResp: strings.Repeat("采", 132000)}, secondID}
	// Some providers reuse call IDs on subsequent model steps.
	model := newScriptedChatModel(
		makeAssistantToolCall(strings.Repeat("Inventory scope. ", 100), "call_0", "query_devices", "{}"),
		makeAssistantToolCall("", "call_0", "get_metrics", `{"page":1}`),
		makeAssistantNoTools("Checked inventory."),
		makeAssistantToolCall("", "call_0", "get_metrics", `{"page":2}`),
		makeAssistantNoTools("Checked inventory and the first metrics page."),
		makeAssistantNoTools("done"),
	)
	window := &ContextWindow{Capability: llm.WindowCapability{Total: 256000}}
	graph, err := BuildReActGraph(model, []basetool.BaseTool{first, second}, Config{Context: window, MaxIterations: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := graph.Invoke(t.Context(), &Input{UserText: "Check the devices and metrics"}); err != nil {
		t.Fatal(err)
	}
	if window.stepCut != 4 {
		t.Fatalf("compacted %d messages, want two complete exchanges", window.stepCut)
	}
	last := model.st.inputsLog[len(model.st.inputsLog)-1]
	var notes string
	for _, msg := range last {
		if msg.Content == first.runResp {
			t.Fatal("first tool was still in raw context")
		}
		if strings.Contains(msg.Content, resultReferenceHeader) {
			notes = msg.Content
		}
	}
	if !strings.Contains(notes, "query_devices: "+firstID) || !strings.Contains(notes, "get_metrics: "+secondID) {
		t.Fatalf("missing stored links in handoff: %s", notes)
	}
}

func TestCurrentToolIndexIncludesMiddleResultsAndUpdatesAfterLookup(t *testing.T) {
	var live liveToolResults
	ctx := context.WithValue(t.Context(), liveResultsKey{}, &live)
	var body []*schema.Message
	var middle string
	for i := 0; i < 48; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		name := "query_devices"
		if i%2 == 1 {
			name = "get_host_summary"
		}
		live.pending.Store("call_0", aiops.ArchivedToolReference{ResultID: id, ToolName: name})
		result := schema.ToolMessage("ok", "call_0")
		result.ToolName = name
		body = append(body, makeAssistantToolCall("", "call_0", name, "{}"), result)
		live.bind(body)
		if i == 22 {
			middle = id
		}
	}
	notes := withResultReferences("notes", "", live.forSummary(body, 0))
	if strings.Contains(notes, middle) {
		t.Fatal("test needs a result outside the bounded summary")
	}
	first, err := CurrentToolResults(ctx, "query_devices", 0)
	if err != nil || len(first.Results) != 16 || first.Next != 16 || first.Results[11].ResultID != middle {
		t.Fatalf("first page: %+v %v", first, err)
	}
	last, err := CurrentToolResults(ctx, "query_devices", first.Next)
	if err != nil || len(last.Results) != 8 || last.Next != 0 || last.Results[0].ResultID == first.Results[15].ResultID {
		t.Fatalf("last page: %+v %v", last, err)
	}
	other, err := CurrentToolResults(t.Context(), "", 0)
	if err != nil || len(other.Results) != 0 {
		t.Fatal("result crossed graph invocations")
	}
}

func TestStoredStepsReduceInputAcrossTwoCompactionsAt256K(t *testing.T) {
	raw := newScriptedChatModel()
	w := &ContextWindow{Capability: llm.WindowCapability{Total: 256000}}
	m := &windowModel{window: w, raw: raw, tools: []*schema.ToolInfo{{Name: "read_tool_result"}}}
	current := currentContextMessage()
	current.Content = strings.Repeat("巡", 131000)
	in := []*schema.Message{current}
	for i := 0; i < 3; i++ {
		name := "get_host_summary"
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		args := fmt.Sprintf(`{"device_id":%d}`, i+71)
		call := makeAssistantToolCall("Keep host in observation.", "same", name, args)
		call.ReasoningContent = "PRIVATE_REASONING"
		result := schema.ToolMessage(strings.Repeat("sample ", 2000), "same")
		result.ToolName = name
		w.liveResults.pending.Store("same", aiops.ArchivedToolReference{ResultID: id, ToolName: name})
		in = append(in, call, result)
		view, _, usage, err := m.prepare(t.Context(), in, nil)
		if err != nil || view[len(view)-1] != result {
			t.Fatalf("lost current result: %+v %v", usage, err)
		}
		if i == 0 {
			continue
		}
		if w.stepCut != i*2 || usage.Window != 256000 || usage.Before-usage.After < 3000 || raw.st.generateCalls.Load() != 0 {
			t.Fatalf("step %d: cut=%d usage=%+v model calls=%d", i, w.stepCut, usage, raw.st.generateCalls.Load())
		}
		for j := 0; j < i; j++ {
			if !strings.Contains(w.stepSummary, fmt.Sprintf(`{"device_id":%d}`, 71+j)) || !strings.Contains(w.stepSummary, fmt.Sprintf("00000000-0000-4000-8000-%012d", j)) {
				t.Fatal("lost an earlier call or storage link")
			}
		}
		if !strings.Contains(w.stepSummary, "Keep host in observation.") || strings.Contains(w.stepSummary, "PRIVATE_REASONING") {
			t.Fatal("incorrect visible assistant notes")
		}
	}
}

func TestStoredStepsRequireCompleteEvidence(t *testing.T) {
	id := "00000000-0000-4000-8000-000000000009"
	call := makeAssistantToolCall("", "call", "get_host_summary", "{}")
	for _, tc := range []struct{ body, id string }{
		{`{"device_id":7}`, ""},
		{`{"status":"failed","error":"no sample"}`, id},
		{fmt.Sprintf(`{"kind":"tool_result","result_id":%q,"capture_status":"partial","storage_status":"ready"}`, id), id},
		{fmt.Sprintf(`{"kind":"tool_result","result_id":%q,"capture_status":"complete","storage_status":"pending"}`, id), id},
	} {
		result := schema.ToolMessage(tc.body, "call")
		result.Extra = map[string]any{"result_id": tc.id}
		if _, ok := storedStepNotes("", []*schema.Message{call, result}); ok {
			t.Fatalf("accepted incomplete evidence: %s", tc.body)
		}
	}
}

func TestCheckpointWriteFailureStillCompactsRecoverableLiveSteps(t *testing.T) {
	raw := newScriptedChatModel(schema.AssistantMessage("Historical observations.", nil))
	w := &ContextWindow{
		Capability: llm.WindowCapability{Total: 256000},
		Save: func(context.Context, string, string, aiops.ContextCompaction) error {
			return errors.New("unavailable checkpoint store")
		},
	}
	m := &windowModel{raw: raw, window: w, tools: []*schema.ToolInfo{{Name: "read_tool_result"}}}
	user := currentContextMessage()
	user.Content = strings.Repeat("巡", 130000)
	first := schema.ToolMessage(strings.Repeat("sample ", 4000), "first")
	first.ToolName = "query_devices"
	id := "00000000-0000-4000-8000-000000000001"
	w.liveResults.byPosition = map[int]aiops.ArchivedToolReference{1: {ResultID: id, ToolName: first.ToolName}}
	old := schema.UserMessage(strings.Repeat("prior notes ", 160))
	in := []*schema.Message{old, schema.AssistantMessage("Noted.", nil), user,
		makeAssistantToolCall("", "first", "query_devices", "{}"), first,
		makeAssistantToolCall("", "latest", "get_host_summary", "{}"), schema.ToolMessage("fresh sample", "latest"),
	}
	view, _, u, err := m.prepare(t.Context(), in, nil)
	if err != nil || w.historyCut != 0 || w.stepCut != 2 || view[0] != old || view[len(view)-1] != in[len(in)-1] || !strings.Contains(w.stepSummary, id) || raw.st.generateCalls.Load() != 1 || u.Estimated >= u.InputBudget || u.Status != "deferred" || u.Before <= u.After || u.After != u.Estimated {
		t.Fatalf("live cut=%d history cut=%d usage=%+v err=%v", w.stepCut, w.historyCut, u, err)
	}
}
