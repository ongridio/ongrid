package basetool

import "context"

// SkillAssetRef identifies a runtime-owned immutable package snapshot. It never
// grants command execution: cloud_bash still requires the normal approval.
type SkillAssetRef struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Version          string `json:"version,omitempty"`
	Revision         string `json:"revision"`
	Directory        string `json:"directory"`
	PackageDirectory string `json:"package_directory,omitempty"`
}

type skillAssetsKey struct{}

func WithSkillAssets(ctx context.Context, refs []SkillAssetRef) context.Context {
	return context.WithValue(ctx, skillAssetsKey{}, append([]SkillAssetRef(nil), refs...))
}
func SkillAssetsFromContext(ctx context.Context) []SkillAssetRef {
	refs, _ := ctx.Value(skillAssetsKey{}).([]SkillAssetRef)
	return append([]SkillAssetRef(nil), refs...)
}
