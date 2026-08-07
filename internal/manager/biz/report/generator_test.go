package report

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/chatruntime"
	model "github.com/ongridio/ongrid/internal/manager/model/report"
)

// fakeFacts returns a canned ReportFacts.
type fakeFacts struct {
	facts *ReportFacts
	err   error
}

func (f fakeFacts) Collect(context.Context, Period, Period, Scope) (*ReportFacts, error) {
	return f.facts, f.err
}

// fakeSpawner returns a canned worker (Result / Err).
type fakeSpawner struct {
	result string
	werr   string
	spawnErr error
	gotReq chatruntime.SpawnRequest
}

func (s *fakeSpawner) SpawnWorker(_ context.Context, req chatruntime.SpawnRequest) (*chatruntime.Worker, error) {
	s.gotReq = req
	if s.spawnErr != nil {
		return nil, s.spawnErr
	}
	return &chatruntime.Worker{
		ID:        "agent-deadbeef",
		SessionID: "sess-1",
		Result:    s.result,
		Err:       s.werr,
	}, nil
}

func sampleFacts() *ReportFacts {
	d := -12.0
	return &ReportFacts{
		Hero: []HeroStat{
			{Key: "incidents", Label: "Incidents", Value: 3, DeltaPct: &d, Sparkline: []int{1, 0, 2}},
			{Key: "mttr_minutes", Label: "MTTR", Value: 60, Unit: "min"},
		},
		Incidents: []IncidentFact{
			{ID: 1, Title: "CPU High", Severity: "warning", Status: "resolved", DeviceID: 7, DurationMin: 30},
			{ID: 2, Title: "Disk Full", Severity: "critical", Status: "resolved", DeviceID: 9, DurationMin: 90},
		},
		Actions: ActionsSummary{MutatingTotal: 3, MutatingApproved: 2, SafeTotal: 5},
	}
}

func pendingReport() *model.Report {
	loc := time.UTC
	return &model.Report{
		ID:          "rpt-1",
		CreatedBy:   42,
		Title:       "周报 · test",
		Kind:        model.KindWeekly,
		PeriodStart: time.Date(2026, 6, 1, 0, 0, 0, 0, loc),
		PeriodEnd:   time.Date(2026, 6, 8, 0, 0, 0, 0, loc),
		Timezone:    "UTC",
		Status:      model.StatusPending,
		ScopeJSON:   "{}",
	}
}

func newGenTestRepo(rpt *model.Report) *fakeRepo {
	r := newFakeRepo()
	r.reports[rpt.ID] = rpt
	return r
}

func TestGeneratorTimeoutUsesLiveProvider(t *testing.T) {
	ctx := context.Background()
	gen := NewWorkerGenerator(
		newGenTestRepo(pendingReport()),
		fakeFacts{facts: sampleFacts()},
		&fakeSpawner{},
		GeneratorConfig{
			Timeout:         time.Minute,
			TimeoutProvider: func(context.Context) time.Duration { return 5 * time.Minute },
		},
		nil,
	)
	if got := gen.timeout(ctx); got != 5*time.Minute {
		t.Fatalf("timeout() = %s, want 5m", got)
	}

	gen.cfg.TimeoutProvider = func(context.Context) time.Duration { return 0 }
	if got := gen.timeout(ctx); got != time.Minute {
		t.Fatalf("timeout() fallback = %s, want 1m", got)
	}
}

