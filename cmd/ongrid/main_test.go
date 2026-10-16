package main

import (
	"reflect"
	"testing"
)

func TestStartupCleanupRunsInReverseOrder(t *testing.T) {
	var closed []string
	start := func() {
		var cleanup startupCleanup
		defer cleanup.run()
		cleanup.add(func() { closed = append(closed, "tracing") })
		cleanup.add(func() { closed = append(closed, "frontier") })
	}
	start()
	if !reflect.DeepEqual(closed, []string{"frontier", "tracing"}) {
		t.Fatalf("cleanup order = %v", closed)
	}
}

func TestStartupCleanupContinuesAfterPanic(t *testing.T) {
	var closed []string
	cleanup := startupCleanup{
		func() { closed = append(closed, "tracing") },
		func() { panic("frontier shutdown") },
		func() { closed = append(closed, "investigator") },
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		cleanup.run()
	}()
	if recovered != "frontier shutdown" {
		t.Fatalf("cleanup panic = %v", recovered)
	}
	if !reflect.DeepEqual(closed, []string{"investigator", "tracing"}) {
		t.Fatalf("cleanup skipped a hook after panic: %v", closed)
	}
}
