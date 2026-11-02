package imbridge

import (
	"context"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"testing"
)

type approvalRepoFake struct{ row *model.Approval }

func (r approvalRepoFake) Get(context.Context, string) (*model.Approval, error) { return r.row, nil }
func TestApprovalStatusRequiresSessionAndOwner(t *testing.T) {
	for _, tc := range []struct {
		name, session string
		owner         uint64
		want          bool
	}{
		{"same session", "session", 7, true}, {"different session", "other", 7, false}, {"empty session", "", 7, false}, {"different owner", "session", 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAiopsAdapter(nil, 7, nil, nil, WithApprovals(approvalRepoFake{&model.Approval{SessionID: "session", ProposedBy: tc.owner, Status: "rejected"}}))
			got, err := a.ApprovalStatus(context.Background(), tc.session, "id")
			if tc.want && (err != nil || got != "rejected") {
				t.Fatalf("%s %v", got, err)
			}
			if !tc.want && err == nil {
				t.Fatal("cross-session or owner read allowed")
			}
		})
	}
}
