//go:build linux

package collector

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveProcessSampleReportsCurrentWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := collectHostProcesses(ctx, 100000, "cpu")
	if err != nil {
		t.Fatal(err)
	}
	if result.CPUBasis != "host_total" || result.CPUWindowSeconds < 0.25 {
		t.Fatalf("missing current window: %+v", result)
	}
	for _, p := range result.Processes {
		if p.PID == int32(os.Getpid()) {
			if p.CPUSampleStatus != "valid" {
				t.Fatalf("self sample: %+v", p)
			}
			return
		}
	}
	t.Fatal("current process missing from readable process table")
}

func TestInteractiveHostLoadDoesNotConsumePeriodicBaseline(t *testing.T) {
	c, err := NewEmbedded(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.CollectAll(ctx); err != nil {
		t.Fatal(err)
	}
	c.mapper.mu.Lock()
	before := make(map[string]counterSample, len(c.mapper.last))
	for k, v := range c.mapper.last {
		before[k] = v
	}
	c.mapper.mu.Unlock()
	result, err := c.GetHostLoad(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.CPUSampleStatus != "valid" || result.CPUWindowSeconds < 0.25 {
		t.Fatalf("missing CPU sample: %+v", result)
	}
	c.mapper.mu.Lock()
	defer c.mapper.mu.Unlock()
	if len(before) != len(c.mapper.last) {
		t.Fatal("periodic baseline changed")
	}
	for k, v := range before {
		if c.mapper.last[k] != v {
			t.Fatalf("baseline changed for %s", k)
		}
	}
}