func TestGenerator_HappyPath_OverwritesNumbersFromFacts(t *testing.T) {
	rpt := pendingReport()
	repo := newGenTestRepo(rpt)
	// LLM returns valid ContentJSON but with WRONG/empty numbers — the
	// generator must overwrite hero/actions/incidents from facts.
	llmOut := "```json\n" + `{
		"version":"1",
		"hero":[{"key":"incidents","label":"Incidents","value":9999}],
		"narrative":{"headline":"本周整体平稳","paragraphs":[
			{"text":"{{entity:edge:7|db-prod-3}} 出现 IO 压力"}]},
		"key_incidents":[{"id":2,"root_cause_snippet":"backup 重叠"}],
		"actions_summary":{"mutating_total":0},
		"advice":[{"text":"挪 backup 窗口"}]
	}` + "\n```"
	spawner := &fakeSpawner{result: llmOut}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: sampleFacts()}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")

	got, _ := repo.GetReport(context.Background(), "rpt-1")
	if got.Status != model.StatusReady {
		t.Fatalf("status = %q, want ready (err=%q)", got.Status, got.ErrorMsg)
	}
	content, err := ParseContent(got.ContentJSON)
	if err != nil {
		t.Fatal(err)
	}
	// Hero overwritten from facts — the 9999 the LLM emitted is gone.
	if len(content.Hero) != 2 || content.Hero[0].Value != 3 {
		t.Errorf("hero not overwritten from facts: %+v", content.Hero)
	}
	// Actions overwritten from facts.
	if content.Actions.MutatingTotal != 3 || content.Actions.MutatingApproved != 2 {
		t.Errorf("actions not overwritten: %+v", content.Actions)
	}
	// KeyIncidents rebuilt from facts (sorted by duration desc: id2 90m, id1 30m),
	// preserving the LLM snippet on id2.
	if len(content.KeyIncidents) != 2 || content.KeyIncidents[0].ID != 2 {
		t.Errorf("incidents not merged/sorted: %+v", content.KeyIncidents)
	}
	if content.KeyIncidents[0].RootCauseSnippet != "backup 重叠" {
		t.Errorf("LLM snippet not preserved: %+v", content.KeyIncidents[0])
	}
	// Narrative (LLM-owned) survives.
	if content.Narrative.Headline != "本周整体平稳" {
		t.Errorf("headline lost: %q", content.Narrative.Headline)
	}
	// Markdown + summary populated.
	if got.ContentMD == "" || got.SummaryText != "本周整体平稳" {
		t.Errorf("md/summary not set: md=%d summary=%q", len(got.ContentMD), got.SummaryText)
	}
	if got.GeneratedAt == nil {
		t.Error("generated_at not stamped")
	}
	// Worker/session ids captured.
	if got.AuditSessionID == nil || *got.AuditSessionID != "sess-1" {
		t.Errorf("audit session id not captured")
	}
	// Spawn used the report persona + report session kind + owner.
	if spawner.gotReq.AgentName != model.DefaultReporterPersona {
		t.Errorf("persona = %q", spawner.gotReq.AgentName)
	}
	if spawner.gotReq.SessionKind != "report" || spawner.gotReq.OwnerUserID != 42 {
		t.Errorf("spawn req = %+v", spawner.gotReq)
	}
}

func TestGenerator_SpawnError_MarksFailed(t *testing.T) {
	rpt := pendingReport()
	repo := newGenTestRepo(rpt)
	spawner := &fakeSpawner{spawnErr: errors.New("boom")}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: sampleFacts()}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")

	got, _ := repo.GetReport(context.Background(), "rpt-1")
	if got.Status != model.StatusFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.ErrorMsg == "" {
		t.Error("error_msg not set on failure")
	}
}

func TestGenerator_WorkerErr_MarksFailed(t *testing.T) {
	rpt := pendingReport()
	repo := newGenTestRepo(rpt)
	spawner := &fakeSpawner{werr: "exceeds max steps"}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: sampleFacts()}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")
	got, _ := repo.GetReport(context.Background(), "rpt-1")
	if got.Status != model.StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestGenerator_BadJSON_MarksFailed(t *testing.T) {
	rpt := pendingReport()
	repo := newGenTestRepo(rpt)
	spawner := &fakeSpawner{result: "this is not json at all"}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: sampleFacts()}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")
	got, _ := repo.GetReport(context.Background(), "rpt-1")
	if got.Status != model.StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
}

func TestGenerator_CalmReport_StillGenerates(t *testing.T) {
	rpt := pendingReport()
	repo := newGenTestRepo(rpt)
	// 0 incidents, 0 actions — calm period. Facts all zero but hero present.
	calmFacts := &ReportFacts{
		Hero:    []HeroStat{{Key: "incidents", Label: "Incidents", Value: 0}},
		Actions: ActionsSummary{},
	}
	llmOut := `{"version":"1","hero":[],"narrative":{"headline":"本周无异常，一切平稳"},"key_incidents":[],"actions_summary":{},"advice":[]}`
	spawner := &fakeSpawner{result: llmOut}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: calmFacts}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")
	got, _ := repo.GetReport(context.Background(), "rpt-1")
	if got.Status != model.StatusReady {
		t.Fatalf("calm report should be ready, got %q (%s)", got.Status, got.ErrorMsg)
	}
	if got.SummaryText != "本周无异常，一切平稳" {
		t.Errorf("calm summary = %q", got.SummaryText)
	}
}

func TestGenerator_NonPendingIsNoOp(t *testing.T) {
	rpt := pendingReport()
	rpt.Status = model.StatusReady // already done
	repo := newGenTestRepo(rpt)
	spawner := &fakeSpawner{result: "{}"}
	gen := NewWorkerGenerator(repo, fakeFacts{facts: sampleFacts()}, spawner, GeneratorConfig{}, nil)

	gen.Generate(context.Background(), "rpt-1")
	// Spawner must not have been called.
	if spawner.gotReq.AgentName != "" {
		t.Error("generator ran on a non-pending report")
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                              `{"a":1}`,
		"```json\n{\"a\":1}\n```":              `{"a":1}`,
		"```\n{\"a\":1}\n```":                  `{"a":1}`,
		"here you go:\n{\"a\":1}\nthat's it":   `{"a":1}`,
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}
