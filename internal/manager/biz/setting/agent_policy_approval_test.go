package setting

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/setting"
)

type policyApprovalErrorRepo struct{ Repo }

func (policyApprovalErrorRepo) Get(context.Context, string, string) (*model.Setting, error) {
	return nil, errors.New("store unavailable")
}

func TestAgentCommandPolicyApprovalFailsClosed(t *testing.T) {
	for _, value := range []string{"", "true", "false", "TRUE", "1"} {
		repo := newFakeRepo()
		if value != "" {
			if _, err := repo.Set(t.Context(), model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, value, false); err != nil {
				t.Fatal(err)
			}
		}
		if got := New(repo, nil).AgentCommandPolicyApprovalEnabled(t.Context()); got != (value == "true") {
			t.Fatalf("value=%q enabled=%v", value, got)
		}
	}
	if New(policyApprovalErrorRepo{}, nil).AgentCommandPolicyApprovalEnabled(t.Context()) {
		t.Fatal("store failure enabled policy approval")
	}
}

func TestAgentCommandPolicyApprovalValidationAndUpdates(t *testing.T) {
	svc := New(newFakeRepo(), nil)
	for _, value := range []string{"true", "false"} {
		if err := svc.Set(t.Context(), model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, value, false); err != nil {
			t.Fatal(err)
		}
		if got := svc.AgentCommandPolicyApprovalEnabled(t.Context()); got != (value == "true") {
			t.Fatalf("stale setting after %s", value)
		}
	}
	for _, value := range []string{"", "TRUE", "1"} {
		if err := svc.Set(t.Context(), model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, value, false); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

type delayedPolicyApprovalRepo struct {
	Repo
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *delayedPolicyApprovalRepo) Get(ctx context.Context, category, key string) (*model.Setting, error) {
	row, err := r.Repo.Get(ctx, category, key)
	r.once.Do(func() {
		close(r.read)
		select {
		case <-r.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return row, err
}

func TestAgentCommandPolicyApprovalDisableDuringRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	repo := &delayedPolicyApprovalRepo{Repo: newFakeRepo(), read: make(chan struct{}), release: make(chan struct{})}
	svc := New(repo, nil)
	if err := svc.Set(ctx, model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, "true", false); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- svc.AgentCommandPolicyApprovalEnabled(ctx) }()
	select {
	case <-repo.read:
	case <-ctx.Done():
		t.Fatal("setting read did not start")
	}
	if err := svc.Set(ctx, model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, "false", false); err != nil {
		t.Fatal(err)
	}
	// Finish the old read after the update has invalidated the generic cache.
	close(repo.release)
	<-done
	if svc.AgentCommandPolicyApprovalEnabled(ctx) {
		t.Fatal("a completed disable must apply to new calls even after an older read returns")
	}
}

func TestAgentCommandPolicyApprovalUpdatesAcrossManagers(t *testing.T) {
	repo := newFakeRepo()
	writer, reader := New(repo, nil), New(repo, nil)
	for _, value := range []string{"true", "false", "true", "false"} {
		if err := writer.Set(t.Context(), model.CategoryAgent, model.KeyAgentCommandPolicyApprovalEnabled, value, false); err != nil {
			t.Fatal(err)
		}
		if got := reader.AgentCommandPolicyApprovalEnabled(t.Context()); got != (value == "true") {
			t.Fatalf("other manager retained stale permission after %s: enabled=%v", value, got)
		}
	}
}
