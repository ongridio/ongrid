package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type searchPolicyStub struct {
	stubConfigResolver
	fallback, engines string
	per, total        time.Duration
}

func (p searchPolicyStub) FallbackProvider(context.Context) string { return p.fallback }
func (p searchPolicyStub) SearxngEngines(context.Context) string   { return p.engines }
func (p searchPolicyStub) SearchTimeouts(context.Context) (time.Duration, time.Duration) {
	if p.per == 0 {
		return time.Second, 3 * time.Second
	}
	return p.per, p.total
}

func TestSearchEngineFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body, status string
		count              int
	}{
		{"all failed", `{"results":[],"unresponsive_engines":[["bing","timeout"],["baidu","CAPTCHA"]]}`, "failed", 0},
		{"empty", `{"results":[]}`, "no_results", 0},
		{"partial", `{"results":[{"title":"official","url":"https://example.com/a"}],"unresponsive_engines":[["bing","timeout"]]}`, "partial", 1},
		{"deduplicate", `{"results":[{"title":"a","url":"https://example.com/a"},{"title":"a","url":"https://example.com/a#x"},{"title":"unsafe","url":"javascript:alert(1)"}]}`, "success", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("engines") != "360search,sogou" {
					t.Error("engine selection lost")
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			s := &webSearchSkill{cfgResolver: searchPolicyStub{stubConfigResolver: stubConfigResolver{searxngURL: srv.URL}, engines: "360search,sogou"}}
			raw, err := s.Execute(t.Context(), json.RawMessage(`{"query":"test"}`))
			if err != nil {
				t.Fatal(err)
			}
			var out webSearchResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if out.Status != tc.status || len(out.Results) != tc.count {
				t.Fatalf("%s", raw)
			}
			if tc.status == "failed" && (len(out.EngineFailures) != 2 || out.SkippedReason == "") {
				t.Fatalf("failure hidden: %s", raw)
			}
		})
	}
}

func TestSearchFallbackIsExplicitAndBounded(t *testing.T) {
	for _, enable := range []bool{false, true} {
		t.Run(fmt.Sprint(enable), func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				fmt.Fprint(w, `{"results":[],"unresponsive_engines":[["bing","timeout"]]}`)
			}))
			defer primary.Close()
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				fmt.Fprint(w, `{"web":{"results":[{"title":"official","url":"https://example.com/answer"}]}}`)
			}))
			defer backup.Close()
			policy := searchPolicyStub{stubConfigResolver: stubConfigResolver{searxngURL: primary.URL, braveKey: "configured-key"}}
			if enable {
				policy.fallback = "brave"
			}
			s := &webSearchSkill{cfgResolver: policy, braveEndpoint: backup.URL}
			raw, err := s.Execute(t.Context(), json.RawMessage(`{"query":"test","provider":"searxng"}`))
			if err != nil {
				t.Fatal(err)
			}
			var out webSearchResponse
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatal(err)
			}
			if primaryCalls.Load() != 1 {
				t.Fatal("primary retry")
			}
			if enable {
				if backupCalls.Load() != 1 || out.Provider != "brave" || out.Status != "partial" || len(out.Attempts) != 2 {
					t.Fatalf("fallback: %s", raw)
				}
			} else if backupCalls.Load() != 0 || out.Status != "failed" {
				t.Fatal("unapproved fallback")
			}
		})
	}
}

func TestSearchAttemptTimeoutUsesBackupAndCancellationStops(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer primary.Close()
	var calls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"web":{"results":[{"title":"ok","url":"https://example.com"}]}}`)
	}))
	defer backup.Close()
	s := &webSearchSkill{braveEndpoint: backup.URL, cfgResolver: searchPolicyStub{stubConfigResolver: stubConfigResolver{searxngURL: primary.URL, braveKey: "key"}, fallback: "brave", per: 20 * time.Millisecond, total: time.Second}}
	raw, err := s.Execute(t.Context(), json.RawMessage(`{"query":"test"}`))
	if err != nil || !strings.Contains(string(raw), `"provider":"brave"`) {
		t.Fatalf("timeout fallback: %s %v", raw, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Execute(ctx, json.RawMessage(`{"query":"test"}`)); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("called backup after cancellation")
	}
}

func TestSearchCircuitCooldownRecoveryAndConfigurationChange(t *testing.T) {
	s := &webSearchSkill{}
	fingerprint := [32]byte{1}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !s.allowSearch("brave", fingerprint, now) {
			t.Fatal("closed too soon")
		}
		s.recordSearch("brave", fingerprint, true, now)
	}
	if s.allowSearch("brave", fingerprint, now) {
		t.Fatal("failed provider not suspended")
	}
	later := now.Add(61 * time.Second)
	if !s.allowSearch("brave", fingerprint, later) || s.allowSearch("brave", fingerprint, later) {
		t.Fatal("must admit one recovery probe")
	}
	s.recordSearch("brave", fingerprint, false, later)
	if !s.allowSearch("brave", fingerprint, later) {
		t.Fatal("recovery not accepted")
	}
	for i := 0; i < 3; i++ {
		s.recordSearch("brave", fingerprint, true, later)
	}
	if !s.allowSearch("brave", [32]byte{2}, later) {
		t.Fatal("new credentials inherited failure state")
	}
}
