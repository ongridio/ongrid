package traces

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type receiverHandlerClient struct {
	tunnel.Client
	method  string
	handler tunnel.Handler
}

func (c *receiverHandlerClient) RegisterHandler(method string, handler tunnel.Handler) {
	c.method, c.handler = method, handler
}

type receiverHealthPlugin struct {
	plugins.Plugin
	state plugins.PluginState
}

func (p *receiverHealthPlugin) Name() string { return Name }
func (p *receiverHealthPlugin) HealthSnapshot() plugins.PluginHealth {
	return plugins.PluginHealth{Name: Name, State: p.state}
}

type instanceReceiverFunc func(context.Context, string, tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error)

func (f instanceReceiverFunc) ApplicationReceiver(ctx context.Context, path string, req tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error) {
	return f(ctx, path, req)
}

func TestApplicationReceiverHandlerRoutesOnlyRunningInstances(t *testing.T) {
	supervisor := plugins.NewSupervisor(plugins.SupervisorOpts{})
	plugin := &receiverHealthPlugin{state: plugins.StateStopped}
	supervisor.Register(plugin)
	client := &receiverHandlerClient{}
	calls := 0
	expectedErr := errors.New("instance failed")
	resolver := instanceReceiverFunc(func(_ context.Context, path string, request tunnel.ApplicationReceiverRequest) (tunnel.ApplicationReceiverResponse, error) {
		calls++
		if path != "/plugins/traces/otelcol.yaml" || request.InstanceID != "instance" {
			t.Fatalf("unexpected resolver arguments: %s %+v", path, request)
		}
		return tunnel.ApplicationReceiverResponse{}, expectedErr
	})
	RegisterApplicationReceiver(client, supervisor, "/plugins/traces/otelcol.yaml", resolver)
	if client.method != tunnel.MethodGetApplicationReceiver || client.handler == nil {
		t.Fatal("receiver handler not registered")
	}
	invoke := func(raw string) ([]byte, error) {
		return client.handler(t.Context(), tunnel.Session{}, "", []byte(raw))
	}
	if _, err := invoke(`{`); err == nil || !strings.Contains(err.Error(), "decode application receiver request") {
		t.Fatalf("invalid request error=%v", err)
	}
	raw, err := invoke(`{"instance_id":"instance"}`)
	var response tunnel.ApplicationReceiverResponse
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	if response.Reason != "receiver_unavailable" || calls != 0 {
		t.Fatalf("stopped receiver: %+v calls=%d", response, calls)
	}
	plugin.state = plugins.StateRunning
	if _, err := invoke(`{"instance_id":"instance"}`); !errors.Is(err, expectedErr) || calls != 1 {
		t.Fatalf("instance error=%v calls=%d", err, calls)
	}
	if _, err := invoke(`{}`); err == nil || !strings.Contains(err.Error(), "positive process_id required") || calls != 1 {
		t.Fatalf("generic receiver error=%v calls=%d", err, calls)
	}
	RegisterApplicationReceiver(client, supervisor, "/plugins/traces/otelcol.yaml", nil)
	if _, err := invoke(`{"instance_id":"instance"}`); err == nil || !strings.Contains(err.Error(), "positive process_id required") {
		t.Fatalf("gateway receiver error=%v", err)
	}
}
