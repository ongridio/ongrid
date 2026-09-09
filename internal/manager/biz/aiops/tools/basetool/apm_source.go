package basetool

import (
	"context"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
)

type apmSourceKey struct{}

func WithAPMSource(ctx context.Context, scope *model.APMSourceScope) context.Context {
	return context.WithValue(ctx, apmSourceKey{}, scope)
}
func APMSourceFromContext(ctx context.Context) *model.APMSourceScope {
	scope, _ := ctx.Value(apmSourceKey{}).(*model.APMSourceScope)
	return scope
}
