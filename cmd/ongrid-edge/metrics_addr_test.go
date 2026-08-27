package main

import "testing"

func TestEdgeMetricsListenAddr_UsesDefaultAndEnvironmentOverride(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("ONGRID_EDGE_METRICS_ADDR", "")
		if got := edgeMetricsListenAddr(); got != edgeMetricsAddr {
			t.Fatalf("edgeMetricsListenAddr() = %q, want %q", got, edgeMetricsAddr)
		}
	})

	t.Run("environment override", func(t *testing.T) {
		t.Setenv("ONGRID_EDGE_METRICS_ADDR", ":19101")
		if got := edgeMetricsListenAddr(); got != ":19101" {
			t.Fatalf("edgeMetricsListenAddr() = %q, want %q", got, ":19101")
		}
	})
}
