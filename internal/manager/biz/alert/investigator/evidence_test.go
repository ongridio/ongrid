package investigator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	aiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

type evidenceReaderFake struct {
	records EvidenceRecords
	err     error
	reads   int
}

func (f *evidenceReaderFake) ListInvestigationCalls(context.Context, string, time.Time) (EvidenceRecords, error) {
	f.reads++
	return f.records, f.err
}
func (f *evidenceReaderFake) GetInvestigationCall(_ context.Context, _, callID string, _ time.Time) (EvidenceCall, uint64, error) {
	f.reads++
	if f.err != nil {
		return EvidenceCall{}, 0, f.err
	}
	for _, call := range f.records.Calls {
		if call.ID == callID {
			return call, f.records.OwnerID, nil
		}
	}
	return EvidenceCall{}, 0, errs.ErrNotFound
}

func evidenceCall(id, name, status, result string) EvidenceCall {
	now := time.Date(2026, 11, 4, 6, 20, 0, 0, time.UTC)
	return EvidenceCall{SessionID: "investigation", ToolCall: aiopsmodel.ToolCall{ID: id, ToolName: name, Status: status,
		ArgumentsJSON: `{}`, ResultJSON: &result, StartedAt: now, EndedAt: &now, CreatedAt: now}}
}

func TestEvidenceRecognizesOutcomesWithoutMistakingMissingDataForHealth(t *testing.T) {
	for _, tt := range []struct{ name, tool, status, raw, want string }{
		{"metrics", "query_promql", "success", `{"resultType":"matrix","result":[{"metric":{"device_id":"42"},"values":[[1793772000,"80"],[1793772600,"95"]]}]}`, "available"},
		{"empty logs", "query_logql", "success", `{"resultType":"streams","result":[]}`, "empty"},
		{"empty ES", "query_logql", "success", `{"records":[],"has_more":false}`, "empty"},
		{"log page incomplete", "query_logql", "success", `{"records":[],"has_more":true}`, "truncated"},
		{"transport timeout", "query_logql", "timeout", "", "timeout"},
		{"envelope error", "query_promql", "success", `{"error":"backend failed"}`, "error"},
		{"missing", "query_logql", "success", "", "missing"},
		{"unknown shape", "query_logql", "success", `{}`, "unsupported"},
		{"knowledge is context", "query_knowledge", "success", `{"items":["OOM"]}`, "unsupported"},
		{"empty command", "host_bash", "success", `{"allowed":true,"exit_code":0}`, "empty"},
		{"blocked command", "host_bash", "success", `{"allowed":false,"exit_code":0}`, "error"},
		{"command truncation", "host_bash", "success", `{"allowed":true,"exit_code":0,"truncated":true}`, "truncated"},
		{"nonzero command", "host_bash", "success", `{"allowed":true,"exit_code":1,"stderr":"failed"}`, "error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			call := evidenceCall("call", tt.tool, tt.status, tt.raw)
			got := describeEvidence(call, tt.raw, nil)
			if got.State != tt.want {
				t.Fatalf("state=%s want %s", got.State, tt.want)
			}
			if tt.name == "metrics" && (got.Target != "device_id=42" || got.DataStart == "" || got.DataEnd == "") {
				t.Fatalf("lost observation metadata: %+v", got)
			}
			if got.State == "empty" && got.DataStart != "" {
				t.Fatal("invented timestamp for empty result")
			}
		})
	}
}

