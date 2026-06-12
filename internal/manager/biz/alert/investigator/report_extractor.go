package investigator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

// LLMSummarizer is the narrow seam used by the report-extractor step.
// Implemented by *llm.MultiClient and any structurally-compatible test
// double. Kept narrow so tests can inject a deterministic response
// without standing up the multi-provider router.
type LLMSummarizer interface {
	Chat(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error)
}

// extractedReport is the JSON schema we ask the LLM to return. Mirrors
// the persistable ReadyFields plus a few intermediate types the model
// is asked to emit before we serialise to JSON for storage.
type extractedReport struct {
	RootCause      string           `json:"root_cause"`
	AffectedWindow string           `json:"affected_window,omitempty"`
	PinpointTarget map[string]any   `json:"pinpoint_target,omitempty"`
	Evidence       []map[string]any `json:"evidence,omitempty"`
	Suggested      []map[string]any `json:"suggested_actions,omitempty"`
	Confidence     *float64         `json:"confidence,omitempty"`
}

// extractStructured runs a single LLM call over the worker's final
// answer to produce structured ReadyFields. On any failure (LLM error,
// JSON parse error, empty answer) it falls back to the PR-2 "first
// paragraph" heuristic so the report still ships findings_md +
// root_cause to operators. The fallback is silent to keep the alert
// pipeline single-threaded happy-path simple.
func (uc *Usecase) extractStructured(ctx context.Context, incident alertmodel.Incident, finalAnswer string, toolCallCount int, locale string) ReadyFields {
	// related_alerts is independent of Pass-2 — it's a pure DB query
	// over alert_incidents, so we run it unconditionally and reuse the
	// result whether or not the LLM extraction succeeds.
	relatedJSON := uc.buildRelatedAlertsJSON(ctx, incident)

	fallback := ReadyFields{
		RootCause:             firstParagraphOneLine(finalAnswer, 200),
		AffectedWindow:        "",
		PinpointedTargetJSON:  "{}",
		RelatedAlertsJSON:     relatedJSON,
		EvidenceJSON:          "[]",
		SuggestedActionsJSON:  "[]",
		FindingsMD:            finalAnswer,
		Confidence:            nil,
		ConfidenceFactorsJSON: "{}",
		ToolCallCount:         toolCallCount,
	}

	if uc.summarizer == nil {
		return fallback
	}

	prompt := buildExtractorPrompt(incident, finalAnswer, locale)
	cctx, cancel := context.WithTimeout(ctx, uc.cfg.SummarizerTimeout)
	defer cancel()

	resp, err := uc.summarizer.Chat(cctx, llm.ChatReq{
		Model:       uc.cfg.SummarizerModel,
		Provider:    uc.cfg.SummarizerProvider,
		Temperature: 0,
		Messages: []llm.Message{
			{Role: "system", Content: extractorSystemPrompt},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		uc.logger().Info("report extractor LLM failed; falling back",
			"err", err.Error())
		return fallback
	}

	rawAnswer := strings.TrimSpace(resp.Assistant.Content)
	jsonBlob := extractJSONBlob(rawAnswer)
	if jsonBlob == "" {
		uc.logger().Info("report extractor returned no JSON; falling back",
			"answer_head", truncate(rawAnswer, 200))
		return fallback
	}

	var ex extractedReport
	if err := json.Unmarshal([]byte(jsonBlob), &ex); err != nil {
		uc.logger().Info("report extractor JSON parse failed; falling back",
			"err", err.Error(), "blob_head", truncate(jsonBlob, 200))
		return fallback
	}

	// Promote extracted fields into ReadyFields. Always carry the
	// original markdown answer as findings_md so the SPA still has
	// the human-readable narrative; the extractor adds structure on
	// top, doesn't replace.
	out := ReadyFields{
		FindingsMD:    finalAnswer,
		ToolCallCount: toolCallCount,
	}
	if ex.RootCause != "" {
		out.RootCause = clampRunes(ex.RootCause, 200)
	} else {
		out.RootCause = fallback.RootCause
	}
	out.AffectedWindow = strings.TrimSpace(ex.AffectedWindow)
	out.PinpointedTargetJSON = marshalOrDefault(ex.PinpointTarget, "{}")
	out.EvidenceJSON = marshalOrDefault(ex.Evidence, "[]")
	out.SuggestedActionsJSON = marshalOrDefault(ex.Suggested, "[]")
	// related_alerts already computed at the top of the function — DB
	// query independent of Pass-2. Reuse to avoid a second round-trip.
	out.RelatedAlertsJSON = relatedJSON

	if ex.Confidence != nil {
		// Clamp model self-report to [0, 1] — some models drift to
		// 0-100 or 0-10.
		c := *ex.Confidence
		if c > 1 && c <= 100 {
			c = c / 100.0
		}
		if c > 1 {
			c = 1
		}
		if c < 0 {
			c = 0
		}
		out.Confidence = &c
	}
	out.ConfidenceFactorsJSON = buildConfidenceFactors(ex, toolCallCount)
	return out
}

// relatedAlertWire is the per-row shape persisted into
// related_alerts_json. Kept minimal so the SPA can render a compact
// list (rule + when + status) without joining further. The full
// incident is reachable via incident_id → /v1/alerts/incidents/{id}.
type relatedAlertWire struct {
	IncidentID  uint64 `json:"incident_id"`
	Rule        string `json:"rule"`
	RuleName    string `json:"rule_name"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	FiredAt     string `json:"fired_at"`
	LastFiredAt string `json:"last_fired_at"`
}

const (
	relatedAlertHalfWindow = 5 * time.Minute
	relatedAlertLimit      = 10
)

// buildRelatedAlertsJSON queries the related-alerts seam and serialises
// the result. Returns "[]" on any failure / missing dep — RelatedAlertsJSON
// must be non-empty (the column is NOT NULL with no DEFAULT — MySQL
// Error 1101 trap).
func (uc *Usecase) buildRelatedAlertsJSON(ctx context.Context, target alertmodel.Incident) string {
	if uc.related == nil {
		return "[]"
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := uc.related.RelatedToIncident(qctx, &target, relatedAlertHalfWindow, relatedAlertLimit)
	if err != nil {
		uc.logger().Info("related_alerts query failed (non-fatal)", "err", err.Error())
		return "[]"
	}
	if len(rows) == 0 {
		return "[]"
	}
	out := make([]relatedAlertWire, 0, len(rows))
	for _, r := range rows {
		if r == nil || r.ID == target.ID {
			continue
		}
		out = append(out, relatedAlertWire{
			IncidentID:  r.ID,
			Rule:        r.Rule,
			RuleName:    r.RuleName,
			Severity:    r.Severity,
			Status:      r.Status,
			FiredAt:     r.FirstFiredAt.UTC().Format(time.RFC3339),
			LastFiredAt: r.LastFiredAt.UTC().Format(time.RFC3339),
		})
	}
	return marshalOrDefault(out, "[]")
}

const extractorSystemPrompt = `You are an AIOps report-extractor.
Given an alert and the engineer-style root-cause narrative an
investigator wrote, output ONE compact JSON object with these keys:

  {
    "root_cause":        "<one short sentence, max 200 chars>",
    "affected_window":   "<ISO-8601 start/end, e.g. 2026-05-19T03:42:15Z/2026-05-19T03:48:30Z, or empty>",
    "pinpoint_target":   { "device_id": <id-or-null>, "pid": <pid-or-null>, "service": "<name>", "cmd": "<command line>" },
    "evidence":          [ { "step": 1, "tool": "...", "summary": "..." }, ... ],
    "suggested_actions": [ { "label": "...", "category": "mutate|capacity|observe", "danger": "high|medium|low|none", "command": "...", "deeplink": "..." }, ... ],
    "confidence":        <float 0..1>
  }

Rules:
- Output ONLY the JSON, no prose, no markdown fences.
- Omit keys whose value would be empty or unknown rather than fabricate.
- Keep "evidence" entries to the steps the narrative actually performs;
  do not invent tool calls.
- "confidence" should be lower when the narrative is vague or relies
  on a single signal.`

// buildExtractorPrompt frames the alert context + worker output for
// the structured extraction call. Keeps the prompt fully self-contained
// so the summarizer doesn't need the in-progress chat history. locale
// overrides the language the JSON string fields come back in — without
// it the extractor inherits whatever language the worker wrote in.
func buildExtractorPrompt(incident alertmodel.Incident, finalAnswer string, locale string) string {
	var b strings.Builder
	b.WriteString("# Alert\n")
	fmt.Fprintf(&b, "  rule: %s (%s)\n", incident.Rule, incident.RuleName)
	fmt.Fprintf(&b, "  severity: %s\n", incident.Severity)
	fmt.Fprintf(&b, "  first_fired_at: %s\n", incident.FirstFiredAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "  last_fired_at: %s\n", incident.LastFiredAt.UTC().Format(time.RFC3339))
	if incident.DeviceID != nil {
		fmt.Fprintf(&b, "  device_id: %d\n", *incident.DeviceID)
	}
	if incident.Value != nil {
		fmt.Fprintf(&b, "  value: %.4f\n", *incident.Value)
	}
	if incident.Threshold != nil {
		fmt.Fprintf(&b, "  threshold: %.4f\n", *incident.Threshold)
	}
	if incident.Summary != "" {
		fmt.Fprintf(&b, "  summary: %s\n", incident.Summary)
	}
	b.WriteString("\n# Investigator narrative (markdown)\n\n")
	b.WriteString(finalAnswer)
	b.WriteString("\n\n# Now output the JSON described in the system prompt.\n")
	if d := localeDirective(locale); d != "" {
		b.WriteString("\n")
		b.WriteString(d)
		b.WriteString("\n")
		b.WriteString("Specifically: every string value in the JSON (root_cause, evidence[].summary, suggested_actions[].label, etc.) MUST be in the specified language, regardless of what language the worker narrative above uses.\n")
	}
	return b.String()
}

// extractJSONBlob pulls the first balanced top-level JSON object from
// the model's reply. Robust to ``` fences, leading prose, or trailing
// commentary the model may add despite the system prompt.
func extractJSONBlob(s string) string {
	// Strip common fence markers.
	s = strings.TrimSpace(s)
	for _, fence := range []string{"```json", "```JSON", "```"} {
		s = strings.ReplaceAll(s, fence, "")
	}
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	if start < 0 {
		return ""
	}
	// Find the matching closing brace by depth counting. Quotes
	// nullify braces inside strings.
	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func marshalOrDefault(v any, def string) string {
	if v == nil {
		return def
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return def
	}
	return string(b)
}

func buildConfidenceFactors(ex extractedReport, toolCallCount int) string {
	factors := map[string]any{
		"evidence_steps":    len(ex.Evidence),
		"tool_call_count":   toolCallCount,
		"has_pinpoint":      len(ex.PinpointTarget) > 0,
		"has_affected_win":  ex.AffectedWindow != "",
		"has_suggested":     len(ex.Suggested) > 0,
		"narrative_present": true,
	}
	b, _ := json.Marshal(factors)
	return string(b)
}

func clampRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
