package traces

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// InstanceReceiver resolves a configured Auto APM application instance.
type InstanceReceiver interface {
	ApplicationReceiver(context.Context, string, tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error)
}

// RegisterApplicationReceiver exposes the running traces receiver through the tunnel.
func RegisterApplicationReceiver(client tunnel.Client, supervisor *plugins.Supervisor, configPath string, instanceResolver InstanceReceiver) {
	client.RegisterHandler(tunnel.MethodGetApplicationReceiver, func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var request tunnel.ApplicationReceiverRequest
		if err := json.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode application receiver request: %w", err)
		}
		for _, health := range supervisor.HealthSnapshots() {
			if health.Name != Name || health.State != plugins.StateRunning {
				continue
			}
			var receiver tunnel.ApplicationReceiverResponse
			var err error
			if instanceResolver != nil && request.InstanceID != "" {
				receiver, err = instanceResolver.ApplicationReceiver(ctx, configPath, request)
			} else {
				receiver, err = ApplicationReceiver(ctx, configPath, request)
			}
			if err != nil {
				return nil, err
			}
			return json.Marshal(receiver)
		}
		return json.Marshal(tunnel.ApplicationReceiverResponse{Reason: "receiver_unavailable"})
	})
}
