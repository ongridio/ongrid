package store

import (
	"context"
	"testing"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The history page's instance labels: the model's show attribute, read in one
// query per model, with a missing instance simply absent.
func TestHistoryCINamesUsesTheShowAttribute(t *testing.T) {
	repo, _ := newSearchRepo(t)
	uc := cibiz.NewUsecase(repo, fixtureResolver{}, nil)

	names, err := cibiz.NewHistoryCINames(uc).ResolveCINames(
		context.Background(), []model.ID{1, 18, 9999},
	)
	if err != nil {
		t.Fatalf("ResolveCINames: %v", err)
	}
	if names[1] != "web-01" {
		t.Fatalf("instance 1 = %q, want web-01", names[1])
	}
	if names[18] != "web-99" {
		t.Fatalf("instance 18 = %q, want web-99", names[18])
	}
	if _, ok := names[9999]; ok {
		t.Fatal("a missing instance was given a name")
	}
}
