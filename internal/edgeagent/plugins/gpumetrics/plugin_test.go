package gpumetrics

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestBuildArgs_defaults(t *testing.T) {
	cfg := plugins.PluginConfig{}
	args := buildArgs(cfg, "")
	if len(args) != 1 {
		t.Fatalf("expected 1 arg, got %d: %v", len(args), args)
	}
	want := "--web.listen-address=" + DefaultListenAddress
	if args[0] != want {
		t.Errorf("arg[0] = %q, want %q", args[0], want)
	}
}

func TestBuildArgs_customListen(t *testing.T) {
	cfg := plugins.PluginConfig{
		Spec: map[string]interface{}{
			"listen_address": ":9999",
		},
	}
	args := buildArgs(cfg, "")
	want := "--web.listen-address=:9999"
	if args[0] != want {
		t.Errorf("arg[0] = %q, want %q", args[0], want)
	}
}

func TestBuildArgs_extraArgs(t *testing.T) {
	cfg := plugins.PluginConfig{
		Spec: map[string]interface{}{
			"extra_args": []interface{}{"--foo", "--bar=baz"},
		},
	}
	args := buildArgs(cfg, "")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d: %v", len(args), args)
	}
	if args[1] != "--foo" || args[2] != "--bar=baz" {
		t.Errorf("unexpected args: %v", args)
	}
}

func TestBuildArgs_emptyListenFallsBack(t *testing.T) {
	cfg := plugins.PluginConfig{
		Spec: map[string]interface{}{
			"listen_address": "",
		},
	}
	args := buildArgs(cfg, "")
	want := "--web.listen-address=" + DefaultListenAddress
	if args[0] != want {
		t.Errorf("arg[0] = %q, want %q", args[0], want)
	}
}

func TestHasNVIDIASMI_deterministic(t *testing.T) {
	// Create a temp dir with a fake nvidia-smi to test the detection.
	tmpDir := t.TempDir()
	fakeSMI := filepath.Join(tmpDir, "nvidia-smi")
	if err := os.WriteFile(fakeSMI, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// With nvidia-smi in PATH
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+origPath)
	if !hasNVIDIASMILookPath() {
		t.Error("expected HasNVIDIASMI() = true when nvidia-smi is in PATH")
	}

	// Without nvidia-smi in PATH
	t.Setenv("PATH", tmpDir)
	if hasNVIDIASMILookPath() {
		t.Error("expected HasNVIDIASMI() = false when nvidia-smi is not in PATH")
	}
}

// hasNVIDIASMILookPath mirrors collector.HasNVIDIASMI for testing without
// importing the collector package (avoids test-time dependency on gopsutil).
func hasNVIDIASMILookPath() bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}
