//go:build darwin

package main

// Darwin default path constants. make build and the darwin release
// targets compile this file; Linux and Windows keep their own layouts
// in paths_linux.go and paths_windows.go.
//
// The directories match the Linux install layout so a locally built
// binary can share ONGRID_EDGE_* overrides with Linux. There is no
// DPAPI on Darwin, so secrets.enc is not loaded.

func defaultPluginBinDir() string { return "/usr/local/lib/ongrid-edge" }

func defaultPluginWorkDir() string { return "/var/lib/ongrid-edge/plugins" }

func defaultStageDir() string { return "/var/lib/ongrid-edge/.upgrade" }

func defaultSecretsFile() string { return "" }
