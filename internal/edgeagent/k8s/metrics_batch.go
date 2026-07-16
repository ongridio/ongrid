package k8s

import (
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type metricsBatchStats struct {
	SuccessfulBatches int
	FailedBatches     int
	SuccessfulSamples int
	FailedSamples     int
	RejectedSamples   int
	FirstError        error
}

type metricsBatcher struct {
	maxSamples       int
	maxBytes         int
	baseEncodedBytes int
	encodedBytes     int
	samples          []tunnel.PromSample
	push             func([]tunnel.PromSample) error
	stats            metricsBatchStats
}

func newMetricsBatcher(edgeID uint64, source string, maxSamples, maxBytes int, push func([]tunnel.PromSample) error) (*metricsBatcher, error) {
	if maxSamples <= 0 {
		return nil, fmt.Errorf("batch sample limit must be positive")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("batch byte limit must be positive")
	}
	if push == nil {
		return nil, fmt.Errorf("batch push function is required")
	}
	empty, err := json.Marshal(tunnel.PushPromSamplesRequest{
		EdgeID:  edgeID,
		Source:  source,
		Samples: []tunnel.PromSample{},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal empty batch: %w", err)
	}
	// The empty array brackets remain in the populated request; each sample
	// adds its encoded size and subsequent samples add one comma separator.
	baseBytes := len(empty)
	if baseBytes >= maxBytes {
		return nil, fmt.Errorf("batch byte limit %d is smaller than request metadata %d", maxBytes, baseBytes)
	}
	return &metricsBatcher{
		maxSamples:       maxSamples,
		maxBytes:         maxBytes,
		baseEncodedBytes: baseBytes,
		encodedBytes:     baseBytes,
		samples:          make([]tunnel.PromSample, 0, min(maxSamples, 1024)),
		push:             push,
	}, nil
}

func (b *metricsBatcher) Add(samples ...tunnel.PromSample) {
	for _, sample := range samples {
		encoded, err := json.Marshal(sample)
		if err != nil {
			b.rejectSample(fmt.Errorf("marshal sample %q: %w", sample.Name, err))
			continue
		}
		separatorBytes := 0
		if len(b.samples) > 0 {
			separatorBytes = 1
		}
		nextBytes := b.encodedBytes + separatorBytes + len(encoded)
		if len(b.samples) >= b.maxSamples || nextBytes > b.maxBytes {
			b.Flush()
			separatorBytes = 0
			nextBytes = b.encodedBytes + len(encoded)
		}
		if nextBytes > b.maxBytes {
			b.rejectSample(fmt.Errorf("encoded sample %q is %d bytes and exceeds batch byte limit %d", sample.Name, len(encoded), b.maxBytes))
			continue
		}
		b.samples = append(b.samples, sample)
		b.encodedBytes = nextBytes
	}
}

func (b *metricsBatcher) Flush() {
	if len(b.samples) == 0 {
		return
	}
	count := len(b.samples)
	batch := append([]tunnel.PromSample(nil), b.samples...)
	if err := b.push(batch); err != nil {
		b.stats.FailedBatches++
		b.stats.FailedSamples += count
		if b.stats.FirstError == nil {
			b.stats.FirstError = err
		}
	} else {
		b.stats.SuccessfulBatches++
		b.stats.SuccessfulSamples += count
	}
	clear(b.samples)
	b.samples = b.samples[:0]
	b.encodedBytes = b.baseEncodedBytes
}

func (b *metricsBatcher) Stats() metricsBatchStats {
	return b.stats
}

func (b *metricsBatcher) rejectSample(err error) {
	b.stats.RejectedSamples++
	if b.stats.FirstError == nil {
		b.stats.FirstError = err
	}
}
