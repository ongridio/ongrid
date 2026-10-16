package chatruntime

import (
	"context"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// originTool is a minimal BaseTool whose Info carries a configurable Origin /
// Class, for exercising the role filter.
type originTool struct{ name, class, origin, confirmation string }

func (o *originTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: o.name, Description: "x", Class: o.class, Origin: o.origin, Confirmation: o.confirmation}, nil
}

func TestFilterToolsForAgentRole_ViewerDropsSensitiveReadTool(t *testing.T) {
	bag := []basetool.BaseTool{
		&originTool{name: "query_devices", class: "read"},
		&originTool{name: "capture_pcap", class: "read", confirmation: basetool.ConfirmationRequired},
	}
	view := filterToolsForAgentRole(bag, nil, true, true)
	if !toolbagHas(view, "query_devices") || toolbagHas(view, "capture_pcap") {
		t.Fatalf("viewer tools = %+v; sensitive read tool must be removed", view)
	}
}
func (o *originTool) InvokableRun(context.Context, string, ...basetool.InvokeOption) (string, error) {
	return "{}", nil
}

func toolbagHas(tools []basetool.BaseTool, name string) bool {
	for _, t := range tools {
		if i, _ := t.Info(context.Background()); i != nil && i.Name == name {
			return true
		}
	}
	return false
}

// Dynamic (runtime-discovered) read tools are in scope for BOTH the coordinator
// and specialists (they can't be pre-listed in a persona whitelist). A viewer
// still drops non-read dynamic tools. (We briefly excluded dynamic tools from
// the coordinator to force delegation, but that made MCP unreachable with the
// weak deployed model — see worker.go history note — so they're back.)
func TestFilterToolsForAgentRole_DynamicReadTools(t *testing.T) {
	bag := []basetool.BaseTool{
		&originTool{name: "rank_edges", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "mcp__k8s__namespaces_list", class: "read", origin: basetool.OriginMCP},
		&originTool{name: "mcp__k8s__nodes_drain", class: "destructive", origin: basetool.OriginMCP},
		&originTool{name: "skill__custom__foo", class: "read", origin: basetool.OriginSkill},
	}

	// coordinator + specialist both keep dynamic READ tools (so MCP is reachable)
	for _, isCoord := range []bool{true, false} {
		got := filterToolsForAgentRole(bag, nil, isCoord, false)
		for _, n := range []string{"rank_edges", "mcp__k8s__namespaces_list", "skill__custom__foo"} {
			if !toolbagHas(got, n) {
				t.Errorf("isCoordinator=%v should keep %s", isCoord, n)
			}
		}
	}

	// viewer: non-read dynamic tools are dropped (read ones survive)
	view := filterToolsForAgentRole(bag, nil, false, true /*viewerOnly*/)
	if !toolbagHas(view, "mcp__k8s__namespaces_list") {
		t.Errorf("viewer should keep a READ dynamic tool")
	}
	if toolbagHas(view, "mcp__k8s__nodes_drain") {
		t.Errorf("viewer must drop a DESTRUCTIVE dynamic tool")
	}
}

func TestFilterToolsForAgentRole_NetworkInventoryStaysWithNetworkSpecialist(t *testing.T) {
	bag := []basetool.BaseTool{
		&originTool{name: "query_devices", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "query_network_devices", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "query_network_interfaces", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "get_network_neighbors", class: "read", origin: basetool.OriginBuiltin},
		&originTool{name: "ToolSearch", class: "read", origin: basetool.OriginBuiltin},
	}

	coordinator := &Agent{Tools: []string{"query_devices"}}
	coordinatorTools := filterToolsForAgentRole(bag, coordinator, true, false)
	for _, name := range []string{"query_network_devices", "query_network_interfaces", "get_network_neighbors"} {
		if toolbagHas(coordinatorTools, name) {
			t.Errorf("coordinator must not receive network inventory tool %q", name)
		}
	}

	networkSpecialist := &Agent{Tools: []string{
		"query_network_devices",
		"query_network_interfaces",
		"get_network_neighbors",
		"ToolSearch",
	}}
	networkTools := filterToolsForAgentRole(bag, networkSpecialist, false, false)
	for _, name := range networkSpecialist.Tools {
		if !toolbagHas(networkTools, name) {
			t.Errorf("network specialist missing tool %q", name)
		}
	}
}
