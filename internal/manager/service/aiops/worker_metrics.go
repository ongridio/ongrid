package aiops

import (
	"context"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// RunWorkerMetrics samples active worker sessions until shutdown.
func RunWorkerMetrics(ctx context.Context, runtime interface{ CountWorkersByStatus() (running, pending int) }) error {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			running, pending := runtime.CountWorkersByStatus()
			prom.SetWorkerSessions(running, pending)
		}
	}
}
