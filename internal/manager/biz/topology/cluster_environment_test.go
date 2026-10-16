package topology_test

import (
	"context"
	"errors"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"testing"
)

func TestClusterEnvironmentReadsCurrentNodeProperties(t *testing.T) {
	uc := newUC(t)
	ctx := context.Background()
	node, err := uc.CreateNode(ctx, "cluster", "prod", `{"environment":"production"}`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := uc.ClusterEnvironment(ctx, node.ID)
	if err != nil || got != "production" {
		t.Fatalf("environment = %q, %v", got, err)
	}
	if err := uc.SetClusterEnvironment(ctx, node.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, err = uc.ClusterEnvironment(ctx, node.ID)
	if err != nil || got != "" {
		t.Fatalf("cleared environment = %q, %v", got, err)
	}
	if _, err := uc.ClusterEnvironment(ctx, node.ID+100); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing node = %v", err)
	}
}
