package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestScrapeSnapshotsKeepAcquisitionIdentityAndRates(t *testing.T) {
	var calls atomic.Int64
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		n := calls.Add(1)
		_, err := fmt.Fprintf(w, "# TYPE node_cpu_seconds_total counter\nnode_cpu_seconds_total{cpu=\"0\",mode=\"idle\"} %d\nnode_cpu_seconds_total{cpu=\"0\",mode=\"user\"} %d\n# TYPE node_load1 gauge\nnode_load1 2.5\n# TYPE fixed gauge\nfixed 7 123456789\n", 100+10*n, 200+10*n)
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	target := ScrapeTarget{Name: "host", URL: srv.URL, Role: ScrapeRoleHost, Interval: 30 * time.Second, Timeout: time.Second}
	s := NewScraper(&ScrapeConfig{Targets: []ScrapeTarget{target}}, nil)
	read := func() CollectorOutput {
		t.Helper()
		out, err := s.CollectAll(context.Background())
		if err != nil || len(out) != 1 {
			t.Fatalf("outputs=%d err=%v", len(out), err)
		}
		return out[0]
	}
	s.scrapeOnce(context.Background(), target)
	first := read()
	if first.SnapshotID == 0 || !first.HostPointValid {
		t.Fatal("missing scrape identity")
	}
	s.scrapeOnce(context.Background(), target)
	second := read()
	if second.SnapshotID == first.SnapshotID || second.HostPoint.CPUPct != 50 {
		t.Fatalf("fresh snapshot/rate incorrect: %+v", second.HostPoint)
	}
	for range 3 {
		load, err := s.GetHostLoad(context.Background())
		if err != nil || load.CPUPct != 50 || load.SampledAt != second.HostPoint.Ts {
			t.Fatalf("load=%+v err=%v", load, err)
		}
		if got := read(); !reflect.DeepEqual(got, second) {
			t.Fatal("cached read changed timestamp, identity or counter rate")
		}
	}
	for _, sample := range second.Samples {
		if sample.Name == "fixed" && sample.TsMs != 123456789 {
			t.Fatalf("explicit timestamp changed: %d", sample.TsMs)
		}
		if sample.Name == "node_load1" && sample.TsMs != s.snapshot[target.Name].at.UnixMilli() {
			t.Fatal("sample was re-stamped after acquisition")
		}
	}
	fail.Store(true)
	s.scrapeOnce(context.Background(), target)
	if got := read(); !reflect.DeepEqual(got, second) {
		t.Fatal("failed scrape made stale data look fresh")
	}
	embedded := &countingEmbedded{}
	composite := NewComposite(embedded, s, nil)
	for range 3 {
		if _, err := composite.CollectAll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if embedded.calls != 0 {
		t.Fatal("cached host source incorrectly activated embedded fallback")
	}
}

type countingEmbedded struct {
	Collector
	calls int
}

func (c *countingEmbedded) CollectAll(context.Context) ([]CollectorOutput, error) {
	c.calls++
	return nil, nil
}
