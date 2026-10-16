package investigator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	chatruntime "github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/contract"
	alertmodel "github.com/ongridio/ongrid/internal/manager/model/alert"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// fakeRepo records every call so the tests can assert state transitions.
type fakeRepo struct {
	mu       sync.Mutex
	created  []*alertmodel.InvestigationReport
	statuses []statusCall
	attaches []attachCall
	ready    []readyCall
	recently bool
	recError error
}

type statusCall struct{ id, status, reason string }
type attachCall struct{ id, workerID, auditSessionID string }
type readyCall struct {
	id     string
	fields ReadyFields
}

func (r *fakeRepo) Create(_ context.Context, rep *alertmodel.InvestigationReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rep.ID == "" {
		rep.ID = "rpt_" + rep.Status + "_x"
	}
	r.created = append(r.created, rep)
	return nil
}

func (r *fakeRepo) UpdateStatus(_ context.Context, id, status, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statuses = append(r.statuses, statusCall{id, status, reason})
	return nil
}

func (r *fakeRepo) AttachWorker(_ context.Context, id, workerID, auditSessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attaches = append(r.attaches, attachCall{id, workerID, auditSessionID})
	return nil
}

func (r *fakeRepo) MarkReady(_ context.Context, id string, fields ReadyFields) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = append(r.ready, readyCall{id, fields})
	return nil
}

func (r *fakeRepo) RecentlySpawnedFor(_ context.Context, _ string, _ *uint64, _ time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recently, r.recError
}

func (r *fakeRepo) GetByIncident(_ context.Context, _ uint64) (*alertmodel.InvestigationReport, error) {
	return nil, errs.ErrNotFound
}

func (r *fakeRepo) DeleteByIncident(_ context.Context, _ uint64) error { return nil }

func (r *fakeRepo) ListIncidentsWithoutReport(_ context.Context, _ time.Time, _ int) ([]uint64, error) {
	return nil, nil
}

type fakeSpawner struct {
	mu     sync.Mutex
	calls  []chatruntime.SpawnRequest
	worker *chatruntime.Worker
	err    error
	wait   time.Duration
}

type fakeLocaleResolver struct {
	locale string
	found  bool
	err    error
}

func (r fakeLocaleResolver) AgentOutputLocale(context.Context) (string, bool, error) {
	return r.locale, r.found, r.err
}

