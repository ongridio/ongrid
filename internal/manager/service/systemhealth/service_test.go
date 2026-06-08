package systemhealth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	edgemodel "github.com/ongridio/ongrid/internal/manager/model/edge"
	alertsvc "github.com/ongridio/ongrid/internal/manager/service/alert"
)

type fakeDB struct{ err error }

func (f fakeDB) PingContext(context.Context) error { return f.err }

type fakeProm struct{ err error }

func (f fakeProm) Query(context.Context, string, time.Time) (any, error) { return nil, f.err }

type fakeProbe struct{ err error }

func (f fakeProbe) Probe(context.Context) error { return f.err }

type fakeGrafana struct{ err error }

func (f fakeGrafana) Test(context.Context) error { return f.err }

type fakeRules struct {
	rules []*alertsvc.Rule
	err   error
}

func (f fakeRules) ListRules(context.Context, alertsvc.Caller, string) ([]*alertsvc.Rule, error) {
	return f.rules, f.err
}

type fakeIncidents struct {
	count int64
	err   error
}

func (f fakeIncidents) CountIncidents(context.Context, alertsvc.Caller, alertsvc.IncidentFilter) (int64, error) {
	return f.count, f.err
}

type fakeEdges struct {
	edges []*edgemodel.Edge
	err   error
}

func (f fakeEdges) List(context.Context, edgebiz.ListFilter) ([]*edgemodel.Edge, error) {
	return f.edges, f.err
}

