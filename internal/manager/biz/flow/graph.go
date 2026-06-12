// graph.go — the canvas wire format and its validation. The frontend
// saves exactly this shape; the engine re-parses it on every run.
//
// Node I/O contract (HLD-016):
//   - every node resolves its config templates against the run context
//     ({{trigger.x}} / {{nodes.<id>.output.<path>}} / {{vars.<name>}}),
//     executes, and emits (dataOutput, controlPort).
//   - data flows through the shared run context, NOT along edges; edges
//     are control flow only. Plain nodes emit port "next"; condition
//     emits "true"/"false"; every node may emit "error".
//   - join semantics are OR with execute-once: a node activates the
//     first time any incoming edge fires and never re-executes within
//     a run. This keeps diamonds after a condition deadlock-free
//     without an explicit merge node (merge/parallel-join is P2).
package flow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Node types. Kept as plain strings in the wire format so the palette
// can grow without schema migrations.
const (
	NodeTriggerManual = "trigger.manual"
	NodeAgent         = "agent"
	NodeTool          = "tool"
	NodeCondition     = "condition"
	NodeNotify        = "notify"
	NodeSet           = "set"
)

// Control ports.
const (
	PortNext  = "next"
	PortTrue  = "true"
	PortFalse = "false"
	PortError = "error"
)

// GraphNode is one canvas node. Config is type-specific (see executors
// in nodes.go); Position is canvas-only and ignored by the engine.
type GraphNode struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Name     string          `json:"name,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
	Position *Position       `json:"position,omitempty"`
}

// Position is the canvas coordinate. Persisted verbatim for React Flow.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// GraphEdge is one control edge. SourcePort defaults to "next".
type GraphEdge struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourcePort string `json:"sourcePort,omitempty"`
	Target     string `json:"target"`
}

// Graph is the persisted canvas document.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

var knownTypes = map[string]bool{
	NodeTriggerManual: true,
	NodeAgent:         true,
	NodeTool:          true,
	NodeCondition:     true,
	NodeNotify:        true,
	NodeSet:           true,
}

var nodeIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// portsFor lists the legal source ports per node type. Every type also
// implicitly allows "error".
func portsFor(typ string) []string {
	switch typ {
	case NodeCondition:
		return []string{PortTrue, PortFalse}
	default:
		return []string{PortNext}
	}
}

// ParseGraph decodes and validates a canvas document. It is the single
// gate both Save and Execute go through, so a hand-edited DB row can't
// reach the executor in a shape it doesn't understand.
func ParseGraph(raw string) (*Graph, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return &Graph{}, nil
	}
	var g Graph
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		return nil, fmt.Errorf("graph: %w", err)
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return &g, nil
}

// Validate enforces structural invariants: unique well-formed node ids,
// known types, edges referencing existing nodes and legal ports, no
// inbound edges into triggers, and acyclicity.
func (g *Graph) Validate() error {
	byID := make(map[string]*GraphNode, len(g.Nodes))
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if !nodeIDRe.MatchString(n.ID) {
			return fmt.Errorf("graph: bad node id %q", n.ID)
		}
		if _, dup := byID[n.ID]; dup {
			return fmt.Errorf("graph: duplicate node id %q", n.ID)
		}
		if !knownTypes[n.Type] {
			return fmt.Errorf("graph: unknown node type %q (node %s)", n.Type, n.ID)
		}
		byID[n.ID] = n
	}
	adj := make(map[string][]string, len(g.Nodes))
	indeg := make(map[string]int, len(g.Nodes))
	for _, e := range g.Edges {
		src, ok := byID[e.Source]
		if !ok {
			return fmt.Errorf("graph: edge %s references missing source %q", e.ID, e.Source)
		}
		if _, ok := byID[e.Target]; !ok {
			return fmt.Errorf("graph: edge %s references missing target %q", e.ID, e.Target)
		}
		if strings.HasPrefix(byID[e.Target].Type, "trigger.") {
			return fmt.Errorf("graph: edge %s targets trigger %q", e.ID, e.Target)
		}
		port := e.SourcePort
		if port == "" {
			port = PortNext
		}
		if port != PortError {
			legal := false
			for _, p := range portsFor(src.Type) {
				if p == port {
					legal = true
					break
				}
			}
			if !legal {
				return fmt.Errorf("graph: edge %s uses port %q not exposed by %s node %q", e.ID, port, src.Type, e.Source)
			}
		}
		adj[e.Source] = append(adj[e.Source], e.Target)
		indeg[e.Target]++
	}
	// Kahn cycle check.
	queue := make([]string, 0, len(g.Nodes))
	seen := 0
	for id := range byID {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		seen++
		for _, t := range adj[id] {
			indeg[t]--
			if indeg[t] == 0 {
				queue = append(queue, t)
			}
		}
	}
	if seen != len(g.Nodes) {
		return fmt.Errorf("graph: cycle detected")
	}
	return nil
}

// Triggers returns the trigger nodes (execution entry points).
func (g *Graph) Triggers() []GraphNode {
	var out []GraphNode
	for _, n := range g.Nodes {
		if strings.HasPrefix(n.Type, "trigger.") {
			out = append(out, n)
		}
	}
	return out
}

// EdgesFrom returns the targets reachable from node id via port.
func (g *Graph) EdgesFrom(id, port string) []string {
	var out []string
	for _, e := range g.Edges {
		p := e.SourcePort
		if p == "" {
			p = PortNext
		}
		if e.Source == id && p == port {
			out = append(out, e.Target)
		}
	}
	return out
}
