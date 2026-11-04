package investigator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	MaxEvidenceSessions    = 32
	MaxEvidenceCalls       = 128
	MaxEvidenceResultBytes = 32 * 1024
	maxEvidenceSources     = 64
	maxEvidencePromptBytes = 12 * 1024
)

type EvidenceCall struct {
	aiopsmodel.ToolCall
	SessionID      string
	ArgumentsBytes int
	ResultBytes    int
	Truncated      bool `gorm:"-"`
}

func (c *EvidenceCall) BoundContent() {
	// SQL may already have cut JSON mid-field; a partial document cannot be safely redacted.
	const omitted = "[omitted: content exceeds evidence read limit]"
	if c.ArgumentsBytes > MaxEvidenceResultBytes || len(c.ArgumentsJSON) > MaxEvidenceResultBytes {
		c.Truncated = true
		c.ArgumentsJSON = omitted
	}
	if c.ResultBytes > MaxEvidenceResultBytes || (c.ResultJSON != nil && len(*c.ResultJSON) > MaxEvidenceResultBytes) {
		c.Truncated = true
		result := omitted
		c.ResultJSON = &result
	}
	// ResultOutcome may already have cut the persisted error at 512 runes.
	if c.Error != nil && utf8.RuneCountInString(*c.Error) >= 512 {
		errText := "[omitted: error may be truncated]"
		c.Error = &errText
	}
}

type EvidenceRecords struct {
	OwnerID    uint64
	Calls      []EvidenceCall
	Incomplete bool
}

type EvidenceReader interface {
	ListInvestigationCalls(ctx context.Context, root string, before time.Time) (EvidenceRecords, error)
	GetInvestigationCall(ctx context.Context, root, callID string, before time.Time) (EvidenceCall, uint64, error)
}

type evidenceSet struct {
	Sources   []alertmodel.EvidenceSource
	Issues    []string
	Prompt    string
	CallCount int
}

