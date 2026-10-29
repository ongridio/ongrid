package basetool

import (
	"context"
	"time"
)

type observationTimeKey struct{}

// WithObservationTime binds an exact query end time for external result metadata.
func WithObservationTime(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, observationTimeKey{}, at)
}
func ObservationTime(ctx context.Context) time.Time {
	if at, ok := ctx.Value(observationTimeKey{}).(time.Time); ok {
		return at
	}
	return time.Now()
}
