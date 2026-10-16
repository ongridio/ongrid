package callbacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// captureLogger writes JSON log lines into an in-memory buffer the test
// can grep. We use slog.NewJSONHandler so attribute names are stable.
func captureLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h), buf
}

func TestAuditHandler_NewNilLogger(t *testing.T) {
	t.Parallel()
	if NewAuditHandler(AuditDeps{}) != nil {
		t.Fatalf("expected nil handler when logger is nil")
	}
}

func TestAuditHandler_ChatModelStartAndEnd(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	h := NewAuditHandler(AuditDeps{Logger: log, SessionID: "sess-1", UserID: 42})
	ctx := context.Background()
	in := &einomodel.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: "hello world"}},
	}
	h.OnStart(ctx, chatModelInfo(), in)
	out := &einomodel.CallbackOutput{
		Message:    &schema.Message{Role: schema.Assistant, Content: "ok"},
		TokenUsage: &einomodel.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
	h.OnEnd(ctx, chatModelInfo(), out)

	lines := splitLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines got %d: %v", len(lines), lines)
	}

	startLine := decodeJSON(t, lines[0])
	if startLine["msg"] != "graph stage start" {
		t.Errorf("msg = %v, want graph stage start", startLine["msg"])
	}
	if startLine["session_id"] != "sess-1" {
		t.Errorf("session_id = %v", startLine["session_id"])
	}
	if startLine["kind"] != "chat_model" {
		t.Errorf("kind = %v", startLine["kind"])
	}
	if iter, ok := startLine["iteration"].(float64); !ok || iter != 1 {
		t.Errorf("iteration = %v", startLine["iteration"])
	}

	endLine := decodeJSON(t, lines[1])
	if endLine["status"] != "success" {
		t.Errorf("status = %v", endLine["status"])
	}
	usage, ok := endLine["token_usage"].(map[string]any)
	if !ok {
		t.Fatalf("token_usage missing: %v", endLine)
	}
	if usage["total"] != float64(8) {
		t.Errorf("total tokens = %v", usage["total"])
	}
}

func TestAuditHandler_ToolError(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	h := NewAuditHandler(AuditDeps{Logger: log, SessionID: "s1"})
	ctx := WithToolCallID(context.Background(), "tc-1")
	h.OnStart(ctx, toolInfo("flaky"), &einotool.CallbackInput{ArgumentsInJSON: `{"x":1}`})
	h.OnError(ctx, toolInfo("flaky"), errors.New("boom"))

	lines := splitLines(buf.String())
	if len(lines) < 2 {
		t.Fatalf("expected start+end lines, got %d", len(lines))
	}
	end := decodeJSON(t, lines[len(lines)-1])
	if end["status"] != "error" {
		t.Errorf("status = %v", end["status"])
	}
	if end["error"] != "boom" {
		t.Errorf("error = %v", end["error"])
	}
}

func TestAuditHandler_DoesNotLogUserText(t *testing.T) {
	t.Parallel()
	// / 红线: user text must never appear in audit logs.
	const sensitive = "私密文本-DO-NOT-LOG"
	log, buf := captureLogger()
	h := NewAuditHandler(AuditDeps{Logger: log, SessionID: "s"})
	in := &einomodel.CallbackInput{
		Messages: []*schema.Message{{Role: schema.User, Content: sensitive}},
	}
	h.OnStart(context.Background(), chatModelInfo(), in)
	h.OnEnd(context.Background(), chatModelInfo(), &einomodel.CallbackOutput{
		Message: &schema.Message{Role: schema.Assistant, Content: "reply"},
	})
	if strings.Contains(buf.String(), sensitive) {
		t.Fatalf("audit log leaked user text: %s", buf.String())
	}
}

func TestAuditHandler_TimeoutClassified(t *testing.T) {
	t.Parallel()
	log, buf := captureLogger()
	h := NewAuditHandler(AuditDeps{Logger: log, SessionID: "s"})
	ctx := WithToolCallID(context.Background(), "tc-x")
	h.OnStart(ctx, toolInfo("slow"), &einotool.CallbackInput{})
	h.OnError(ctx, toolInfo("slow"), context.DeadlineExceeded)
	end := decodeJSON(t, splitLines(buf.String())[1])
	if end["status"] != "timeout" {
		t.Errorf("status = %v, want timeout", end["status"])
	}
}

// helpers --------------------------------------------------

func splitLines(s string) []string {
	out := []string{}
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode line: %v\nline=%s", err, s)
	}
	return m
}

func TestAuditHandler_NeededGating(t *testing.T) {
	t.Parallel()
	log, _ := captureLogger()
	h := NewAuditHandler(AuditDeps{Logger: log})
	if !h.Needed(context.Background(), chatModelInfo(), callbacks.TimingOnStart) {
		t.Errorf("ChatModel OnStart should be needed")
	}
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnEnd) {
		t.Errorf("Tool OnEnd should be needed")
	}
	if !h.Needed(context.Background(), toolInfo("t"), callbacks.TimingOnError) {
		t.Errorf("Tool OnError should be needed")
	}
}
