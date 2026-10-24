package flow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/flow"
)

// fakeFlowRepo serves one mutable enabled flow with a single cron trigger.
type fakeFlowRepo struct {
	mu   sync.Mutex
	flow model.Flow
}

func (r *fakeFlowRepo) setCron(spec string) {
	cfg, _ := json.Marshal(map[string]string{"cron": spec})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flow = model.Flow{ID: 1, Enabled: true, GraphJSON: `{"nodes":[{"id":"c","type":"trigger.cron","config":` + string(cfg) + `}]}`}
}

func (r *fakeFlowRepo) Create(context.Context, *model.Flow) error { return nil }
func (r *fakeFlowRepo) Update(context.Context, *model.Flow) error { return nil }
func (r *fakeFlowRepo) Get(context.Context, uint64) (*model.Flow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.flow
	return &f, nil
}
func (r *fakeFlowRepo) List(context.Context, int, int) ([]*model.Flow, int64, error) {
	return nil, 0, nil
}
func (r *fakeFlowRepo) ListEnabled(ctx context.Context) ([]*model.Flow, error) {
	f, _ := r.Get(ctx, 1)
	return []*model.Flow{f}, nil
}
func (r *fakeFlowRepo) Delete(context.Context, uint64) error { return nil }

// countingRunRepo counts launched runs (CreateRun runs synchronously in
// triggerRun, before the run goroutine starts).
type countingRunRepo struct {
	fakeRunRepo
	mu sync.Mutex
	n  int
}

func (c *countingRunRepo) CreateRun(context.Context, *model.FlowRun) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return nil
}

func (c *countingRunRepo) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Editing a trigger's cron must re-arm it from the new spec instead of
// keeping the next fire computed from the old one, while a save that leaves
// the spec unchanged keeps the armed schedule.
func TestSchedulerRearmsEditedCron(t *testing.T) {
	at := func(hms string) time.Time {
		ts, err := time.Parse(time.RFC3339, "2026-10-22T"+hms+"Z")
		if err != nil {
			t.Fatalf("parse %q: %v", hms, err)
		}
		return ts
	}
	cases := []struct {
		name, from, to string
		ticks          []string
		wantFires      int
	}{
		{name: "rare to frequent", from: "0 0 1 1 *", to: "*/5 * * * *", ticks: []string{"10:00:30", "10:05:30", "10:10:30"}, wantFires: 2},
		{name: "frequent to rare", from: "*/5 * * * *", to: "0 0 1 1 *", ticks: []string{"10:01:00", "10:05:30"}, wantFires: 0},
		{name: "timezone removed", from: "CRON_TZ=Asia/Shanghai 0 11 * * *", to: "0 11 * * *", ticks: []string{"10:01:00", "11:00:30"}, wantFires: 1},
		{name: "unchanged spec", from: "*/5 * * * *", to: "*/5 * * * *", ticks: []string{"10:02:00", "10:05:30"}, wantFires: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeFlowRepo{}
			runs := &countingRunRepo{}
			s := NewScheduler(NewUsecase(repo, runs, NewEngine(Executors{}, &fakeRunRepo{}, nil), nil), nil)

			repo.setCron(tc.from)
			s.tick(context.Background(), at("10:00:00")) // first sighting arms tc.from
			repo.setCron(tc.to)
			for _, hms := range tc.ticks {
				s.tick(context.Background(), at(hms))
			}

			if got := runs.count(); got != tc.wantFires {
				t.Fatalf("%q -> %q: %d fires, want %d", tc.from, tc.to, got, tc.wantFires)
			}
			s.mu.Lock()
			armed := len(s.next)
			s.mu.Unlock()
			if armed != 1 {
				t.Fatalf("%d schedules armed after the edit, want 1 (the old spec's entry must be swept)", armed)
			}
		})
	}
}
