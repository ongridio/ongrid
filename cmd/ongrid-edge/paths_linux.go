//go:build linux

package main

// Linux default path constants (created by install.sh). The Windows
// counterparts live in paths_windows.go, and Darwin in paths_darwin.go.
// These were extracted from the
// hardcoded paths in main.go as part of the platform split via build
// tags: main.go only calls defaultXxx() and is unaware of the OS.

// defaultPluginBinDir is the fallback when ONGRID_EDGE_PLUGIN_BIN_DIR is
// unset. Points at the install directory of subprocess binaries
// (node_exporter / otelcol / ...).
func defaultPluginBinDir() string { return "/usr/local/lib/ongrid-edge" }

// defaultPluginWorkDir is the fallback when ONGRID_EDGE_PLUGIN_WORK_DIR is
// unset. Working directory for subprocesses (state files / pid files /
// scratch output).
func defaultPluginWorkDir() string { return "/var/lib/ongrid-edge/plugins" }

// defaultStageDir is the fallback when ONGRID_EDGE_UPGRADE_STAGE_DIR is
// unset. Staging directory for bundle upgrades.
func defaultStageDir() string { return "/var/lib/ongrid-edge/.upgrade" }

// defaultSecretsFile returns the default path of the DPAPI-encrypted
// secrets.enc. There is no DPAPI on Linux, so it returns an empty string
// (secrets.enc is never loaded).
func defaultSecretsFile() string { return "" }
