//go:build windows

// health_writer_windows.go: worker.exe writes a heartbeat to health.json
// every HeartbeatInterval so supervisor.exe can monitor this process.
//
// This file is compiled on Windows only. Other platforms use the no-op
// stub in health_writer_other.go (a Linux edge has no supervisor parent).

package supervisorhealth

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/ongridio/ongrid/internal/edgeagent/edgedirs"
)

// RunWriter periodically writes health.json. It returns nil
// immediately when ctx is cancelled (no stopping state is written; the
// supervisor learns about worker exit via cmd.Wait).
//
// A failed write is logged but not returned as an error: a lost heartbeat
// makes the supervisor watchdog kill this process after HeartbeatTimeout,
// which is the intended fail-safe behavior.
func RunWriter(ctx context.Context, log *slog.Logger) error {
	pid := os.Getpid()
	startedAt := time.Now()

	// Write the starting state immediately so the supervisor sees the
	// worker early.
	h := Health{
		Version:       HealthSchemaVersion,
		WorkerPID:     pid,
		StartedAt:     startedAt,
		LastHeartbeat: time.Now(),
		Status:        StatusStarting,
	}
	writeHeartbeat(h, log)

	// Move to healthy.
	h.Status = StatusHealthy
	writeHeartbeat(h, log)

	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			h.LastHeartbeat = time.Now()
			writeHeartbeat(h, log)
		}
	}
}

func writeHeartbeat(h Health, log *slog.Logger) {
	if err := Write(edgedirs.HealthFile, h); err != nil {
		log.Error("heartbeat write failed", "path", edgedirs.HealthFile, "err", err)
	}
}
