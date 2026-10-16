package plugins

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type wireHealthPlugin struct {
	Plugin
	health PluginHealth
}

func (p wireHealthPlugin) Name() string                 { return p.health.Name }
func (p wireHealthPlugin) HealthSnapshot() PluginHealth { return p.health }

type reloadHandlerClient struct {
	tunnel.Client
	method  string
	handler tunnel.Handler
}

func (c *reloadHandlerClient) RegisterHandler(method string, handler tunnel.Handler) {
	c.method, c.handler = method, handler
}

func TestHealthWirePreservesFieldsAndZeroTimestamps(t *testing.T) {
	supervisor := NewSupervisor(SupervisorOpts{})
	supervisor.Register(wireHealthPlugin{health: PluginHealth{Name: "zero", State: StateStopped, Targets: []TargetHealth{{ID: "zero"}}}})
	supervisor.Register(wireHealthPlugin{health: PluginHealth{Name: "active", State: StateRunning, LastError: "error", DiscoveryError: "discovery", RestartCount: 2, PID: 42, StartedAt: time.Unix(100, 0), UpdatedAt: time.Unix(200, 0), Targets: []TargetHealth{{ID: "db", Name: "Database", Kind: "mysql", State: "healthy", LastError: "target-error", Samples: 3, LastSuccessAt: time.Unix(150, 0), UpdatedAt: time.Unix(180, 0)}}}})
	want := []tunnel.PluginHealthWire{
		{Name: "active", State: "running", LastError: "error", DiscoveryError: "discovery", RestartCount: 2, PID: 42, StartedAt: 100, UpdatedAt: 200, Targets: []tunnel.PluginTargetHealthWire{{ID: "db", Name: "Database", Kind: "mysql", State: "healthy", LastError: "target-error", Samples: 3, LastSuccessAt: 150, UpdatedAt: 180}}},
		{Name: "zero", State: "stopped", Targets: []tunnel.PluginTargetHealthWire{{ID: "zero"}}},
	}
	if got := supervisor.HealthWire(); !reflect.DeepEqual(got, want) {
		t.Fatalf("health wire = %+v, want %+v", got, want)
	}
}

func TestRegisterReloadHandlerCoalescesNotifications(t *testing.T) {
	supervisor := NewSupervisor(SupervisorOpts{})
	client := &reloadHandlerClient{}
	supervisor.RegisterReloadHandler(client)
	if client.method != tunnel.MethodPluginConfigsChanged || client.handler == nil {
		t.Fatal("reload handler not registered")
	}
	for range 2 {
		body, err := client.handler(context.Background(), tunnel.Session{}, "", nil)
		if err != nil || string(body) != "{}" {
			t.Fatalf("response = %s, %v", body, err)
		}
	}
	if len(supervisor.reloadSignal) != 1 {
		t.Fatal("reload notifications were not coalesced")
	}
}
