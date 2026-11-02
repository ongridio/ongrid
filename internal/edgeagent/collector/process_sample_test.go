package collector

import (
	"context"
	"testing"
)

func TestProcessCPUWholeHostWindow(t *testing.T) {
	got, status := processCPUPercent(10, 10.5, 0.5, 4)
	if got != 25 || status != "valid" {
		t.Fatalf("got %v %s", got, status)
	}
	if got, status := processCPUPercent(10, 10, 1, 4); got != 0 || status != "valid" {
		t.Fatal("idle process is valid zero")
	}
	for _, v := range [][4]float64{{10, 9, 1, 4}, {1, 2, 0, 4}, {1, 2, 1, 0}} {
		if _, status := processCPUPercent(v[0], v[1], v[2], int(v[3])); status != "unavailable" {
			t.Fatal("invalid sample accepted")
		}
	}
}
func TestProcessSampleCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectHostProcesses(ctx, 3, "cpu"); err == nil {
		t.Fatal("expected cancellation")
	}
}
