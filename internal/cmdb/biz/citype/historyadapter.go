package citype

import (
	"context"
	"fmt"

	historybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// HistoryTypeNames and HistoryAttributes adapt the model configuration to the
// operation-history page's consumer-declared ports.
//
// THE ATTRIBUTE SET IS THE EFFECTIVE ONE — inherited attributes included —
// because a change to an inherited attribute is still a change a person reads,
// and a history page that could not name it would show a blank column.

// HistoryTypeNames names a model.
type HistoryTypeNames struct{ uc *Usecase }

// NewHistoryTypeNames builds the adapter.
func NewHistoryTypeNames(uc *Usecase) *HistoryTypeNames { return &HistoryTypeNames{uc: uc} }

var _ historybiz.TypeNameResolver = (*HistoryTypeNames)(nil)

// TypeName returns the model's name and alias.
func (r *HistoryTypeNames) TypeName(ctx context.Context, typeID model.ID) (string, string, error) {
	view, err := r.uc.GetByRef(ctx, fmt.Sprint(typeID))
	if err != nil {
		return "", "", err
	}
	if view == nil {
		return "", "", nil
	}
	return view.Type.Name, view.Type.Alias, nil
}

// HistoryAttributes lists a model's effective attributes.
type HistoryAttributes struct{ uc *Usecase }

// NewHistoryAttributes builds the adapter.
func NewHistoryAttributes(uc *Usecase) *HistoryAttributes { return &HistoryAttributes{uc: uc} }

var _ historybiz.AttributeResolver = (*HistoryAttributes)(nil)

// ResolveAttributes returns the model's attributes in binding order.
func (r *HistoryAttributes) ResolveAttributes(ctx context.Context, typeID model.ID) ([]historybiz.AttributeRef, error) {
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
	out := make([]historybiz.AttributeRef, 0, len(effective.Attributes))
	for _, item := range effective.Attributes {
		out = append(out, historybiz.AttributeRef{
			ID: item.ID, Name: item.Name, Alias: item.Alias, ValueType: item.ValueType,
			Sensitive: item.IsPassword || item.IsLink || item.IsFile,
		})
	}
	return out, nil
}
