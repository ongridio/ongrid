package flow

import (
	"context"
	"sort"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
)

type triggerReproRepo struct {
	Repo
	flow *model.Flow
}

func (r *triggerReproRepo) Get(context.Context, uint64) (*model.Flow, error) {
	return r.flow, nil
}

func (r *triggerReproRepo) ListEnabled(context.Context) ([]*model.Flow, error) {
	return []*model.Flow{r.flow}, nil
}

type triggerReproRuns struct {
	*fakeRunRepo
	done chan *model.FlowRun
}

func (r *triggerReproRuns) UpdateRun(_ context.Context, run *model.FlowRun) error {
	r.done <- run
	return nil
}

func waitTriggerRun(t *testing.T, runs *triggerReproRuns) {
	t.Helper()
	select {
	case run := <-runs.done:
		if run.Status != model.RunStatusSucceeded {
			t.Fatalf("triggered run status = %s, error = %s", run.Status, run.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("triggered run did not finish")
	}
}

func TestReproCronOnlyDueBranchRuns(t *testing.T) {
	graph := `{
		"nodes": [
			{"id":"at-nine","type":"trigger.cron","config":{"cron":"0 9 * * *"}},
			{"id":"at-ten","type":"trigger.cron","config":{"cron":"0 10 * * *"}},
			{"id":"nine-action","type":"tool","config":{"tool":"nine_tool","args":{}}},
			{"id":"ten-action","type":"tool","config":{"tool":"ten_tool","args":{}}}
		],
		"edges": [
			{"id":"nine-edge","source":"at-nine","target":"nine-action"},
			{"id":"ten-edge","source":"at-ten","target":"ten-action"}
		]
	}`
	tools := &fakeTools{}
	runs := &triggerReproRuns{fakeRunRepo: &fakeRunRepo{}, done: make(chan *model.FlowRun, 1)}
	repo := &triggerReproRepo{flow: &model.Flow{ID: 1, Name: "cron repro", GraphJSON: graph, Enabled: true, Version: 1}}
	uc := NewUsecase(repo, runs, NewEngine(Executors{Tools: tools}, runs, nil), nil)
	scheduler := NewScheduler(uc, nil)

	// The first scan arms both nodes; only the 09:00 node is due on the next scan.
	scheduler.tick(t.Context(), time.Date(2026, 10, 22, 8, 59, 0, 0, time.UTC))
	scheduler.tick(t.Context(), time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC))

	waitTriggerRun(t, runs)

	tools.mu.Lock()
	calls := append([]string(nil), tools.calls...)
	tools.mu.Unlock()
	sort.Strings(calls)
	if len(calls) != 1 || calls[0] != "nine_tool" {
		t.Fatalf("BUG: at 09:00 UTC only at-nine is due, but tool calls = %v; ten_tool belongs to the 10:00 trigger", calls)
	}
}

func TestAlertRunsOnlyMatchingTrigger(t *testing.T) {
	graph := `{
		"nodes": [
			{"id":"disk-alert","type":"trigger.alert_fired","config":{"rule":"disk"}},
			{"id":"cpu-alert","type":"trigger.alert_fired","config":{"rule":"cpu"}},
			{"id":"disk-action","type":"tool","config":{"tool":"disk_tool","args":{}}},
			{"id":"cpu-action","type":"tool","config":{"tool":"cpu_tool","args":{}}}
		],
		"edges": [
			{"id":"disk-edge","source":"disk-alert","target":"disk-action"},
			{"id":"cpu-edge","source":"cpu-alert","target":"cpu-action"}
		]
	}`
	tools := &fakeTools{}
	runs := &triggerReproRuns{fakeRunRepo: &fakeRunRepo{}, done: make(chan *model.FlowRun, 1)}
	repo := &triggerReproRepo{flow: &model.Flow{ID: 1, Name: "alert repro", GraphJSON: graph, Enabled: true, Version: 1}}
	uc := NewUsecase(repo, runs, NewEngine(Executors{Tools: tools}, runs, nil), nil)

	NewDispatcher(uc, nil).dispatch(1, "cpu_high", "CPU high", "critical", 0, 0, nil, time.Now())
	waitTriggerRun(t, runs)

	tools.mu.Lock()
	calls := append([]string(nil), tools.calls...)
	tools.mu.Unlock()
	if len(calls) != 1 || calls[0] != "cpu_tool" {
		t.Fatalf("CPU alert tool calls = %v, want [cpu_tool] only", calls)
	}
}

func TestTriggerEventRejectsRemovedEntryNode(t *testing.T) {
	repo := &triggerReproRepo{flow: &model.Flow{
		ID: 1, Enabled: true, GraphJSON: `{"nodes":[{"id":"new-cron","type":"trigger.cron"}]}`,
	}}
	runs := &fakeRunRepo{}
	uc := NewUsecase(repo, runs, NewEngine(Executors{}, runs, nil), nil)
	if _, err := uc.TriggerEvent(t.Context(), 1, NodeTriggerCron, "removed-cron", nil); err == nil {
		t.Fatal("removed cron node must not start another trigger")
	}
	if _, err := uc.TriggerEvent(t.Context(), 1, NodeTriggerCron, "", nil); err == nil {
		t.Fatal("event trigger requires a node ID")
	}
}
