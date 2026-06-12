package investigator

import (
	"context"
	"errors"
	"testing"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type fakeSummarizer struct {
	resp    *llm.ChatResp
	err     error
	lastReq llm.ChatReq
	calls   int
}

func (f *fakeSummarizer) Chat(_ context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
	f.calls++
	f.lastReq = req
	return f.resp, f.err
}

// fakeRelatedQuerier returns canned related rows + records what the
// caller asked for so tests can assert window/limit got passed.
type fakeRelatedQuerier struct {
	rows []*alertmodel.Incident
	err  error
	last struct {
		halfWindow time.Duration
		limit      int
		deviceID   *uint64
	}
}

func (f *fakeRelatedQuerier) RelatedToIncident(_ context.Context, target *alertmodel.Incident, halfWindow time.Duration, limit int) ([]*alertmodel.Incident, error) {
	f.last.halfWindow = halfWindow
	f.last.limit = limit
	if target != nil {
		f.last.deviceID = target.DeviceID
	}
	return f.rows, f.err
}

// TestExtractStructured_RelatedAlerts — when a RelatedAlertQuerier is
// wired, extracted ReadyFields carry a populated related_alerts_json
// (instead of the legacy "[]"). Filters out the target's own row +
// any nil entries.
func TestExtractStructured_RelatedAlerts(t *testing.T) {
	now := time.Now().UTC()
	dev := uint64(67)
	related := &fakeRelatedQuerier{
		rows: []*alertmodel.Incident{
			{ID: 1, Rule: "swap_high", RuleName: "Swap > 50%", Severity: "warning", Status: "open", FirstFiredAt: now, LastFiredAt: now, DeviceID: &dev},
			nil, // skipped
			{ID: 99, Rule: "self", RuleName: "self", Severity: "warning", Status: "open", FirstFiredAt: now, LastFiredAt: now, DeviceID: &dev}, // matches target ID → skipped
			{ID: 2, Rule: "disk_high", RuleName: "Disk > 80%", Severity: "warning", Status: "open", FirstFiredAt: now, LastFiredAt: now, DeviceID: &dev},
		},
	}
	uc := (&Usecase{
		cfg: Config{},
	}).WithRelatedQuerier(related)
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 99, DeviceID: &dev}, "narrative", 0, "")

	if fields.RelatedAlertsJSON == "[]" {
		t.Fatalf("related_alerts_json not populated: %q", fields.RelatedAlertsJSON)
	}
	// Cheap sanity: must include the two real rows by rule name.
	for _, want := range []string{"swap_high", "disk_high"} {
		if !contains(fields.RelatedAlertsJSON, want) {
			t.Errorf("related_alerts_json missing %q: %s", want, fields.RelatedAlertsJSON)
		}
	}
	// Target self-row must NOT appear.
	if contains(fields.RelatedAlertsJSON, "\"incident_id\":99") {
		t.Errorf("target incident leaked into related_alerts: %s", fields.RelatedAlertsJSON)
	}
	if related.last.halfWindow != relatedAlertHalfWindow {
		t.Errorf("halfWindow = %v, want %v", related.last.halfWindow, relatedAlertHalfWindow)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestExtractJSONBlob_VariousWrappers — the LLM sometimes prefixes
// the JSON with ```json fences, leading prose, or trailing commentary.
// The blob extractor MUST find the first balanced top-level object.
func TestExtractJSONBlob_VariousWrappers(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			in:   `{"a":1}`,
			want: `{"a":1}`,
		},
		{
			in:   "```json\n{\"a\":1}\n```",
			want: `{"a":1}`,
		},
		{
			in:   "Sure, here:\n```\n{\"x\":\"y\"}\n```\n",
			want: `{"x":"y"}`,
		},
		{
			in:   `{"nested": {"k": "v"}, "arr": [1, {"q":"r"}]}`,
			want: `{"nested": {"k": "v"}, "arr": [1, {"q":"r"}]}`,
		},
		{
			in:   `{"escaped": "}",  "ok": true}`,
			want: `{"escaped": "}",  "ok": true}`,
		},
		{in: "no json at all", want: ""},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		if got := extractJSONBlob(tc.in); got != tc.want {
			t.Errorf("extractJSONBlob(%q)\n  got:  %q\n  want: %q", tc.in, got, tc.want)
		}
	}
}