func TestEvidenceBatchSeparatesTargetsAndFencesInventedReferences(t *testing.T) {
	call := evidenceCall("batch", "get_host_processes", "success", `{"results":[{"device_id":42,"process_list":{"processes":[{"pid":7}]}},{"device_id":43,"error":"offline"}]}`)
	call.SessionID = "specialist-child"
	r := &evidenceReaderFake{records: EvidenceRecords{Calls: []EvidenceCall{call}}}
	uc := (&Usecase{}).WithEvidenceReader(r)
	set := uc.collectEvidence(context.Background(), "investigation")
	if len(set.Sources) != 2 || set.Sources[0].State != "available" || set.Sources[1].State != "error" {
		t.Fatalf("sources=%+v", set.Sources)
	}
	if set.Sources[0].Target != "device_id=42" || set.Sources[1].Target != "device_id=43" || set.Sources[0].SessionID != "specialist-child" {
		t.Fatalf("lost identity: %+v", set.Sources)
	}
	for _, tt := range []struct {
		refs []string
		want string
	}{
		{[]string{"E1"}, "traceable"}, {[]string{"E1", "E2"}, "partial"}, {[]string{"E2"}, "unverified"},
		{[]string{"foreign-call"}, "unverified"}, {nil, "unverified"}, {[]string{"E1", "E1"}, "traceable"},
	} {
		if got := checkReference(tt.refs, set.Sources); got.Status != tt.want {
			t.Errorf("refs=%v got=%+v", tt.refs, got)
		}
	}
}

func TestEvidenceExtractionCannotLaunderCitationsThroughNarrative(t *testing.T) {
	r := &evidenceReaderFake{records: EvidenceRecords{Calls: []EvidenceCall{
		evidenceCall("metrics", "query_promql", "success", `{"resultType":"vector","result":[{"metric":{"device_id":"42"},"value":[1793772000,"95"]}]}`),
		evidenceCall("logs", "query_logql", "timeout", `{"error":"context deadline exceeded"}`),
	}}}
	sum := &fakeSummarizer{resp: &llm.ChatResp{Assistant: llm.Message{Content: `{"root_cause":"Confirmed OOM","root_cause_source_ids":["E1","E2"],"evidence":[{"summary":"The OOM log proves it","source_ids":["E2"]},{"summary":"An invented trace","source_ids":["E99"]}],"confidence":0.99}`}}}
	uc := (&Usecase{summarizer: sum, cfg: Config{SummarizerTimeout: time.Second}}).WithEvidenceReader(r)
	set := uc.collectEvidence(context.Background(), "investigation")
	fields := uc.extractStructured(context.Background(), alertmodel.Incident{ID: 1}, "Confirmed OOM", 2, "en", set)
	var check alertmodel.EvidenceCheck
	if err := json.Unmarshal([]byte(*fields.EvidenceCheckJSON), &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "partial" || check.RootCause.Status != "partial" {
		t.Fatalf("check=%+v", check)
	}
	var entries []alertmodel.InvestigationEvidence
	if err := json.Unmarshal([]byte(fields.EvidenceJSON), &entries); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Status != "unverified" {
			t.Fatalf("fabricated support passed: %+v", e)
		}
	}
	if sum.calls != 1 || r.reads != 1 {
		t.Fatalf("extra paid/model or data calls: %d %d", sum.calls, r.reads)
	}
	if !strings.Contains(sum.lastReq.Messages[1].Content, `"state":"timeout"`) {
		t.Fatal("extractor did not receive real execution outcome")
	}
}

func TestEvidenceReadAndPromptLimitsRemainExplicit(t *testing.T) {
	for _, state := range []string{"unsupported", "truncated", "pending", "missing"} {
		ref := checkReference([]string{"E1"}, []alertmodel.EvidenceSource{{ID: "E1", State: state}})
		if ref.Status != "incomplete" {
			t.Fatalf("%s must remain incomplete: %+v", state, ref)
		}
	}
	for _, tt := range []struct {
		name   string
		reader *evidenceReaderFake
		issue  string
	}{
		{"read error", &evidenceReaderFake{err: errors.New("db down")}, "source_read_failed"},
		{"incomplete tree", &evidenceReaderFake{records: EvidenceRecords{Incomplete: true}}, "source_limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			set := (&Usecase{}).WithEvidenceReader(tt.reader).collectEvidence(context.Background(), "root")
			if !strings.Contains(strings.Join(set.Issues, ","), tt.issue) {
				t.Fatalf("issues=%v", set.Issues)
			}
		})
	}
	r := &evidenceReaderFake{}
	for i := 0; i < MaxEvidenceCalls; i++ {
		r.records.Calls = append(r.records.Calls, evidenceCall("call", "query_logql", "success", `{"records":[{"message":"`+strings.Repeat("中", 500)+`"}]}`))
	}
	set := (&Usecase{}).WithEvidenceReader(r).collectEvidence(context.Background(), "root")
	if len(set.Prompt) > maxEvidencePromptBytes || !utf8.ValidString(set.Prompt) || len(set.Issues) == 0 {
		t.Fatalf("prompt bytes=%d issues=%v", len(set.Prompt), set.Issues)
	}
	for _, line := range strings.Split(strings.TrimSpace(set.Prompt), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatal("bounded prompt broke JSON")
		}
	}
}

