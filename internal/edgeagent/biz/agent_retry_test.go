package biz

import (
	"testing"
	"time"
)

func TestHeartbeatRetryDelayBounds(t *testing.T) {
	for _, tc := range []struct {
		interval time.Duration
		failures int
		limit    time.Duration
	}{
		{time.Nanosecond, 0, time.Nanosecond},
		{time.Nanosecond, 63, time.Minute},
		{30 * time.Second, 0, 30 * time.Second},
		{30 * time.Second, 1, time.Minute},
		{30 * time.Second, 32, time.Minute},
		{2 * time.Minute, 32, 2 * time.Minute},
	} {
		for range 20 {
			got := heartbeatRetryDelay(tc.interval, tc.failures)
			if got < tc.limit-tc.limit/10 || got > tc.limit {
				t.Fatalf("delay(%s, %d) = %s, want 90-100%% of %s", tc.interval, tc.failures, got, tc.limit)
			}
		}
	}
}
