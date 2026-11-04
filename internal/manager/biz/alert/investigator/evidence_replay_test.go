package investigator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/llm"
)

const replayNarrative = "应用内存从 80% 上涨到 95%，内核日志确认发生 OOM，根因为应用内存泄漏。"
const replayExtraction = `{"root_cause":"应用内存泄漏触发 OOM，导致服务异常。","root_cause_source_ids":["E1","E2"],"evidence":[{"summary":"内存使用率从 80% 上涨到 95%。","source_ids":["E1"],"tool":"query_promql"},{"summary":"内核日志确认发生 OOM。","source_ids":["E2"],"tool":"query_logql"}],"suggested_actions":[{"label":"重试内核日志查询，并采集堆内存数据。","category":"observe","danger":"none"}],"confidence":0.95}`

func replayInputs() (*Usecase, *evidenceReaderFake, *fakeSummarizer, alertmodel.Incident) {
	metrics := evidenceCall("metrics-db-id", "query_promql", "success", `{"resultType":"matrix","result":[{"metric":{"device_id":"42","service_name":"checkout-api"},"values":[[1793772000,"80"],[1793773200,"95"]]}]}`)
	metrics.ArgumentsJSON = `{"expr":"memory_usage_percent{device_id=\"42\"}","start":"2026-11-04T06:00:00Z","end":"2026-11-04T06:20:00Z"}`
	logs := evidenceCall("logs-db-id", "query_logql", "timeout", `{"error":"context deadline exceeded"}`)
	logs.SessionID = "specialist-child"
	logs.ArgumentsJSON = `{"query":"{device_id=\"42\"} |= \"Out of memory\"","start":"2026-11-04T06:00:00Z","end":"2026-11-04T06:20:00Z"}`
	errText := "context deadline exceeded"
	logs.Error = &errText
	r := &evidenceReaderFake{records: EvidenceRecords{Calls: []EvidenceCall{metrics, logs}}}
	sum := &fakeSummarizer{resp: &llm.ChatResp{Assistant: llm.Message{Content: replayExtraction}}}
	uc := NewUsecase(nil, nil, sum, Config{SummarizerTimeout: time.Second}, nil).WithEvidenceReader(r)
	device := uint64(42)
	incident := alertmodel.Incident{ID: 42, Rule: "memory_high", RuleName: "应用内存超过 90%", Severity: "critical", DeviceID: &device, FirstFiredAt: metrics.StartedAt, LastFiredAt: metrics.StartedAt}
	return uc, r, sum, incident
}

// Fixed replies deliberately retain the wrong OOM claim in both versions. This
// measures deterministic citation checking, not a model's diagnostic accuracy.
func TestEvidenceReplay(t *testing.T) {
	uc, reader, sum, incident := replayInputs()
	start := time.Now()
	set := uc.collectEvidence(context.Background(), "investigation")
	fields := uc.extractStructured(context.Background(), incident, replayNarrative, 2, "zh", set)
	var check alertmodel.EvidenceCheck
	if err := json.Unmarshal([]byte(*fields.EvidenceCheckJSON), &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "partial" || check.RootCause.Status != "partial" || check.Sources[1].State != "timeout" || sum.calls != 1 || reader.reads != 1 {
		t.Fatalf("unexpected replay: %+v, model=%d reads=%d", check, sum.calls, reader.reads)
	}
	t.Logf("fixed replay: model_calls=%d catalog_bytes=%d source_reads=%d local_elapsed=%s", sum.calls, len(set.Prompt), reader.reads, time.Since(start))
	writeReplayArtifact(t, "new", fields)
	if dir := os.Getenv("ONGRID_EVIDENCE_REPLAY_DIR"); dir != "" {
		root := "investigation"
		rep := &alertmodel.InvestigationReport{ID: "replay-report", AuditSessionID: &root, ReadyAt: reader.records.Calls[0].EndedAt, EvidenceCheckJSON: fields.EvidenceCheckJSON}
		for _, source := range check.Sources {
			detail, err := ReadEvidenceDetail(context.Background(), reader, rep, rep.ID, source.ID, 1, true)
			if err != nil {
				t.Fatal(err)
			}
			writeReplayJSON(t, filepath.Join(dir, source.ID+".json"), detail)
		}
	}
}

func TestEvidencePipelinePersistsCheckWithAuditSession(t *testing.T) {
	uc, reader, sum, incident := replayInputs()
	repo := &fakeRepo{}
	uc.repo = repo
	uc.spawner = &fakeSpawner{worker: &chatruntime.Worker{ID: "worker", SessionID: "investigation", Result: replayNarrative}}
	uc.run("report", incident, "42", "zh")
	if len(repo.attaches) != 1 || repo.attaches[0].auditSessionID != "investigation" || len(repo.ready) != 1 {
		t.Fatalf("pipeline did not attach audit session and finish: %+v", repo)
	}
	fields := repo.ready[0].fields
	var check alertmodel.EvidenceCheck
	if fields.EvidenceCheckJSON == nil {
		t.Fatal("pipeline lost evidence check")
	}
	if err := json.Unmarshal([]byte(*fields.EvidenceCheckJSON), &check); err != nil {
		t.Fatal(err)
	}
	if check.Status != "partial" || fields.ToolCallCount != 2 || sum.calls != 1 || reader.reads != 1 {
		t.Fatalf("pipeline changed checked result: %+v", check)
	}
}

func TestEvidencePipelineKeepsIndependentExtractionTimeout(t *testing.T) {
	uc, _, sum, incident := replayInputs()
	repo := &fakeRepo{}
	uc.repo = repo
	uc.spawner = &fakeSpawner{worker: &chatruntime.Worker{ID: "worker", SessionID: "investigation", Result: replayNarrative}}
	uc.cfg.WorkerTimeout = time.Minute
	uc.cfg.SummarizerTimeout = time.Hour
	uc.summarizer = summarizerFunc(func(ctx context.Context, req llm.ChatReq) (*llm.ChatResp, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 59*time.Minute || time.Until(deadline) > time.Hour {
			t.Errorf("extractor did not receive its own bounded budget: deadline=%v", deadline)
		}
		return sum.Chat(ctx, req)
	})
	uc.run("report", incident, "42", "en")
	if len(repo.ready) != 1 || repo.ready[0].fields.RootCause == "" || sum.calls != 1 {
		t.Fatalf("structured extraction did not finish: %+v", repo.ready)
	}
}

func writeReplayArtifact(t *testing.T, version string, fields ReadyFields) {
	t.Helper()
	dir := os.Getenv("ONGRID_EVIDENCE_REPLAY_DIR")
	if dir == "" {
		return
	}
	report := map[string]any{
		"id": "replay-report", "incident_id": 42, "status": "ready", "root_cause": fields.RootCause,
		"evidence": json.RawMessage(fields.EvidenceJSON), "confidence": fields.Confidence,
		"findings_md": fields.FindingsMD, "tool_call_count": fields.ToolCallCount,
		"suggested_actions": json.RawMessage(fields.SuggestedActionsJSON),
		"created_at":        "2026-11-04T06:20:00Z", "ready_at": "2026-11-04T06:20:30Z",
	}
	if fields.EvidenceCheckJSON != nil {
		report["evidence_check"] = json.RawMessage(*fields.EvidenceCheckJSON)
	}
	writeReplayJSON(t, filepath.Join(dir, version+".json"), report)
}

func writeReplayJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}