func TestEvidenceDetailEnforcesReportAndTranscriptBoundary(t *testing.T) {
	call := evidenceCall("call", "query_logql", "success", `{"records":[{"message":"password=demo-secret Authorization=Bearer demo-token","timestamp":"2026-11-04T06:19:00Z"}]}`)
	call.ArgumentsJSON = `{"authorization":"demo-credential","nested":{"password":"hidden"}}`
	r := &evidenceReaderFake{records: EvidenceRecords{OwnerID: 7, Calls: []EvidenceCall{call}}}
	set := (&Usecase{}).WithEvidenceReader(r).collectEvidence(context.Background(), "investigation")
	check := marshalOrDefault(alertmodel.EvidenceCheck{Version: 1, Sources: set.Sources}, "{}")
	root := "investigation"
	rep := &alertmodel.InvestigationReport{ID: "report", AuditSessionID: &root, ReadyAt: call.EndedAt, EvidenceCheckJSON: &check}
	for _, tt := range []struct {
		name, report, source string
		user                 uint64
		admin                bool
		want                 error
	}{
		{"owner", "report", "E1", 7, false, nil}, {"admin", "report", "E1", 8, true, nil},
		{"other user", "report", "E1", 8, false, errs.ErrForbidden}, {"unauthenticated", "report", "E1", 0, false, errs.ErrUnauthorized},
		{"old report", "old", "E1", 7, false, errs.ErrNotFound}, {"other reference", "report", "E9", 7, false, errs.ErrNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadEvidenceDetail(context.Background(), r, rep, tt.report, tt.source, tt.user, tt.admin)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err=%v want=%v", err, tt.want)
			}
			if got != nil && (strings.Contains(got.Arguments, "demo-credential") || strings.Contains(got.Arguments, "hidden") || strings.Contains(got.Result, "demo-secret") || strings.Contains(got.Result, "demo-token")) {
				t.Fatalf("credential leaked: %+v", got)
			}
		})
	}
	r.records.Calls = nil
	if _, err := ReadEvidenceDetail(context.Background(), r, rep, "report", "E1", 7, false); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("deleted source: %v", err)
	}
}

