package aiops

import (
	"context"
	"errors"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type fakeMutatingProposalRepo struct {
	items     []*model.MutatingProposal
	total     int64
	lastList  biz.MutatingProposalFilter
	lastCount biz.MutatingProposalFilter
	listErr   error
	countErr  error
}

func (f *fakeMutatingProposalRepo) ListMutatingProposals(_ context.Context, filter biz.MutatingProposalFilter) ([]*model.MutatingProposal, error) {
	f.lastList = filter
	return f.items, f.listErr
}

func (f *fakeMutatingProposalRepo) CountMutatingProposals(_ context.Context, filter biz.MutatingProposalFilter) (int64, error) {
	f.lastCount = filter
	return f.total, f.countErr
}

func TestServiceListMutatingProposalsRequiresAdmin(t *testing.T) {
	s := NewWithKernel(nil, nil, KernelLegacy, nil, nil, nil)
	s.SetMutatingProposalRepo(&fakeMutatingProposalRepo{})

	_, _, err := s.ListMutatingProposals(context.Background(), Caller{UserID: 2, Role: "user"}, biz.MutatingProposalFilter{})
	if !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestServiceListMutatingProposalsNormalizesFilter(t *testing.T) {
	repo := &fakeMutatingProposalRepo{
		items: []*model.MutatingProposal{{ID: "proposal-1", ToolName: "execute_k8s_action"}},
		total: 1,
	}
	s := NewWithKernel(nil, nil, KernelLegacy, nil, nil, nil)
	s.SetMutatingProposalRepo(repo)

	items, total, err := s.ListMutatingProposals(context.Background(), Caller{UserID: 1, Role: "admin"}, biz.MutatingProposalFilter{
		ToolName: " execute_k8s_action ",
		Decision: model.DecisionApprove,
		Limit:    999,
		Offset:   -3,
	})
	if err != nil {
		t.Fatalf("ListMutatingProposals: %v", err)
	}
	if len(items) != 1 || total != 1 {
		t.Fatalf("items=%d total=%d", len(items), total)
	}
	if repo.lastList.ToolName != "execute_k8s_action" || repo.lastList.Limit != 50 || repo.lastList.Offset != 0 {
		t.Fatalf("lastList = %+v", repo.lastList)
	}
	if repo.lastCount.Limit != 0 || repo.lastCount.Offset != 0 {
		t.Fatalf("lastCount = %+v", repo.lastCount)
	}
}
