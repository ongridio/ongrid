//go:build darwin

package main

import (
	"log/slog"

	edgehostfiles "github.com/ongridio/ongrid/internal/edgeagent/host_files"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// registerEdgeCapabilities registers the platform-specific edge capability
// handlers with the tunnel client. Darwin registers host_files, which has
// a Darwin implementation. bash and restart_service stay Linux-only
// (cmdpolicy / systemd).
//
// Register is soft-fail: the edge keeps booting and the operator sees a
// warning when the capability is disabled.
func registerEdgeCapabilities(client tunnel.Client, log *slog.Logger) {
	if err := edgehostfiles.Register(client, log); err != nil {
		log.Warn("host_files register failed; capability disabled", slog.Any("err", err))
	}
}
