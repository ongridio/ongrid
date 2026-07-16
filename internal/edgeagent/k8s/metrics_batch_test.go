package k8s

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

func TestMetricsBatcherHonorsSampleLimitAndContinuesAfterFailure(t *testing.T) {
	var batches [][]tunnel.PromSample
	batcher, err := newMetricsBatcher(41, k8sMetricsSource, 2, 1<<20, func(samples []tunnel.PromSample) error {
		batches = append(batches, samples)
		if len(batches) == 2 {
			return errors.New("temporary push failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("newMetricsBatcher() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		batcher.Add(tunnel.PromSample{Name: "kube_pod_info", Value: float64(i), TsMs: 1})
	}
	batcher.Flush()

	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(batches))
	}
	for i, batch := range batches {
		if len(batch) > 2 {
			t.Fatalf("batch %d has %d samples, want at most 2", i, len(batch))
		}
	}
	stats := batcher.Stats()
	if stats.SuccessfulBatches != 2 || stats.FailedBatches != 1 {
		t.Fatalf("batch stats = %#v, want 2 successful and 1 failed", stats)
	}
	if stats.SuccessfulSamples != 3 || stats.FailedSamples != 2 {
		t.Fatalf("sample stats = %#v, want 3 successful and 2 failed", stats)
	}
}

func TestMetricsBatcherHonorsEncodedByteLimit(t *testing.T) {
	sample := tunnel.PromSample{
		Name:   "kube_pod_labels",
		Labels: map[string]string{"namespace": "default", "pod": "api-1"},
		Value:  1,
		TsMs:   1,
	}
	oneSampleRequest, err := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  41,
		Source:  k8sMetricsSource,
		Samples: []tunnel.PromSample{sample},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	maxBytes := len(oneSampleRequest)
	var encodedSizes []int
	batcher, err := newMetricsBatcher(41, k8sMetricsSource, 100, maxBytes, func(samples []tunnel.PromSample) error {
		payload, marshalErr := json.Marshal(tunnel.PushPromSamplesRequest{
			EdgeID:  41,
			Source:  k8sMetricsSource,
			Samples: samples,
		})
		if marshalErr != nil {
			return marshalErr
		}
		encodedSizes = append(encodedSizes, len(payload))
		return nil
	})
	if err != nil {
		t.Fatalf("newMetricsBatcher() error = %v", err)
	}
	batcher.Add(sample, sample, sample)
	batcher.Flush()

	if len(encodedSizes) != 3 {
		t.Fatalf("encoded batches = %d, want 3", len(encodedSizes))
	}
	for i, size := range encodedSizes {
		if size > maxBytes {
			t.Fatalf("batch %d encoded size = %d, exceeds %d", i, size, maxBytes)
		}
	}
}

func TestMetricsBatcherHandlesDefaultLargeClusterLimit(t *testing.T) {
	var batches, samples int
	batcher, err := newMetricsBatcher(41, k8sMetricsSource, defaultK8sMetricsBatchSampleLimit, defaultK8sMetricsBatchByteLimit, func(batch []tunnel.PromSample) error {
		batches++
		samples += len(batch)
		if len(batch) > defaultK8sMetricsBatchSampleLimit {
			t.Fatalf("batch samples = %d, exceeds %d", len(batch), defaultK8sMetricsBatchSampleLimit)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("newMetricsBatcher() error = %v", err)
	}
	sample := tunnel.PromSample{
		Name:   "kube_pod_info",
		Labels: map[string]string{"namespace": "default", "pod": "api"},
		Value:  1,
		TsMs:   1,
	}
	for i := 0; i < defaultK8sMetricsLimit; i++ {
		batcher.Add(sample)
	}
	batcher.Flush()

	wantBatches := defaultK8sMetricsLimit / defaultK8sMetricsBatchSampleLimit
	if batches != wantBatches {
		t.Fatalf("batches = %d, want %d", batches, wantBatches)
	}
	if samples != defaultK8sMetricsLimit {
		t.Fatalf("samples = %d, want %d", samples, defaultK8sMetricsLimit)
	}
}
