package collector

import (
	"os/exec"
	"strings"
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
func NVIDIAGPUModel() string {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}
