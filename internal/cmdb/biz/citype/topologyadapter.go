package citype

import (
	"context"

	topologybiz "github.com/ongridio/ongrid/internal/cmdb/biz/topology"
)

// TopologyModelRelations adapts the model-relation graph to the topology path
// picker.
//
// THE PATH PICKER NEEDS NAMES, NOT ONLY IDS: a view's path is "host → switch →
// rack", and an operator choosing the next model should see the model, not a
// numeric id. The relation type and cardinality travel with it because the
// picker shows why the models may be walked.
type TopologyModelRelations struct {
	uc *Usecase
}

// NewTopologyModelRelations builds the adapter.
func NewTopologyModelRelations(uc *Usecase) *TopologyModelRelations {
	return &TopologyModelRelations{uc: uc}
}

var _ topologybiz.ModelRelations = (*TopologyModelRelations)(nil)

// RelatedTypes returns the models reachable below and above a model.
func (r *TopologyModelRelations) RelatedTypes(ctx context.Context, typeRef string) (children, parents []topologybiz.RelatedModel, err error) {
	below, err := r.uc.ListRelatedTypes(ctx, typeRef, true)
	if err != nil {
		return nil, nil, err
	}
	above, err := r.uc.ListRelatedTypes(ctx, typeRef, false)
	if err != nil {
		return nil, nil, err
	}
	return relatedModels(below), relatedModels(above), nil
}

func relatedModels(rows []RelatedType) []topologybiz.RelatedModel {
	out := make([]topologybiz.RelatedModel, 0, len(rows))
	for _, row := range rows {
		out = append(out, topologybiz.RelatedModel{
			TypeID: row.Type.Type.ID, Name: row.Type.Type.Name, Alias: row.Type.Type.Alias,
			RelationTypeID: row.RelationType.ID, Constraint: row.Constraint,
		})
	}
	return out
}