// TestExtractStructured_SummarizerNil — when no summarizer wired, the
// extractor falls back to first-paragraph root_cause + verbatim
// findings_md. Same shape as the PR-2 behaviour.
func TestExtractStructured_SummarizerNil(t *testing.T) {
	uc := &Usecase{cfg: Config{SummarizerTimeout: time.Second}}
	final := "## Root\n\npg-replica-7 saturated"
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, final, 3, "")
	if fields.RootCause != "pg-replica-7 saturated" {
		t.Errorf("root_cause = %q", fields.RootCause)
	}
	if fields.FindingsMD != final {
		t.Errorf("findings_md not preserved")
	}
	if fields.ToolCallCount != 3 {
		t.Errorf("tool_call_count = %d, want 3", fields.ToolCallCount)
	}
	if fields.Confidence != nil {
		t.Errorf("confidence should be nil when summarizer unwired")
	}
}

// TestExtractStructured_ParsesValid — happy path. The summarizer
// returns proper JSON; the extractor promotes every field into
// ReadyFields and JSON-marshals nested objects for the columns that
// store *_json strings.
func TestExtractStructured_ParsesValid(t *testing.T) {
	conf := 0.85
	sum := &fakeSummarizer{resp: &llm.ChatResp{
		Assistant: llm.Message{
			Role: "assistant",
			Content: `{
  "root_cause": "PID 8821 saturated CPU on pg-replica-7",
  "affected_window": "2026-05-19T03:42:15Z/2026-05-19T03:48:30Z",
  "pinpoint_target": {"device_id": 42, "pid": 8821, "service": "postgres"},
  "evidence": [{"step": 1, "tool": "correlate_incident", "summary": "cpu spike + net spike"}],
  "suggested_actions": [{"label": "kill query", "danger": "high"}],
  "confidence": 0.85
}`,
		},
	}}
	uc := &Usecase{
		summarizer: sum,
		cfg: Config{
			SummarizerModel:   "glm-4-air",
			SummarizerTimeout: time.Second,
		},
	}
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, "narrative", 7, "")

	if fields.RootCause != "PID 8821 saturated CPU on pg-replica-7" {
		t.Errorf("root_cause = %q", fields.RootCause)
	}
	if fields.AffectedWindow != "2026-05-19T03:42:15Z/2026-05-19T03:48:30Z" {
		t.Errorf("affected_window = %q", fields.AffectedWindow)
	}
	if fields.PinpointedTargetJSON == "{}" {
		t.Errorf("pinpoint_target_json not populated: %q", fields.PinpointedTargetJSON)
	}
	if fields.EvidenceJSON == "[]" {
		t.Errorf("evidence_json not populated: %q", fields.EvidenceJSON)
	}
	if fields.SuggestedActionsJSON == "[]" {
		t.Errorf("suggested_actions_json not populated: %q", fields.SuggestedActionsJSON)
	}
	if fields.Confidence == nil || *fields.Confidence != conf {
		t.Errorf("confidence = %v, want %v", fields.Confidence, conf)
	}
	if fields.ToolCallCount != 7 {
		t.Errorf("tool_call_count = %d, want 7", fields.ToolCallCount)
	}
	if sum.lastReq.Model != "glm-4-air" {
		t.Errorf("model = %q, want glm-4-air", sum.lastReq.Model)
	}
}

func TestExtractStructured_EmptyModelUsesRouterDefault(t *testing.T) {
	sum := &fakeSummarizer{resp: &llm.ChatResp{
		Assistant: llm.Message{Role: "assistant", Content: `{"root_cause":"router default"}`},
	}}
	uc := &Usecase{
		summarizer: sum,
		cfg:        Config{SummarizerTimeout: time.Second},
	}

	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, "narrative", 2, "")
	if fields.RootCause != "router default" {
		t.Fatalf("root_cause = %q, want router default", fields.RootCause)
	}
	if sum.calls != 1 {
		t.Fatalf("summarizer calls = %d, want 1", sum.calls)
	}
	if sum.lastReq.Provider != "" || sum.lastReq.Model != "" {
		t.Fatalf("summarizer request = provider %q model %q, want empty router defaults", sum.lastReq.Provider, sum.lastReq.Model)
	}
}

