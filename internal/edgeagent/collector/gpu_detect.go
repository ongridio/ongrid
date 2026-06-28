package collector

import "os/exec"

// HasNVIDIASMI reports whether nvidia-smi is available in PATH.
// Used by HostInfo to populate GPUAvailable and by the gpumetrics
// plugin to decide whether to start the exporter subprocess.
func HasNVIDIASMI() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}
