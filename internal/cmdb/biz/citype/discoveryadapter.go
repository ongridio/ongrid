package citype

import (
	"context"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
)

// DiscoveryResolver adapts this package's model definitions to the discovery
// module's consumer-declared port.
//
// THE DEPENDENCY POINTS THIS WAY ON PURPOSE, exactly as it does for the instance
// side: `biz/discovery` declares what it needs and knows nothing about how a
// model's effective attributes are derived, and this package answers. The two
// things a discovery target needs from a model — its id, and the NAME of the
// attribute that identifies an instance — are both readings of the model's
// inheritance and its bindings, which is this package's subject and not the
// discovery module's.
type DiscoveryResolver struct {
	uc *Usecase
}

// NewDiscoveryResolver builds the adapter.
func NewDiscoveryResolver(uc *Usecase) *DiscoveryResolver {
	return &DiscoveryResolver{uc: uc}
}

var _ discoverybiz.ModelResolver = (*DiscoveryResolver)(nil)

// ResolveCIType resolves a model by numeric id or exact name, returning what a
// target needs, or (nil, nil) when nothing matches.
func (r *DiscoveryResolver) ResolveCIType(ctx context.Context, ref string) (*discoverybiz.CITypeRef, error) {
	view, err := r.uc.GetByRef(ctx, ref)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, nil
	}
	icon := ""
	if view.Type.Icon != nil {
		icon = *view.Type.Icon
	}
	return &discoverybiz.CITypeRef{
		ID:    view.Type.ID,
		Name:  view.Type.Name,
		Alias: view.Type.Alias,
		Icon:  icon,

		// THE IDENTIFYING ATTRIBUTE'S NAME, resolved through the model's whole
		// inheritance chain — so a target bound to a child model can map the
		// attribute its parent declared, which is the case that makes sharing the
		// derivation rule worth a port.
		UniqueKey: view.UniqueKey,
	}, nil
}
