package alertconfig

import (
	"testing"
	"time"
)

func TestCompactAlertPreviewLimitsLongSeriesAndSamples(t *testing.T) {
	now := time.Now()
	in := &PreviewResult{}
	for i := 0; i < 200; i++ {
		in.Series = append(in.Series, PreviewSeriesPoint{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Value:     float64(i),
		})
		in.Samples = append(in.Samples, PreviewSample{
			Timestamp: now.Add(time.Duration(i) * time.Minute),
			Value:     float64(i),
			Summary:   "sample",
		})
	}

	got := compactAlertPreview(in)
	if got == nil {
		t.Fatal("compactAlertPreview() = nil")
	}
	if len(got.Series) != configDraftPreviewSeriesLimit {
		t.Fatalf("Series len = %d, want %d", len(got.Series), configDraftPreviewSeriesLimit)
	}
	if len(got.Samples) != configDraftPreviewSampleLimit {
		t.Fatalf("Samples len = %d, want %d", len(got.Samples), configDraftPreviewSampleLimit)
	}
	if got.Series[0].Value != 0 || got.Series[len(got.Series)-1].Value != 199 {
		t.Fatalf("Series should preserve first and last point, got first=%v last=%v", got.Series[0].Value, got.Series[len(got.Series)-1].Value)
	}
	if got.Samples[0].Value != 0 || got.Samples[len(got.Samples)-1].Value != 199 {
		t.Fatalf("Samples should preserve first and last point, got first=%v last=%v", got.Samples[0].Value, got.Samples[len(got.Samples)-1].Value)
	}
	if len(in.Series) != 200 || len(in.Samples) != 200 {
		t.Fatalf("compactAlertPreview mutated input lengths: series=%d samples=%d", len(in.Series), len(in.Samples))
	}
}
