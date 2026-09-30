package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
)

func metricsItemsField(method string) string {
	switch method {
	case MethodPushPromSamples:
		return "samples"
	case MethodPushHostMetrics:
		return "points"
	default:
		return ""
	}
}

// forEachMetricsBatch uses the same exact JSON-size accounting as the K8s
// metrics batcher. RawMessage preserves numbers and unknown metadata fields.
// Only oversized requests take this path; one batch buffer is reused after
// each synchronous send. No sample is skipped to make a batch fit.
func forEachMetricsBatch(ctx context.Context, body []byte, field string, limit int, send func([]byte, int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(body, &metadata); err != nil {
		return fmt.Errorf("decode metrics request: %w", err)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(metadata[field], &items); err != nil {
		return fmt.Errorf("decode metrics %s: %w", field, err)
	}
	delete(metadata, field)
	header, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode metrics metadata: %w", err)
	}
	prefix := header[:len(header)-1] // Reopen the object and append its sample array.
	if len(metadata) > 0 {
		prefix = append(prefix, ',')
	}
	prefix = append(prefix, '"')
	prefix = append(prefix, field...)
	prefix = append(prefix, '"', ':', '[')
	baseBytes := len(prefix) + 2 // closing array and object
	if baseBytes > limit {
		// Metadata cannot be split without changing the protocol. Preserve the
		// full request; callJSON still checks whether its wire encoding fits.
		return send(body, len(items))
	}
	batch := make([]byte, 0, min(limit, len(body)))
	batch = append(batch, prefix...)
	count := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		separator := 0
		if count > 0 {
			separator = 1
		}
		if count > 0 && len(batch)+separator+len(item)+2 > limit {
			if err := send(append(batch, ']', '}'), count); err != nil {
				return err
			}
			batch = append(batch[:0], prefix...)
			count = 0
		}
		if count > 0 {
			batch = append(batch, ',')
		}
		// An indivisible item travels alone. callJSON compresses it if possible
		// and rejects it if it cannot fit the transport; nothing is cut.
		batch = append(batch, item...)
		count++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return send(append(batch, ']', '}'), count)
}

func (c *geminioClient) callMetricsBatches(ctx context.Context, method string, body []byte) ([]byte, error) {
	accepted := 0
	err := forEachMetricsBatch(ctx, body, metricsItemsField(method), maxMetricsBatchBytes, func(batch []byte, count int) error {
		data, err := c.callJSON(ctx, method, batch)
		if err != nil {
			return fmt.Errorf("metrics batch after %d confirmed items: %w", accepted, err)
		}
		var result PushPromSamplesResponse // Both metric RPCs return an integer accepted count.
		if err := json.Unmarshal(data, &result); err != nil {
			return fmt.Errorf("decode metrics batch response: %w", err)
		}
		if result.Accepted != count {
			return fmt.Errorf("metrics batch accepted %d of %d items after %d confirmed items", result.Accepted, count, accepted)
		}
		accepted += count
		return nil
	})
	// Even on failure, expose only the fully acknowledged prefix. Callers can
	// resume from it without replaying successful batches; no automatic retry.
	data, encodeErr := json.Marshal(PushPromSamplesResponse{Accepted: accepted})
	if encodeErr != nil {
		return nil, fmt.Errorf("encode metrics batch response: %w", encodeErr)
	}
	return data, err
}
