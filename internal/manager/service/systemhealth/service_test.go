package systemhealth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeDB struct{ err error }

func (f fakeDB) PingContext(context.Context) error { return f.err }

type fakeProm struct{ err error }

func (f fakeProm) Query(context.Context, string, time.Time) (any, error) { return nil, f.err }

type fakeProbe struct{ err error }

func (f fakeProbe) Probe(context.Context) error { return f.err }

type fakeGrafana struct{ err error }

func (f fakeGrafana) Test(context.Context) error { return f.err }

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
	})

	report, err := svc.Check(context.Background())
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
	if findCheck(report, "alert_engine") != nil || findCheck(report, "edges") != nil {
		t.Fatalf("non-system checks must not be included: %+v", report.Checks)
	}
}

func TestCheckReportsDegradedWhenOptionalCapabilitiesMissing(t *testing.T) {
	t.Parallel()
	svc := New(Config{
		FrontierDisabled:    true,
		LLMConfigured:       false,
		EmbeddingConfigured: false,
	}, Dependencies{
		DB: fakeDB{},
	})

	report, err := svc.Check(context.Background())
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
	})

	report, err := svc.Check(context.Background())
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

func findCheck(report *Report, id string) *Check {
	for i := range report.Checks {
		if report.Checks[i].ID == id {
			return &report.Checks[i]
		}
	}
	return nil
}
