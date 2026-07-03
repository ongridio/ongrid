package collector

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// HasNVIDIASMI reports whether nvidia-smi is available in PATH.
// Used by HostInfo to populate GPUAvailable and by the gpumetrics
// plugin to decide whether to start the exporter subprocess.
func HasNVIDIASMI() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

// NVIDIAGPUModel returns the first GPU model name reported by nvidia-smi,
// or empty string if nvidia-smi is unavailable or returns no data.
// Uses a 5-second timeout to handle cases where nvidia-smi hangs during
// GPU reset or driver faults.
func NVIDIAGPUModel(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		return ""
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	return strings.TrimSpace(lines[0])
}