func (s *fakeSpawner) SpawnWorker(ctx context.Context, req chatruntime.SpawnRequest) (*chatruntime.Worker, error) {
	if s.wait > 0 {
		select {
		case <-time.After(s.wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	return s.worker, s.err
}

func (s *fakeSpawner) StopWorker(_ context.Context, _ string) error { return nil }

// TestEnqueue_DisabledByDefault — when cfg.Enabled is false, Enqueue
// does nothing: no repo writes, no spawner calls. Default-off is the
// production contract.
func TestEnqueue_DisabledByDefault(t *testing.T) {
	repo := &fakeRepo{}
	spawner := &fakeSpawner{}
	uc := NewUsecase(repo, spawner, nil, Config{Enabled: false}, nil)
	uc.Enqueue(context.Background(), &alertmodel.Incident{ID: 1, Severity: "critical"})
	if len(repo.created) != 0 {
		t.Errorf("disabled UC created %d rows, want 0", len(repo.created))
	}
}

func TestResolveLocalePrefersAgentSetting(t *testing.T) {
	uc := NewUsecase(&fakeRepo{}, &fakeSpawner{}, nil, Config{DefaultLocale: "en"}, nil).
		WithLocaleResolver(fakeLocaleResolver{locale: "zh", found: true})
	if got := uc.resolveLocale(context.Background(), "en"); got != "zh" {
		t.Fatalf("resolveLocale() = %q, want zh", got)
	}
}

func TestResolveLocaleFallsBackToRequestThenDeploymentDefault(t *testing.T) {
	uc := NewUsecase(&fakeRepo{}, &fakeSpawner{}, nil, Config{DefaultLocale: "en"}, nil).
		WithLocaleResolver(fakeLocaleResolver{})
	if got := uc.resolveLocale(context.Background(), "zh"); got != "zh" {
		t.Fatalf("request fallback = %q, want zh", got)
	}
	if got := uc.resolveLocale(context.Background(), ""); got != "en" {
		t.Fatalf("deployment fallback = %q, want en", got)
	}
}

// TestEnqueue_SkipsBelowSeverityFloor — info-level alerts pass the
// disabled check (Enabled=true) but fail the severity gate.
func TestEnqueue_SkipsBelowSeverityFloor(t *testing.T) {
	repo := &fakeRepo{}
	uc := NewUsecase(repo, &fakeSpawner{}, nil, Config{Enabled: true, MinSeverity: "warning"}, nil)
	uc.Enqueue(context.Background(), &alertmodel.Incident{ID: 1, Severity: "info"})
	if len(repo.created) != 0 {
		t.Errorf("low-severity Enqueue created %d rows, want 0", len(repo.created))
	}
}

// TestEnqueue_ParallelDistinctIncidents — distinct incident_ids on the
// same (rule, device) tuple BOTH get rows. Replaces the prior
// TestEnqueue_SkipsOnDedup: the per-(rule, device, window) gate was
// removed 2026-05-19; each new incident_id is a fresh failure event
// and gets its own analysis. DB uniqueness on incident_id still
// prevents double-spawn for the same incident.
func TestEnqueue_ParallelDistinctIncidents(t *testing.T) {
	repo := &fakeRepo{recently: true} // simulated "recent" — should be ignored
	uc := NewUsecase(repo, &fakeSpawner{}, nil, Config{Enabled: true}, nil)
	uc.Enqueue(context.Background(), &alertmodel.Incident{ID: 1, Rule: "cpu_high", Severity: "critical"})
	uc.Enqueue(context.Background(), &alertmodel.Incident{ID: 2, Rule: "cpu_high", Severity: "critical"})
	if len(repo.created) != 2 {
		t.Errorf("two distinct incidents created %d rows, want 2", len(repo.created))
	}
}

// TestEnqueue_ConcurrencyCap — over the cap, surplus enqueues land as
// status=skipped rows with a reason rather than queueing. The
// spawner.wait keeps the first batch in-flight long enough that the
// next Enqueue trips the cap.
func TestEnqueue_ConcurrencyCap(t *testing.T) {
	repo := &fakeRepo{}
	spawner := &fakeSpawner{
		worker: &chatruntime.Worker{ID: "w", SessionID: "s", Result: "ok"},
		wait:   200 * time.Millisecond, // keep workers in-flight
	}
	uc := NewUsecase(repo, spawner, nil, Config{
		Enabled:       true,
		MaxConcurrent: 2,
	}, nil)
	// Three incidents, cap = 2 → third should be skipped immediately.
	for id := uint64(1); id <= 3; id++ {
		uc.Enqueue(context.Background(), &alertmodel.Incident{
			ID: id, Rule: "cpu_high", Severity: "critical",
		})
	}
	repo.mu.Lock()
	created := len(repo.created)
	skipped := 0
	for _, r := range repo.created {
		if r.Status == "skipped" && strings.Contains(r.StatusReason, "concurrency limit") {
			skipped++
		}
	}
	repo.mu.Unlock()
	if created != 3 {
		t.Fatalf("created = %d, want 3 (2 pending + 1 skipped)", created)
	}
	if skipped != 1 {
		t.Errorf("skipped-by-cap rows = %d, want 1", skipped)
	}
}

// TestEnqueue_HappyPath — gate passes, row created, worker spawned,
// final answer written as findings_md + first line as root_cause.
func TestEnqueue_HappyPath(t *testing.T) {
	repo := &fakeRepo{}
	spawner := &fakeSpawner{
		worker: &chatruntime.Worker{
			ID:        "wkr_abc",
			SessionID: "ses_def",
			Result:    "Root cause: PID 8821 saturated CPU on pg-replica-7.\n\nDetails follow...",
		},
	}
	uc := NewUsecase(repo, spawner, nil, Config{Enabled: true, AgentName: "incident-investigator"}, nil)
	dev := uint64(42)
	inc := &alertmodel.Incident{
		ID: 1, Rule: "cpu_high", RuleName: "CPU high", Severity: "critical",
		DeviceID: &dev, FirstFiredAt: time.Now(), LastFiredAt: time.Now(),
	}
	uc.Enqueue(context.Background(), inc)

	// Give the goroutine a moment to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		readyCount := len(repo.ready)
		repo.mu.Unlock()
		if readyCount > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.created) != 1 {
		t.Fatalf("want 1 row created, got %d", len(repo.created))
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("want 1 spawner call, got %d", len(spawner.calls))
	}
	if spawner.calls[0].SessionKind != "investigation" {
		t.Errorf("SessionKind = %q, want investigation", spawner.calls[0].SessionKind)
	}
	if spawner.calls[0].AgentName != "incident-investigator" {
		t.Errorf("AgentName = %q, want incident-investigator", spawner.calls[0].AgentName)
	}
	if len(repo.attaches) != 1 || repo.attaches[0].workerID != "wkr_abc" {
		t.Errorf("attach call mismatch: %+v", repo.attaches)
	}
	if len(repo.ready) != 1 {
		t.Fatalf("want 1 MarkReady call, got %d", len(repo.ready))
	}
	got := repo.ready[0].fields
	if got.RootCause != "Root cause: PID 8821 saturated CPU on pg-replica-7." {
		t.Errorf("root_cause = %q", got.RootCause)
	}
	if got.FindingsMD != "Root cause: PID 8821 saturated CPU on pg-replica-7.\n\nDetails follow..." {
		t.Errorf("findings_md mismatch: %q", got.FindingsMD)
	}
}

// TestEnqueue_WorkerError — when SpawnWorker returns an error the row
// flips to failed with the error string.
func TestEnqueue_WorkerError(t *testing.T) {
	repo := &fakeRepo{}
	spawner := &fakeSpawner{err: errors.New("LLM timeout")}
	uc := NewUsecase(repo, spawner, nil, Config{Enabled: true}, nil)
	uc.Enqueue(context.Background(), &alertmodel.Incident{
		ID: 1, Rule: "cpu_high", Severity: "critical",
	})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		repo.mu.Lock()
		c := len(repo.statuses)
		repo.mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.statuses) == 0 {
		t.Fatalf("want at least 1 UpdateStatus call")
	}
	last := repo.statuses[len(repo.statuses)-1]
	if last.status != alertmodel.InvestigationStatusFailed {
		t.Errorf("final status = %q, want failed", last.status)
	}
	if last.reason == "" {
		t.Errorf("failure should carry a reason; got empty")
	}
}

// TestSeverityRank — sanity for the severity floor logic. Unknown
// values default to 'warning' rank so existing rules without an
// explicit severity still trip a default-warning floor.
func TestSeverityRank(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"critical", 4}, {"crit", 4}, {"page", 4},
		{"error", 3}, {"high", 3},
		{"warning", 2}, {"warn", 2}, {"", 2}, {"???", 2},
		{"info", 1}, {"notice", 1},
		{"debug", 0},
	}
	for _, tc := range cases {
		if got := severityRank(tc.s); got != tc.want {
			t.Errorf("severityRank(%q) = %d, want %d", tc.s, got, tc.want)
		}
	}
}

// TestFirstParagraphOneLine — strips markdown noise and clamps length.
func TestFirstParagraphOneLine(t *testing.T) {
	cases := []struct {
		in, want string
		maxRunes int
	}{
		{"## Root\n\npg-replica-7 saturated", "pg-replica-7 saturated", 200},
		{"\n\n", "", 200},
		{"a very long line that exceeds the cap", "a very …", 8},
		// Bold section header must be skipped (not mangled into "现象**") —
		// land on the prose beneath. Matches the investigator's "**根因**"
		// / "**现象**" output format.
		{"**现象**\nPID 12345 /usr/bin/foo 吃满 CPU", "PID 12345 /usr/bin/foo 吃满 CPU", 200},
		{"**根因（0 号病人）**\npg-replica-7 主从复制中断", "pg-replica-7 主从复制中断", 200},
	}
	for _, tc := range cases {
		if got := firstParagraphOneLine(tc.in, tc.maxRunes); got != tc.want {
			t.Errorf("firstParagraphOneLine(%q, %d) = %q, want %q", tc.in, tc.maxRunes, got, tc.want)
		}
	}
}
