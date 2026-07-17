package metricscommon

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestParseTextSamplePreservesFlatPrometheusSeries(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantName   string
		wantLabels map[string]string
		wantValue  float64
		wantTs     int64
		wantOK     bool
	}{
		{
			name:       "escaped labels and timestamp",
			line:       `demo_total{slash="a\\b",quote="a\"b",newline="a\nb"} 12.5 123`,
			wantName:   "demo_total",
			wantLabels: map[string]string{"slash": `a\b`, "quote": `a"b`, "newline": "a\nb", "cluster": "test"},
			wantValue:  12.5,
			wantTs:     123,
			wantOK:     true,
		},
		{
			name:       "histogram bucket remains flat",
			line:       `request_duration_seconds_bucket{le="0.5"} 7`,
			wantName:   "request_duration_seconds_bucket",
			wantLabels: map[string]string{"le": "0.5", "cluster": "test"},
			wantValue:  7,
			wantTs:     999,
			wantOK:     true,
		},
		{
			name:       "whitespace before label set",
			line:       `demo_total { service = "api", }3`,
			wantName:   "demo_total",
			wantLabels: map[string]string{"service": "api", "cluster": "test"},
			wantValue:  3,
			wantTs:     999,
			wantOK:     true,
		},
		{name: "comment", line: "# TYPE demo_total counter", wantOK: false},
		{name: "non-finite", line: "demo_value NaN", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sample, ok, err := parseTextSample([]byte(tt.line), 999, map[string]string{"cluster": "test"})
			if err != nil {
				t.Fatalf("parseTextSample() error = %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if sample.Name != tt.wantName || sample.Value != tt.wantValue || sample.TsMs != tt.wantTs {
				t.Fatalf("sample = %#v, want name=%q value=%v ts=%d", sample, tt.wantName, tt.wantValue, tt.wantTs)
			}
			if len(sample.Labels) != len(tt.wantLabels) {
				t.Fatalf("labels = %#v, want %#v", sample.Labels, tt.wantLabels)
			}
			for key, want := range tt.wantLabels {
				if got := sample.Labels[key]; got != want {
					t.Fatalf("label %q = %q, want %q", key, got, want)
				}
			}
		})
	}
}

func TestParseTextSampleRejectsMalformedLines(t *testing.T) {
	for _, line := range []string{
		`bad-name 1`,
		`demo{label="unterminated} 1`,
		`demo{label="a",label="b"} 1`,
		`demo 1 not-a-timestamp`,
		`demo 1 123 trailing`,
	} {
		t.Run(line, func(t *testing.T) {
			if _, _, err := parseTextSample([]byte(line), 999, nil); err == nil {
				t.Fatalf("parseTextSample(%q) error = nil", line)
			}
		})
	}
}

func TestScrapeIncrementalStreamsDefaultLargeClusterLimit(t *testing.T) {
	const sampleLimit = 250000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		writer := bufio.NewWriterSize(w, 64<<10)
		for i := 0; i <= sampleLimit; i++ {
			if _, err := writer.WriteString("kube_pod_info{namespace=\"default\",pod=\"api\"} 1\n"); err != nil {
				return
			}
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)

	accepted := 0
	stats, err := ScrapeIncremental(context.Background(), Target{
		URL:         srv.URL + "/metrics",
		Timeout:     15 * time.Second,
		SampleLimit: sampleLimit,
	}, func(samples []tunnel.PromSample) error {
		if len(samples) > streamSampleChunkSize {
			t.Fatalf("chunk samples = %d, exceeds %d", len(samples), streamSampleChunkSize)
		}
		accepted += len(samples)
		return nil
	})
	if err != nil {
		t.Fatalf("ScrapeIncremental() error = %v", err)
	}
	if stats.Accepted != sampleLimit || stats.Observed != sampleLimit+1 || !stats.LimitExceeded {
		t.Fatalf("stats = %#v, want accepted=%d observed=%d limit exceeded", stats, sampleLimit, sampleLimit+1)
	}
	if accepted != sampleLimit {
		t.Fatalf("consumer accepted = %d, want %d", accepted, sampleLimit)
	}
}

func FuzzParseTextSample(f *testing.F) {
	for _, seed := range []string{
		`demo_total{service="api"} 1`,
		`request_duration_seconds_bucket{le="+Inf"} 10 123`,
		`# HELP demo_total demo`,
		`demo{label="a\nb"} NaN`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		sample, ok, err := parseTextSample([]byte(line), 1, nil)
		if err != nil {
			return
		}
		if ok && sample.Name == "" {
			t.Fatal("parsed sample has an empty name")
		}
	})
}
