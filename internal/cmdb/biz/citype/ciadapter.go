package citype

import (
	"context"
	"fmt"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/cmdb/model/value"
)

// CIResolver adapts this package's model definitions to the instance side's
// consumer-declared port.
//
// THE DEPENDENCY POINTS THIS WAY ON PURPOSE. `biz/ci` declares the interface it
// needs and knows nothing about how a model's attribute set is derived; this
// package implements it. The reverse — instances importing model configuration
// — would put the derivation rule in the consumer and give the codebase two
// copies of the one rule the two modules must agree on exactly: an effective
// attribute set spans the model's whole inheritance chain, and which binding
// wins where two of them name the same attribute is a decision with a right
// answer, not a preference.
//
// It is an adapter rather than a method on the use case so the wiring is
// explicit at the composition root and neither package has to know the other's
// shape.
type CIResolver struct {
	uc *Usecase
}

// NewCIResolver builds the adapter.
func NewCIResolver(uc *Usecase) *CIResolver { return &CIResolver{uc: uc} }

var _ cibiz.TypeResolver = (*CIResolver)(nil)

// ResolveForCI resolves a model by numeric id or exact name and returns its
// effective attribute set, or (nil, nil) when nothing matches.
func (r *CIResolver) ResolveForCI(ctx context.Context, ref string) (*cibiz.AttributeSet, error) {
	view, err := r.uc.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	effective, err := r.uc.effectiveAttributes(ctx, view)
	if err != nil {
		return nil, err
	}

	set := &cibiz.AttributeSet{
		Type:         view.Type,
		Specs:        make(map[model.ID]cibiz.AttributeSpec, len(effective.Attributes)),
		ByName:       make(map[string]model.ID, len(effective.Attributes)),
		ByAlias:      make(map[string]model.ID, len(effective.Attributes)),
		UniqueAttrID: effective.UniqueID,
	}

	// The effective set is already ordered and deduplicated by the composition
	// rule, so a name or alias collision is resolved by taking the FIRST — the
	// same "position from the first binding seen" rule that decides the set's
	// order. Resolving it differently here would let a request address an
	// attribute the console cannot show.
	for _, item := range effective.Attributes {
		spec := cibiz.AttributeSpec{
			ID:        item.ID,
			Name:      item.Name,
			Alias:     item.Alias,
			ValueType: value.ValueType(item.ValueType),

			IsList:     item.IsList,
			IsIndex:    item.IsIndex,
			IsUnique:   item.IsUnique,
			IsRequired: item.IsRequired,
			IsDynamic:  item.IsDynamic,

			IsPassword: item.IsPassword,
			IsLink:     item.IsLink,
			IsFile:     item.IsFile,

			IsComputed:       item.IsComputed,
			HasComputeScript: item.HasComputeScript,

			Default: item.Default,
		}
		set.Specs[spec.ID] = spec
		if _, seen := set.ByName[spec.Name]; !seen {
			set.ByName[spec.Name] = spec.ID
		}
		if spec.Alias != "" {
			if _, seen := set.ByAlias[spec.Alias]; !seen {
				set.ByAlias[spec.Alias] = spec.ID
			}
		}
		if spec.ID == effective.UniqueID {
			set.UniqueAttr = spec
			token, _, _ := cibiz.ClassifyDefault(spec.Default)
			set.UniqueDefault = token
		}
	}

	// The labelling attribute is `show_id or unique_id`, cantor's own rule. A
	// `show_id` that names an attribute the model does not carry falls back
	// rather than being reported: the value is used for a title, and a missing
	// title is a worse outcome than a title that is the identifier.
	set.ShowAttr = set.UniqueAttr
	if view.Type.ShowID != nil {
		if spec, ok := set.Specs[*view.Type.ShowID]; ok {
			set.ShowAttr = spec
		}
	}
	return set, nil
}

// DiscoveryAttributeResolver answers the discovery pool's "which attributes
// does this model carry" question.
//
// IT IS THE SAME EFFECTIVE SET the instance write uses — inherited attributes
// included — because the pool's columns must match what an accept will write.
// A resolver that listed only the model's own bindings would hide an inherited
// attribute from the pool and then accept it happily, which is the sort of
// disagreement a console shows as a permanently empty column.
type DiscoveryAttributeResolver struct {
	uc *Usecase
}

// NewDiscoveryAttributeResolver builds the adapter.
func NewDiscoveryAttributeResolver(uc *Usecase) *DiscoveryAttributeResolver {
	return &DiscoveryAttributeResolver{uc: uc}
}

var _ discoverybiz.AttributeResolver = (*DiscoveryAttributeResolver)(nil)

// ResolveAttributes returns the model's effective attributes in binding order.
func (r *DiscoveryAttributeResolver) ResolveAttributes(ctx context.Context, typeID model.ID) ([]discoverybiz.AttributeRef, error) {
	view, err := r.uc.GetByRef(ctx, fmt.Sprint(typeID))
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	effective, err := r.uc.effectiveAttributes(ctx, view)
	if err != nil {
		return nil, err
	}
	out := make([]discoverybiz.AttributeRef, 0, len(effective.Attributes))
	for _, item := range effective.Attributes {
		out = append(out, discoverybiz.AttributeRef{
			ID: item.ID, Name: item.Name, Alias: item.Alias,
			ValueType: item.ValueType, IsList: item.IsList,
		})
	}
	return out, nil
}

// RelationBetween answers the instance side's question about one model pair.
func (r *CIResolver) RelationBetween(ctx context.Context, parentTypeID, childTypeID model.ID) (cibiz.TypeRelationRule, bool, error) {
	return r.uc.RelationBetween(ctx, parentTypeID, childTypeID)
}

// HierarchyLevels answers the instance side's question about the model graph.
func (r *CIResolver) HierarchyLevels(ctx context.Context, rootTypeID model.ID, maxLevel int, reverse bool) (cibiz.HierarchyLevels, error) {
	return r.uc.HierarchyLevels(ctx, rootTypeID, maxLevel, reverse)
}
