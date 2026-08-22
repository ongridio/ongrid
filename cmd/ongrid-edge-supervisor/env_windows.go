// env_windows.go — read the service Environment registry field directly.
// Companion to mergedServiceEnv in worker.go: Windows SCM's promise of
// "service Environment field = process env" leaks under fork/exec of a
// different binary (supervisor.exe spawning worker.exe). Reading the
// field from the registry bypasses the SCM corner case and feeds the
// pairs into exec.Cmd.Env explicitly.

//go:build windows

package main

import (
	"golang.org/x/sys/windows/registry"
)

// serviceRegKeyPath is the HKLM path to the ongrid-edge service entry.
// Kept in sync with install_windows.go; duplicate literal avoids a
// shared const across the install / worker split.
const envRegKeyPath = `SYSTEM\CurrentControlSet\Services\` + serviceName

// readServiceEnvField reads the service Environment multi-string value
// from the registry. Returns the raw pairs (each "KEY=VALUE"), or an
// empty slice + error if the field is absent / unreadable. Callers
// should treat any error as "fall back to os.Environ()" — this is a
// best-effort correctness path, not a hard requirement.
func readServiceEnvField() ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, envRegKeyPath, registry.QUERY_VALUE|registry.READ)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	values, _, err := k.GetStringsValue("Environment")
	if err != nil {
		return nil, err
	}
	return values, nil
}
