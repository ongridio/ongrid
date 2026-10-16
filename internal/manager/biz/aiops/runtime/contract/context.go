package contract

import "context"

// Version identifies execution policy, independently of the product release.
type Version string

const (
	V1 Version = "v1"
	V2 Version = "v2"
	V3 Version = "v3"
)

type emitKey struct{}
type versionKey struct{}

func WithEmit(ctx context.Context, emit Emit) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, emitKey{}, emit)
}

func EmitFromContext(ctx context.Context) Emit {
	emit, _ := ctx.Value(emitKey{}).(Emit)
	return emit
}

// WithVersion pins nested execution to its originating runtime.
func WithVersion(ctx context.Context, version Version) context.Context {
	return context.WithValue(ctx, versionKey{}, version)
}

func VersionFromContext(ctx context.Context) Version {
	version, _ := ctx.Value(versionKey{}).(Version)
	return version
}
