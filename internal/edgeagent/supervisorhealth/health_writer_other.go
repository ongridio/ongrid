//go:build !windows

// health_writer_other.go: stub for non-Windows platforms. A Linux edge has
// no supervisor.exe parent and needs no health.json IPC.

package supervisorhealth

import (
	"context"
	"log/slog"
)

// RunWriter is a no-op on non-Windows platforms (no supervisor
// is monitoring the process).
func RunWriter(_ context.Context, _ *slog.Logger) error {
	return nil
}