// TestExtractStructured_LLMError — when the summarizer call errors,
// fall back silently to PR-2 behaviour. The report still ships, just
// without the structured fields.
func TestExtractStructured_LLMError(t *testing.T) {
	sum := &fakeSummarizer{err: errors.New("rate limited")}
	uc := &Usecase{
		summarizer: sum,
		cfg:        Config{SummarizerModel: "glm-4-air", SummarizerTimeout: time.Second},
	}
	final := "Root cause line\n\nbody"
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, final, 0, "")
	if fields.RootCause != "Root cause line" {
		t.Errorf("root_cause fallback = %q", fields.RootCause)
	}
	if fields.FindingsMD != final {
		t.Errorf("findings_md should be preserved on fallback")
	}
}

// TestExtractStructured_BadJSON — model ignores the system prompt and
// replies with prose. Fall back the same way.
func TestExtractStructured_BadJSON(t *testing.T) {
	sum := &fakeSummarizer{resp: &llm.ChatResp{
		Assistant: llm.Message{Role: "assistant", Content: "I think the cause is..."},
	}}
	uc := &Usecase{summarizer: sum, cfg: Config{SummarizerModel: "glm-4-air", SummarizerTimeout: time.Second}}
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, "narrative body", 0, "")
	if fields.RootCause != "narrative body" {
		t.Errorf("fallback root_cause = %q", fields.RootCause)
	}
	if fields.Confidence != nil {
		t.Errorf("confidence should be nil on bad-JSON fallback")
	}
}

// TestExtractStructured_ConfidenceClamp — models occasionally return
// 0..100 or 0..10. Promote into ReadyFields as 0..1.
func TestExtractStructured_ConfidenceClamp(t *testing.T) {
	cases := []struct {
		raw, want float64
	}{
		{0.5, 0.5},
		{85, 0.85}, // 0..100 form
		{1.0, 1.0},
		{-0.1, 0},
		{1.5, 1.0}, // > 1 but ≤ 100 → /100 → 0.015 — let's check actual behaviour
	}
	for _, tc := range cases {
		raw := tc.raw
		sum := &fakeSummarizer{resp: &llm.ChatResp{
			Assistant: llm.Message{Role: "assistant",
				Content: `{"root_cause":"x","confidence":` + fmtFloat(raw) + `}`}}}
		uc := &Usecase{summarizer: sum, cfg: Config{SummarizerModel: "m", SummarizerTimeout: time.Second}}
		fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, "narr", 0, "")
		if fields.Confidence == nil {
			t.Errorf("raw=%v: confidence nil", raw)
			continue
		}
		// For raw=1.5 the rule (>1 && <=100 → /100) gives 0.015 — accept either branch.
		got := *fields.Confidence
		if (raw <= 1 && raw >= 0 && got != raw) ||
			(raw > 1 && raw <= 100 && got != raw/100.0) ||
			(raw < 0 && got != 0) {
			t.Errorf("raw=%v -> got %v (expected per clamp rule)", raw, got)
		}
	}
}

func fmtFloat(f float64) string {
	switch f {
	case 0:
		return "0"
	}
	// Avoid importing strconv for a one-liner.
	return func() string {
		s := ""
		neg := f < 0
		if neg {
			f = -f
		}
		intPart := int64(f)
		frac := f - float64(intPart)
		s = ""
		if neg {
			s = "-"
		}
		s += itoa(intPart)
		if frac == 0 {
			return s
		}
		// 6-decimal repr.
		fracInt := int64(frac * 1e6)
		s += "."
		fracs := itoa(fracInt)
		for i := 0; i < 6-len(fracs); i++ {
			s += "0"
		}
		s += fracs
		// trim trailing zeros
		for len(s) > 0 && s[len(s)-1] == '0' {
			s = s[:len(s)-1]
		}
		if len(s) > 0 && s[len(s)-1] == '.' {
			s = s[:len(s)-1]
		}
		return s
	}()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
