package setting

import (
	"context"
	"errors"
	"testing"

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
