package dataplane

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestDataPlaneDiagnosticsKeepsFixedPort(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				if host == "::1" {
					t.Skipf("IPv6 unavailable: %v", err)
				}
				t.Fatal(err)
			}
			defer listener.Close()
			t.Setenv("ONGRID_EDGE_METRICS_ADDR", listener.Addr().String())
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := runDataPlaneDiagnostics(t.Context(), prometheus.NewRegistry(), nil, log); err == nil {
				t.Fatal("K8s data plane silently changed its fixed port")
			}
		})
	}
}
