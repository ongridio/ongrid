//go:build linux

package main

import (
	"log/slog"

	edgebash "github.com/ongridio/ongrid/internal/edgeagent/bash"
	edgehostfiles "github.com/ongridio/ongrid/internal/edgeagent/host_files"
	edgerestartservice "github.com/ongridio/ongrid/internal/edgeagent/restart_service"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// registerEdgeCapabilities registers the platform-specific edge capability
// handlers with the tunnel client. Linux side: host_files / restart_service
// / bash. Windows (capabilities_windows.go) and Darwin
// (capabilities_darwin.go) register host_files only — bash and
// restart_service depend on cmdpolicy/systemd, which are Linux-only.
//
// All Register calls are soft-fail: the edge keeps booting and the
// operator sees a warning in the log when a capability is disabled. A
// single failing skill must not take down the whole agent.
func registerEdgeCapabilities(client tunnel.Client, log *slog.Logger) {
	if err := edgehostfiles.Register(client, log); err != nil {
		log.Warn("host_files register failed; capability disabled", slog.Any("err", err))
	}
	if err := edgerestartservice.Register(client, log); err != nil {
		log.Warn("restart_service register failed; capability disabled", slog.Any("err", err))
	}
	if err := edgebash.Register(client, log); err != nil {
		log.Warn("bash register failed; capability disabled", slog.Any("err", err))
	}
}
