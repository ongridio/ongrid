package apm

import (
	"context"
	"errors"
	"testing"

	knowledge "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type bindingMemory map[string]string

func (m bindingMemory) Get(_ context.Context, c, k string) (string, bool, error) {
	v, ok := m[c+"/"+k]
	return v, ok, nil
}
func (m bindingMemory) Set(_ context.Context, c, k, v string, _ bool) error {
	m[c+"/"+k] = v
	return nil
}
func (m bindingMemory) Delete(_ context.Context, c, k string) error { delete(m, c+"/"+k); return nil }

type bindingRepos map[uint64]*knowledge.Repository

func (m bindingRepos) GetRepo(_ context.Context, id uint64) (*knowledge.Repository, error) {
	if r := m[id]; r != nil {
		return r, nil
	}
	return nil, errs.ErrNotFound
}

func TestRepositoryBindingPersistenceIsolationAndValidation(t *testing.T) {
	ctx := context.Background()
	settings, repos := bindingMemory{}, bindingRepos{1: {ID: 1, URL: "ssh://git@example/demo.git", Branch: "v1.0.0"}}
	svc := New(nil, nil, nil).WithRepositoryBindings(settings, repos)
	id := Identity{ServiceName: "orders", ServiceNamespace: "trade", Environment: "prod"}
	b, err := svc.PutRepositoryBinding(ctx, RepositoryBinding{Identity: id, RepoID: 1, SourceDirectory: "services/orders/", TagPattern: "v{version}"})
	if err != nil || b.SourceDirectory != "services/orders" || b.RepoURL != repos[1].URL {
		t.Fatalf("save: %+v %v", b, err)
	}
	// Reconstruct the service: the binding must survive process-local state.
	svc = New(nil, nil, nil).WithRepositoryBindings(settings, repos)
	if b, err = svc.GetRepositoryBinding(ctx, id); err != nil || b == nil || b.TagPattern != "v{version}" {
		t.Fatalf("reload: %+v %v", b, err)
	}
	for _, other := range []Identity{{"orders", "trade", "dev"}, {"orders", "other", "prod"}, {"other", "trade", "prod"}} {
		if value, err := svc.GetRepositoryBinding(ctx, other); err != nil || value != nil {
			t.Fatalf("cross-service binding: %+v %v", value, err)
		}
	}
	for _, bad := range []RepositoryBinding{
		{Identity: id, RepoID: 1, SourceDirectory: "../other"},
		{Identity: id, RepoID: 1, SourceDirectory: "/etc"},
		{Identity: id, RepoID: 1, SourceDirectory: ":(glob)**"},
		{Identity: id, RepoID: 1, SourceDirectory: ".git/objects"},
		{Identity: id, RepoID: 1, TagPattern: "main"},
		{Identity: id, RepoID: 1, TagPattern: "{version}{version}"},
		{Identity: id, RepoID: 1, TagPattern: "../{version}"},
		{Identity: id, RepoID: 1, TagPattern: "v{version}.lock"},
		{Identity: Identity{}, RepoID: 1},
	} {
		if _, err := svc.PutRepositoryBinding(ctx, bad); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	if _, err := svc.PutRepositoryBinding(ctx, RepositoryBinding{Identity: id, RepoID: 2}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
	delete(repos, 1)
	if b, err = svc.GetRepositoryBinding(ctx, id); err != nil || !b.RepoMissing || b.RepoURL != "" {
		t.Fatalf("deleted repo: %+v %v", b, err)
	}
	if err := svc.DeleteRepositoryBinding(ctx, id); err != nil {
		t.Fatal(err)
	}
	if b, err := svc.GetRepositoryBinding(ctx, id); err != nil || b != nil {
		t.Fatalf("unbind: %+v %v", b, err)
	}
}