func TestEvidenceOversizedJSONDoesNotExposeCredentials(t *testing.T) {
	for _, field := range []string{"arguments", "result"} {
		t.Run(field, func(t *testing.T) {
			call := evidenceCall("call", "query_logql", "success", `{"records":[]}`)
			call.ArgumentsJSON = `{"query":"{device_id=\"42\"} |= \"OOM\""}`
			raw := `{"password":"dummy-sensitive-value","pass\u0077ord":"dummy-sensitive-value","nested":"{\"api_key\":\"dummy-sensitive-value\"}","token":"dummy-sensitive-value` + strings.Repeat("中", MaxEvidenceResultBytes) + `"}`
			// Match the SQL reader: original byte count plus an already clipped prefix.
			prefix := clipEvidence(raw, MaxEvidenceResultBytes)
			if field == "arguments" {
				call.ArgumentsJSON, call.ArgumentsBytes = prefix, len(raw)
			} else {
				call.ResultJSON, call.ResultBytes = &prefix, len(raw)
			}
			call.BoundContent()
			r := &evidenceReaderFake{records: EvidenceRecords{OwnerID: 7, Calls: []EvidenceCall{call}}}
			set := (&Usecase{}).WithEvidenceReader(r).collectEvidence(context.Background(), "investigation")
			if len(set.Sources) != 1 || set.Sources[0].State != "truncated" || strings.Contains(set.Prompt, "dummy-sensitive-value") {
				t.Fatal("oversized source was not safely omitted from the extractor input")
			}
			check := marshalOrDefault(alertmodel.EvidenceCheck{Version: 1, Sources: set.Sources}, "{}")
			root := "investigation"
			rep := &alertmodel.InvestigationReport{ID: "report", AuditSessionID: &root, ReadyAt: call.EndedAt, EvidenceCheckJSON: &check}
			detail, err := ReadEvidenceDetail(context.Background(), r, rep, rep.ID, "E1", 7, false)
			if err != nil {
				t.Fatal(err)
			}
			if !detail.Truncated || strings.Contains(detail.Arguments+detail.Result, "dummy-sensitive-value") {
				t.Fatal("oversized source leaked through the original-result endpoint")
			}
			if field == "arguments" && detail.Result != `{"records":[]}` {
				t.Fatal("complete result was lost when only the arguments exceeded the limit")
			}
			if field == "result" && !strings.Contains(detail.Arguments, `device_id=\"42\"`) {
				t.Fatal("complete LogQL arguments were lost when only the result exceeded the limit")
			}
		})
	}
}

func TestEvidenceContentLimitPreservesCompleteFieldsAndRepeatedBounds(t *testing.T) {
	raw := `{"message":"` + strings.Repeat("x", MaxEvidenceResultBytes-len(`{"message":""}`)) + `"}`
	call := evidenceCall("call", "query_logql", "success", raw)
	call.ArgumentsJSON = raw
	call.BoundContent()
	if call.Truncated || call.ArgumentsJSON != raw || *call.ResultJSON != raw {
		t.Fatal("complete fields at the read limit must remain available")
	}
	raw += " "
	call.ArgumentsJSON = raw
	call.ResultJSON = &raw
	call.BoundContent()
	first := call
	call.BoundContent()
	if !call.Truncated || call.ArgumentsJSON != first.ArgumentsJSON || *call.ResultJSON != *first.ResultJSON {
		t.Fatal("repeated bounding lost the omission or truncation status")
	}
	if len(call.ArgumentsJSON) > MaxEvidenceResultBytes || len(*call.ResultJSON) > MaxEvidenceResultBytes {
		t.Fatal("oversized fields exceeded the read limit without SQL byte metadata")
	}
}

func TestEvidenceDetailOmitsClippedErrorCopy(t *testing.T) {
	for _, size := range []int{1024, MaxEvidenceResultBytes} {
		rawError := `{"password":"dummy-sensitive-value","padding":"` + strings.Repeat("x", size) + `"}`
		result := marshalOrDefault(map[string]string{"error": rawError}, "{}")
		status, storedError := basetool.ResultOutcome(result)
		call := evidenceCall("call", "query_logql", status, result)
		call.Error = &storedError
		call.BoundContent()
		r := &evidenceReaderFake{records: EvidenceRecords{OwnerID: 7, Calls: []EvidenceCall{call}}}
		set := (&Usecase{}).WithEvidenceReader(r).collectEvidence(context.Background(), "investigation")
		check := marshalOrDefault(alertmodel.EvidenceCheck{Version: 1, Sources: set.Sources}, "{}")
		root := "investigation"
		rep := &alertmodel.InvestigationReport{ID: "report", AuditSessionID: &root, ReadyAt: call.EndedAt, EvidenceCheckJSON: &check}
		detail, err := ReadEvidenceDetail(context.Background(), r, rep, rep.ID, "E1", 7, false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(set.Prompt+detail.Result+detail.Error, "dummy-sensitive-value") || !strings.Contains(detail.Error, "omitted") {
			t.Fatalf("clipped error copy was not safely omitted for result size %d", size)
		}
	}
}
