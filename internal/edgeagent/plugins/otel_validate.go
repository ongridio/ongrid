package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// OTelConfigValidator returns a validator for an otelcol-contrib binary. Child
// output is discarded deliberately: Collector diagnostics can echo exporter
// headers and other rendered values. Operators receive a stable error class,
// while the previous config remains untouched.
func OTelConfigValidator(binary string) func(context.Context, string) error {
	return func(ctx context.Context, configPath string) error {
		cmd := exec.CommandContext(ctx, binary, "validate", "--config="+configPath)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("otelcol configuration validation timed out")
			}
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return fmt.Errorf("otelcol rejected configuration (exit %d)", exitErr.ExitCode())
			}
			return fmt.Errorf("run otelcol configuration validation: %w", err)
		}
		return nil
	}
}
