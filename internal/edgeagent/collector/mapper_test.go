package collector

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestFlattenSamplesDropsNonFiniteValues(t *testing.T) {
	name := "demo_value"
	typ := dto.MetricType_GAUGE
	finite := 7.0
	nan := math.NaN()
	inf := math.Inf(1)

	samples := FlattenSamples(time.Unix(1, 0), "custom:demo", []*dto.MetricFamily{{
		Name: &name,
		Type: &typ,
		Metric: []*dto.Metric{
			gaugeMetric("kind", "finite", finite),
			gaugeMetric("kind", "nan", nan),
			gaugeMetric("kind", "inf", inf),
		},
	}}, nil)

	if len(samples) != 1 {
		t.Fatalf("len(samples) = %d, want 1: %#v", len(samples), samples)
	}
	if got := samples[0].Labels["kind"]; got != "finite" {
		t.Fatalf("remaining sample kind = %q, want finite", got)
	}
	if _, err := json.Marshal(tunnel.PushPromSamplesRequest{
		Source:  "custom:demo",
		Samples: samples,
	}); err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
}

func gaugeMetric(labelName, labelValue string, value float64) *dto.Metric {
	return &dto.Metric{
		Label: []*dto.LabelPair{{
			Name:  &labelName,
			Value: &labelValue,
		}},
		Gauge: &dto.Gauge{Value: &value},
	}
}

func TestCPUValidityDistinguishesMissingWarmupIdleAndReset(t *testing.T) {
	mapper := NewMapper()
	at := time.Unix(100, 0)
	if hp := mapper.MapToHostPoint(at, nil); hp.CPUSampleStatus != "unavailable" {
		t.Fatal(hp)
	}
	family := func(idle, user float64) []*dto.MetricFamily {
		name, label := "node_cpu_seconds_total", "mode"
		idleMode, userMode := "idle", "user"
		return []*dto.MetricFamily{{Name: &name, Metric: []*dto.Metric{
			{Label: []*dto.LabelPair{{Name: &label, Value: &idleMode}}, Counter: &dto.Counter{Value: &idle}},
			{Label: []*dto.LabelPair{{Name: &label, Value: &userMode}}, Counter: &dto.Counter{Value: &user}},
		}}}
	}
	if hp := mapper.MapToHostPoint(at, family(10, 10)); hp.CPUSampleStatus != "warming_up" {
		t.Fatal(hp)
	}
	if hp := mapper.MapToHostPoint(at.Add(time.Second), family(11, 10)); hp.CPUPct != 0 || hp.CPUSampleStatus != "valid" || hp.CPUWindowSeconds != 1 {
		t.Fatal(hp)
	}
	if hp := mapper.MapToHostPoint(at.Add(2*time.Second), family(12, 11)); hp.CPUPct != 50 || hp.CPUSampleStatus != "valid" {
		t.Fatal(hp)
	}
	if hp := mapper.MapToHostPoint(at.Add(3*time.Second), family(1, 1)); hp.CPUSampleStatus != "warming_up" {
		t.Fatal(hp)
	}
}
