package ci

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	topologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// TopologyGraph adapts the instance side to the topology walk's ports.
//
// THE ROOT QUERY IS BUILT HERE, not stored as a compiled node: a view stores the
// text a person wrote, and compiling it at read time keeps the expression
// language in the one package that also compiles a search request. The type is
// always added, so a root expression that names no model cannot pull instances
// the view's central model would not draw.
type TopologyGraph struct {
	uc *Usecase
}

// NewTopologyGraph builds the adapter.
func NewTopologyGraph(uc *Usecase) *TopologyGraph { return &TopologyGraph{uc: uc} }

var _ topologybiz.GraphQueries = (*TopologyGraph)(nil)

// Roots returns the view's root instances.
func (g *TopologyGraph) Roots(ctx context.Context, typeID model.ID, expr string, limit int) ([]topologybiz.Node, error) {
	page, err := g.uc.Search(ctx, SearchRequest{
		Query:  rootQuery(typeID, expr),
		Count:  limit,
		RetKey: RetKeyName,
	})
	if err != nil {
		return nil, err
	}
	out := make([]topologybiz.Node, 0, len(page.Hits))
	for _, hit := range page.Hits {
		name := ""
		if hit.UniqueAlias != "" {
			name = valueText(hit.Values[hit.UniqueAlias])
		}
		if name == "" && hit.UniqueAttr != "" {
			name = valueText(hit.Values[hit.UniqueAttr])
		}
		out = append(out, topologybiz.Node{ID: hit.ID, TypeID: typeID, Name: name})
	}
	return out, nil
}

// Children returns the downward edges from a frontier.
func (g *TopologyGraph) Children(ctx context.Context, ids []model.ID) ([]topologybiz.GraphEdge, error) {
	rows, err := g.uc.repo.GraphChildren(ctx, ids)
	if err != nil {
		return nil, err
	}
	return graphEdges(rows), nil
}

// Parents returns the upward edges into a frontier.
func (g *TopologyGraph) Parents(ctx context.Context, ids []model.ID) ([]topologybiz.GraphEdge, error) {
	rows, err := g.uc.repo.GraphParents(ctx, ids)
	if err != nil {
		return nil, err
	}
	return graphEdges(rows), nil
}

// Names labels a batch of instances.
func (g *TopologyGraph) Names(ctx context.Context, ids []model.ID) (map[model.ID]string, error) {
	return NewHistoryCINames(g.uc).ResolveCINames(ctx, ids)
}

// Icons returns each model's stored icon value.
func (g *TopologyGraph) Icons(ctx context.Context, typeIDs []model.ID) (map[model.ID]string, error) {
	out := make(map[model.ID]string, len(typeIDs))
	for _, id := range typeIDs {
		set, err := g.uc.types.ResolveForCI(ctx, fmt.Sprint(id))
		if err != nil {
			return nil, err
		}
		if set == nil || set.Type.Icon == nil {
			continue
		}
		out[id] = *set.Type.Icon
	}
	return out, nil
}

func graphEdges(rows []GraphEdge) []topologybiz.GraphEdge {
	out := make([]topologybiz.GraphEdge, 0, len(rows))
	for _, row := range rows {
		out = append(out, topologybiz.GraphEdge{
			From: row.From, To: row.To, FromTypeID: row.FromTypeID, ToTypeID: row.ToTypeID,
		})
	}
	return out
}

// rootQuery pins the model and accepts the two forms a view stores: a query
// expression, and a comma-separated list of instance ids.
func rootQuery(typeID model.ID, raw string) string {
	expr := strings.TrimSpace(raw)
	if strings.HasPrefix(expr, "q=") {
		expr = strings.TrimSpace(expr[2:])
	}
	if expr == "" {
		return fmt.Sprintf("_type:%d", typeID)
	}
	if ids, ok := idList(expr); ok {
		return fmt.Sprintf("_type:%d,_id:(%s)", typeID, strings.Join(ids, ";"))
	}
	return fmt.Sprintf("_type:%d,%s", typeID, expr)
}

func idList(raw string) ([]string, bool) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err != nil {
			return nil, false
		}
		out = append(out, part)
	}
	return out, len(out) > 0
}

func valueText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := valueText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return strings.TrimSpace(fmt.Sprint(raw))
	}
}