func TestCheckAggregatesFailedDependency(t *testing.T) {
	t.Parallel()
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/ongrid_knowledge" {
			t.Fatalf("qdrant path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(qdrant.Close)

	svc := New(Config{
		Version:             "v-test",
		ProbeTimeout:        time.Second,
		PromEnabled:         true,
		LogsEnabled:         true,
		TracesEnabled:       true,
		AlertEnabled:        true,
		EvaluatorInterval:   5 * time.Minute,
		NotifyCooldown:      10 * time.Minute,
		FrontierAddr:        "frontier:40011",
		LLMConfigured:       true,
		EmbeddingConfigured: true,
		QdrantURL:           qdrant.URL,
		QdrantCollection:    "ongrid_knowledge",
	}, Dependencies{
		DB:      fakeDB{},
		Prom:    fakeProm{err: errors.New("prom down")},
		Grafana: fakeGrafana{},
		Loki:    fakeProbe{},
		Tempo:   fakeProbe{},
		Rules: fakeRules{rules: []*alertsvc.Rule{
			{ID: 1, RuleKey: "cpu_high", Enabled: true},
		}},
		Incidents: fakeIncidents{},
		Edges: fakeEdges{edges: []*edgemodel.Edge{
			{ID: 1, Status: edgemodel.StatusOnline},
		}},
	})

	report, err := svc.Check(context.Background(), alertsvc.Caller{UserID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", report.Status, StatusFailed)
	}
	if report.Summary.Failed != 1 {
		t.Fatalf("failed count = %d, want 1", report.Summary.Failed)
	}
	prom := findCheck(report, "prometheus")
	if prom == nil || prom.Status != StatusFailed {
		t.Fatalf("prometheus check = %+v, want failed", prom)
	}
	qdrantCheck := findCheck(report, "qdrant")
	if qdrantCheck == nil || qdrantCheck.Status != StatusOK {
		t.Fatalf("qdrant check = %+v, want ok", qdrantCheck)
	}
}

func TestCheckReportsDegradedWhenOptionalCapabilitiesMissing(t *testing.T) {
	t.Parallel()
	svc := New(Config{
		AlertEnabled:        true,
		FrontierDisabled:    true,
		LLMConfigured:       false,
		EmbeddingConfigured: false,
	}, Dependencies{
		DB: fakeDB{},
		Rules: fakeRules{rules: []*alertsvc.Rule{
			{ID: 1, RuleKey: "cpu_high", Enabled: true},
		}},
		Incidents: fakeIncidents{},
		Edges:     fakeEdges{},
	})

	report, err := svc.Check(context.Background(), alertsvc.Caller{UserID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.Status != StatusDegraded {
		t.Fatalf("status = %q, want %q", report.Status, StatusDegraded)
	}
	if report.Summary.Degraded == 0 {
		t.Fatalf("degraded count = 0, want > 0")
	}
}

func TestCheckReportsGrafanaMissingCredentialAsDegraded(t *testing.T) {
	t.Parallel()
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/ongrid_knowledge" {
			t.Fatalf("qdrant path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(qdrant.Close)

	svc := New(Config{
		Version:             "v-test",
		ProbeTimeout:        time.Second,
		PromEnabled:         true,
		LogsEnabled:         true,
		TracesEnabled:       true,
		AlertEnabled:        true,
		EvaluatorInterval:   5 * time.Minute,
		NotifyCooldown:      10 * time.Minute,
		FrontierAddr:        "frontier:40011",
		LLMConfigured:       true,
		EmbeddingConfigured: true,
		QdrantURL:           qdrant.URL,
		QdrantCollection:    "ongrid_knowledge",
	}, Dependencies{
		DB:      fakeDB{},
		Prom:    fakeProm{},
		Grafana: fakeGrafana{err: errors.New("grafana: sa_token / api_key empty (create a Grafana service account and paste its token, or paste an api_key for external Grafana)")},
		Loki:    fakeProbe{},
		Tempo:   fakeProbe{},
		Rules: fakeRules{rules: []*alertsvc.Rule{
			{ID: 1, RuleKey: "cpu_high", Enabled: true},
		}},
		Incidents: fakeIncidents{},
		Edges: fakeEdges{edges: []*edgemodel.Edge{
			{ID: 1, Status: edgemodel.StatusOnline},
		}},
	})

	report, err := svc.Check(context.Background(), alertsvc.Caller{UserID: 1, Role: "admin"})
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if report.Status != StatusDegraded {
		t.Fatalf("status = %q, want %q", report.Status, StatusDegraded)
	}
	if report.Summary.Failed != 0 {
		t.Fatalf("failed count = %d, want 0", report.Summary.Failed)
	}
	grafana := findCheck(report, "grafana")
	if grafana == nil || grafana.Status != StatusDegraded {
		t.Fatalf("grafana check = %+v, want degraded", grafana)
	}
}

func TestCheckEdgesReportsAccessStateSeparatelyFromPlatformHealth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		edges   []*edgemodel.Edge
		want    Status
		message string
	}{
		{
			name:    "no registered edge is ok",
			edges:   nil,
			want:    StatusOK,
			message: "no edge agent is registered; edge access is ready",
		},
		{
			name: "all sampled edges offline is failed",
			edges: []*edgemodel.Edge{
				{ID: 1, Status: edgemodel.StatusOffline},
				{ID: 2, Status: edgemodel.StatusOffline},
			},
			want:    StatusFailed,
			message: "all sampled edge agents are offline",
		},
		{
			name: "partial offline is degraded",
			edges: []*edgemodel.Edge{
				{ID: 1, Status: edgemodel.StatusOnline},
				{ID: 2, Status: edgemodel.StatusOffline},
			},
			want:    StatusDegraded,
			message: "1 sampled edge agent(s) are offline",
		},
		{
			name: "all online is ok",
			edges: []*edgemodel.Edge{
				{ID: 1, Status: edgemodel.StatusOnline},
				{ID: 2, Status: edgemodel.StatusOnline},
			},
			want:    StatusOK,
			message: "sampled edge agents are online",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Config{}, Dependencies{Edges: fakeEdges{edges: tc.edges}})
			check := svc.checkEdges(context.Background())
			if check.Status != tc.want {
				t.Fatalf("status = %q, want %q", check.Status, tc.want)
			}
			if check.Message != tc.message {
				t.Fatalf("message = %q, want %q", check.Message, tc.message)
			}
		})
	}
}

func findCheck(report *Report, id string) *Check {
	for i := range report.Checks {
		if report.Checks[i].ID == id {
			return &report.Checks[i]
		}
	}
	return nil
}
