package plugins

import (
	"context"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// HealthWire projects plugin status for the agent heartbeat.
func (s *Supervisor) HealthWire() []tunnel.PluginHealthWire {
	snaps := s.HealthSnapshots()
	out := make([]tunnel.PluginHealthWire, 0, len(snaps))
	for _, s := range snaps {
		targets := make([]tunnel.PluginTargetHealthWire, 0, len(s.Targets))
		for _, th := range s.Targets {
			wth := tunnel.PluginTargetHealthWire{
				ID:        th.ID,
				Name:      th.Name,
				Kind:      th.Kind,
				State:     th.State,
				LastError: th.LastError,
				Samples:   th.Samples,
			}
			if !th.LastSuccessAt.IsZero() {
				wth.LastSuccessAt = th.LastSuccessAt.Unix()
			}
			if !th.UpdatedAt.IsZero() {
				wth.UpdatedAt = th.UpdatedAt.Unix()
			}
			targets = append(targets, wth)
		}
		w := tunnel.PluginHealthWire{
			Name:           s.Name,
			Candidates:     s.Candidates,
			DiscoveryError: s.DiscoveryError,
			State:          string(s.State),
			LastError:      s.LastError,
			RestartCount:   s.RestartCount,
			PID:            s.PID,
			Targets:        targets,
		}
		if !s.StartedAt.IsZero() {
			w.StartedAt = s.StartedAt.Unix()
		}
		if !s.UpdatedAt.IsZero() {
			w.UpdatedAt = s.UpdatedAt.Unix()
		}
		out = append(out, w)
	}
	return out
}

// RegisterReloadHandler receives manager configuration change notifications.
func (s *Supervisor) RegisterReloadHandler(client tunnel.Client) {
	client.RegisterHandler(tunnel.MethodPluginConfigsChanged, func(_ context.Context, _ tunnel.Session, _ string, _ []byte) ([]byte, error) {
		s.TriggerReload()
		return []byte(`{}`), nil
	})
}
