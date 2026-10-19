package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	topologybiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
)

const (
	ToolNameFindTopologyNode = "find_topology_node"

	// findTopologyNodeMaxLimit caps how many rows the LLM can pull in
	// one shot — anything more is noise that crowds out reasoning.
	findTopologyNodeMaxLimit     = 50
	findTopologyNodeDefaultLimit = 20
	findTopologyNodeCallTimeout  = 60 * time.Second
)

const findTopologyNodeDescription = "Search the business topology by name substring (case-insensitive). " +
	"Returns matching nodes with id, name, and type — call this when the user named a service / cluster / app and you need its node_id " +
	"to feed into expand_topology or to link in a reply."

const findTopologyNodeWhenToUse = "Before expand_topology when you only have a human-given name. " +
	"Example: user says 'what does loki-write depend on?' — call find_topology_node{name='loki-write'} to get its node_id, then expand_topology on that. " +
	"NOT for devices (use query_devices) and NOT for fuzzy guessing of what the user meant (let the LLM ask back)."

var findTopologyNodeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {
      "type": "string",
      "description": "Case-insensitive substring of Node.Name to match. Required."
    },
    "type": {
      "type": "string",
      "description": "Optional exact-match filter on Node.Type (e.g. 'service', 'cluster', 'app', 'device'). Leave empty to search across all kinds.",
      "examples": ["service", "cluster", "app", "device", "rack"]
    },
    "limit": {
      "type": "integer",
      "description": "Cap on returned rows. Default 20, max 50.",
      "default": 20
    }
  },
  "required": ["name"]
}`)

type findTopologyNodeArgs struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type findTopologyNodeHit struct {
	NodeID uint64 `json:"node_id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

type findTopologyNodeResult struct {
	Query    string                `json:"query"`
	Type     string                `json:"type_filter,omitempty"`
	Total    int64                 `json:"total"`
	Returned int                   `json:"returned"`
	Hits     []findTopologyNodeHit `json:"hits"`
	Note     string                `json:"note,omitempty"`
}

type FindTopologyNodeTool struct {
	topology *topologybiz.Usecase
	log      *slog.Logger
}

func NewFindTopologyNodeTool(topology *topologybiz.Usecase, log *slog.Logger) *FindTopologyNodeTool {
	if log == nil {
		log = slog.Default()
	}
	return &FindTopologyNodeTool{topology: topology, log: log}
}

func (t *FindTopologyNodeTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameFindTopologyNode,
		Description: findTopologyNodeDescription,
		WhenToUse:   findTopologyNodeWhenToUse,
		Parameters:  findTopologyNodeSchema,
		Class:       "read",
	}, nil
}

func (t *FindTopologyNodeTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.topology == nil {
		return "", fmt.Errorf("find_topology_node: topology usecase not configured")
	}
	var in findTopologyNodeArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("find_topology_node: bad args: %w", err)
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return "", fmt.Errorf("find_topology_node: name is required")
	}
	if in.Limit <= 0 {
		in.Limit = findTopologyNodeDefaultLimit
	}
	clamped := false
	if in.Limit > findTopologyNodeMaxLimit {
		in.Limit = findTopologyNodeMaxLimit
		clamped = true
	}

	callCtx, cancel := context.WithTimeout(ctx, findTopologyNodeCallTimeout)
	defer cancel()

	nodes, total, err := t.topology.ListNodes(callCtx, topologybiz.NodeListFilter{
		Type:  strings.TrimSpace(in.Type),
		Q:     in.Name,
		Limit: in.Limit,
	})
	if err != nil {
		return "", fmt.Errorf("find_topology_node: list: %w", err)
	}

	hits := make([]findTopologyNodeHit, 0, len(nodes))
	for _, n := range nodes {
		hits = append(hits, findTopologyNodeHit{
			NodeID: n.ID,
			Name:   n.Name,
			Type:   n.Type,
		})
	}

	out := findTopologyNodeResult{
		Query:    in.Name,
		Type:     in.Type,
		Total:    total,
		Returned: len(hits),
		Hits:     hits,
	}
	if clamped {
		out.Note = "limit clamped to 50"
	} else if total > int64(len(hits)) {
		out.Note = fmt.Sprintf("%d more results not returned — narrow the query or raise limit", total-int64(len(hits)))
	}

	body, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("find_topology_node: marshal: %w", err)
	}
	return string(body), nil
}
