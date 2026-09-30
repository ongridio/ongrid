package tunnel

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/golang/snappy"
	"github.com/singchia/geminio/packet"
)

const (
	// MetricsCompressionSnappy is the registration capability for compressed metrics.
	MetricsCompressionSnappy = "snappy"
	// The initial NUL makes this unambiguously invalid legacy JSON. Old
	// handlers reject it before ingestion, allowing a safe JSON fallback.
	metricsSnappyPrefix        = "\x00OGMS\x01"
	minMetricsCompressionBytes = 1024
	maxMetricsDecodedBytes     = 16 << 20
	// Geminio base64-encodes payloads inside a JSON frame capped at 10 MiB.
	maxMetricsBatchBytes = 6 << 20
	// Reserve 1 KiB for RPC metadata, deadlines, and Frontier's Edge-ID tail.
	maxMetricsWireBytes = (packet.MaxDecodablePacketLen - 1024) / 4 * 3
)

func checkMetricsWireSize(method string, body []byte) error {
	if metricsItemsField(method) != "" && len(body) > maxMetricsWireBytes {
		return fmt.Errorf("%s: encoded metrics payload %d exceeds tunnel budget %d: %w", method, len(body), maxMetricsWireBytes, packet.ErrPacketTooLarge)
	}
	return nil
}

func encodeMetricsRequest(method string, body []byte) []byte {
	if (method != MethodPushPromSamples && method != MethodPushHostMetrics) ||
		len(body) < minMetricsCompressionBytes || len(body) > maxMetricsDecodedBytes {
		return body
	}
	compressed := snappy.Encode(nil, body)
	if len(metricsSnappyPrefix)+len(compressed) >= len(body) {
		return body
	}
	return append([]byte(metricsSnappyPrefix), compressed...)
}

// DecodeMetricsRequest accepts legacy JSON or negotiated Snappy-compressed JSON.
// dst is the concrete request type owned by the caller, as with json.Unmarshal.
func DecodeMetricsRequest(body []byte, dst any) error {
	if bytes.HasPrefix(body, []byte(metricsSnappyPrefix)) {
		compressed := body[len(metricsSnappyPrefix):]
		// Inspect the advertised decoded size before Snappy allocates memory.
		if len(compressed) > maxMetricsDecodedBytes {
			return fmt.Errorf("compressed metrics request exceeds %d bytes", maxMetricsDecodedBytes)
		}
		n, err := snappy.DecodedLen(compressed)
		if err != nil {
			return fmt.Errorf("metrics snappy length: %w", err)
		}
		if n > maxMetricsDecodedBytes {
			return fmt.Errorf("decoded metrics request exceeds %d bytes", maxMetricsDecodedBytes)
		}
		body, err = snappy.Decode(nil, compressed)
		if err != nil {
			return fmt.Errorf("metrics snappy decode: %w", err)
		}
	}
	return json.Unmarshal(body, dst)
}

// Only this exact legacy decoder error proves that the batch was not ingested.
// Timeouts, transport failures and ingestion errors must never trigger a resend.
func legacyMetricsEncodingError(method string, err error) bool {
	return err != nil && err.Error() == method+": decode: invalid character '\\x00' looking for beginning of value"
}