func (uc *Usecase) collectEvidence(ctx context.Context, sessionID string) evidenceSet {
	out := evidenceSet{Sources: []alertmodel.EvidenceSource{}}
	if uc.evidence == nil || sessionID == "" {
		out.Issues = []string{"source_reader_unavailable"}
		return out
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := uc.evidence.ListInvestigationCalls(readCtx, sessionID, time.Now().UTC())
	if err != nil {
		uc.logger().Warn("RCA evidence read failed", "err", err)
		out.Issues = []string{"source_read_failed"}
		return out
	}
	out.CallCount = len(rows.Calls)
	if rows.Incomplete {
		out.Issues = append(out.Issues, "source_limit")
	}
	var prompt strings.Builder
	for _, call := range rows.Calls {
		parts := evidenceParts(call)
		for _, part := range parts {
			if len(out.Sources) == maxEvidenceSources {
				out.Issues = appendIssue(out.Issues, "source_limit")
				break
			}
			source := describeEvidence(call, part.value, part.index)
			source.ID = fmt.Sprintf("E%d", len(out.Sources)+1)
			// Arguments and results are untrusted data, never instructions.
			entry := struct {
				alertmodel.EvidenceSource
				Arguments string `json:"arguments"`
				Preview   string `json:"result_preview"`
				Clipped   bool   `json:"preview_truncated"`
			}{source, clipEvidence(redactEvidence(call.ArgumentsJSON), 400), clipEvidence(redactEvidence(part.value), 800), len(part.value) > 800 || len(call.ArgumentsJSON) > 400}
			encoded, _ := json.Marshal(entry)
			if prompt.Len()+len(encoded)+1 > maxEvidencePromptBytes {
				out.Issues = appendIssue(out.Issues, "prompt_limit")
				// Keep the source list identical to the model's allowed citation set.
				out.Prompt = prompt.String()
				return out
			}
			if entry.Clipped || source.State == "truncated" {
				out.Issues = appendIssue(out.Issues, "preview_truncated")
			}
			if source.State == "pending" || source.State == "missing" {
				out.Issues = appendIssue(out.Issues, "source_incomplete")
			}
			out.Sources = append(out.Sources, source)
			prompt.Write(encoded)
			prompt.WriteByte('\n')
		}
	}
	out.Prompt = prompt.String()
	return out
}

type evidencePart struct {
	value string
	index *int
}

func evidenceParts(call EvidenceCall) []evidencePart {
	raw := ""
	if call.ResultJSON != nil {
		raw = *call.ResultJSON
	}
	if !call.Truncated && isHostEvidenceTool(call.ToolName) {
		var batch struct {
			Results []json.RawMessage `json:"results"`
		}
		if json.Unmarshal([]byte(raw), &batch) == nil && len(batch.Results) > 0 {
			parts := make([]evidencePart, 0, min(len(batch.Results), maxEvidenceSources+1))
			for i, result := range batch.Results[:min(len(batch.Results), maxEvidenceSources+1)] {
				parts = append(parts, evidencePart{string(result), &i})
			}
			return parts
		}
	}
	return []evidencePart{{raw, nil}}
}

func isHostEvidenceTool(name string) bool {
	return name == "get_host_processes" || name == "get_host_load" || name == "host_bash"
}

func describeEvidence(call EvidenceCall, raw string, index *int) alertmodel.EvidenceSource {
	s := alertmodel.EvidenceSource{ToolCallID: call.ID, SessionID: call.SessionID, Tool: call.ToolName,
		State: "available", StartedAt: call.StartedAt, EndedAt: call.EndedAt, ResultIndex: index}
	if call.DeviceID != nil {
		s.Target = fmt.Sprintf("device_id=%d", *call.DeviceID)
	}
	if call.Truncated {
		s.State, s.Reason = "truncated", "result_limit"
		return s
	}
	var obj map[string]json.RawMessage
	decodeErr := json.Unmarshal([]byte(raw), &obj)
	if decodeErr == nil {
		if id := strings.Trim(string(obj["device_id"]), `"`); id != "" && id != "null" {
			s.Target = "device_id=" + clipEvidence(id, 80)
		}
	}
	if call.Status != aiopsmodel.StatusSuccess {
		s.State = call.Status
		if s.State != "error" && s.State != "timeout" && s.State != "pending" {
			s.State = "missing"
		}
		s.Reason = "execution_" + s.State
		return s
	}
	if raw == "" || raw == "null" || call.EndedAt == nil {
		s.State, s.Reason = "missing", "result_missing"
		return s
	}
	if status, _ := basetool.ResultOutcome(raw); status != "success" {
		s.State, s.Reason = status, "execution_"+status
		return s
	}
	if decodeErr != nil || obj == nil {
		s.State, s.Reason = "unsupported", "result_shape_unknown"
		return s
	}
	switch call.ToolName {
	case "query_promql", "query_logql":
		result := obj["result"]
		if records, ok := obj["records"]; ok {
			result = records
		}
		var entries []json.RawMessage
		if result == nil || json.Unmarshal(result, &entries) != nil {
			s.State, s.Reason = "unsupported", "result_shape_unknown"
		} else if len(entries) == 0 {
			s.State = "empty"
		} else {
			s.Target, s.DataStart, s.DataEnd = observationBounds(entries, s.Target)
		}
		if string(obj["has_more"]) == "true" {
			s.State, s.Reason = "truncated", "query_has_more"
		}
	case "get_host_processes", "get_host_load":
		key := "process_list"
		if call.ToolName == "get_host_load" {
			key = "host_load"
		}
		if obj[key] == nil || string(obj[key]) == "null" {
			s.State, s.Reason = "unsupported", "result_shape_unknown"
		}
	case "host_bash":
		if string(obj["allowed"]) == "false" {
			s.State, s.Reason = "error", "execution_blocked"
		} else if string(obj["truncated"]) == "true" {
			s.State, s.Reason = "truncated", "command_output_limit"
		} else if obj["exit_code"] == nil || string(obj["allowed"]) != "true" {
			s.State, s.Reason = "unsupported", "result_shape_unknown"
		} else if (obj["stdout"] == nil || string(obj["stdout"]) == `""`) && (obj["stderr"] == nil || string(obj["stderr"]) == `""`) {
			s.State = "empty"
		}
	default:
		s.State, s.Reason = "unsupported", "context_only"
	}
	return s
}

func checkReference(ids []string, sources []alertmodel.EvidenceSource) alertmodel.EvidenceReference {
	out := alertmodel.EvidenceReference{SourceIDs: []string{}, Status: "unverified"}
	valid := 0
	incomplete := false
	if len(ids) > 16 {
		out.Issues = append(out.Issues, "reference_limit")
		ids = ids[:16]
	}
	for _, id := range ids {
		id = clipEvidence(strings.TrimSpace(id), 64)
		if slices.Contains(out.SourceIDs, id) {
			continue
		}
		out.SourceIDs = append(out.SourceIDs, id)
		idx := slices.IndexFunc(sources, func(s alertmodel.EvidenceSource) bool { return s.ID == id })
		if idx < 0 {
			out.Issues = appendIssue(out.Issues, "unknown_source:"+id)
			continue
		}
		s := sources[idx]
		if s.State == "available" || s.State == "empty" {
			valid++
		} else {
			out.Issues = appendIssue(out.Issues, s.State+":"+id)
			incomplete = incomplete || s.State == "unsupported" || s.State == "truncated" || s.State == "missing" || s.State == "pending"
		}
	}
	if valid > 0 {
		out.Status = "traceable"
		if len(out.Issues) > 0 {
			out.Status = "partial"
		}
	}
	if len(out.SourceIDs) == 0 {
		out.Issues = appendIssue(out.Issues, "no_references")
	}
	if incomplete {
		out.Status = "incomplete"
	}
	return out
}

func appendIssue(issues []string, issue string) []string {
	if !slices.Contains(issues, issue) {
		return append(issues, issue)
	}
	return issues
}

func clipEvidence(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}

// EvidenceDetail preserves the transcript permission boundary: system-owned
// investigations are readable by admins, user-owned ones by their owner/admin.
func ReadEvidenceDetail(ctx context.Context, reader EvidenceReader, rep *alertmodel.InvestigationReport, reportID, sourceID string, userID uint64, admin bool) (*alertmodel.EvidenceDetail, error) {
	if userID == 0 {
		return nil, errs.ErrUnauthorized
	}
	if reader == nil {
		return nil, errs.ErrNotWiredYet
	}
	if rep == nil || rep.ID != reportID || rep.EvidenceCheckJSON == nil || rep.AuditSessionID == nil || rep.ReadyAt == nil {
		return nil, errs.ErrNotFound
	}
	var check alertmodel.EvidenceCheck
	if json.Unmarshal([]byte(*rep.EvidenceCheckJSON), &check) != nil || check.Version != 1 {
		return nil, errs.ErrNotFound
	}
	idx := slices.IndexFunc(check.Sources, func(s alertmodel.EvidenceSource) bool { return s.ID == sourceID })
	if idx < 0 {
		return nil, errs.ErrNotFound
	}
	source := check.Sources[idx]
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	call, owner, err := reader.GetInvestigationCall(readCtx, *rep.AuditSessionID, source.ToolCallID, *rep.ReadyAt)
	if err != nil {
		return nil, err
	}
	if !admin && owner != userID {
		return nil, errs.ErrForbidden
	}
	if call.SessionID != source.SessionID || call.ToolName != source.Tool {
		return nil, errs.ErrNotFound
	}
	if (call.EndedAt == nil) != (source.EndedAt == nil) || (call.EndedAt != nil && !call.EndedAt.Equal(*source.EndedAt)) {
		return nil, errs.ErrNotFound
	}
	parts := evidenceParts(call)
	for _, part := range parts {
		if (part.index == nil && source.ResultIndex == nil) || (part.index != nil && source.ResultIndex != nil && *part.index == *source.ResultIndex) {
			result := redactEvidence(part.value)
			args := redactEvidence(call.ArgumentsJSON)
			errText := ""
			if call.Error != nil {
				errText = clipEvidence(redactEvidence(*call.Error), 512)
			}
			return &alertmodel.EvidenceDetail{Source: source, Arguments: clipEvidence(args, MaxEvidenceResultBytes), Result: clipEvidence(result, MaxEvidenceResultBytes), Error: errText,
				Truncated: call.Truncated || len(args) > MaxEvidenceResultBytes || len(result) > MaxEvidenceResultBytes}, nil
		}
	}
	return nil, errs.ErrNotFound
}
