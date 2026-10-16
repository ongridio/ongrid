package alert

import (
	model "github.com/ongridio/ongrid/internal/manager/model/alert"
	"testing"
)

type recordingInvestigator func(*model.Incident)

func (f recordingInvestigator) InvestigateAsync(in *model.Incident) { f(in) }

func TestChainInvestigatorsPreservesDispatchOrderAndIdentity(t *testing.T) {
	if ChainInvestigators(nil, nil) != nil {
		t.Fatal("empty chain should be nil")
	}
	one := &fakeInvestigator{}
	if ChainInvestigators(nil, one) != one {
		t.Fatal("single investigator should be returned unchanged")
	}
	incident := &model.Incident{ID: 42}
	var order []int
	recorder := func(n int) Investigator {
		return recordingInvestigator(func(got *model.Incident) {
			if got != incident {
				t.Fatal("incident identity changed")
			}
			order = append(order, n)
		})
	}
	ChainInvestigators(recorder(1), nil, recorder(2)).InvestigateAsync(incident)
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("dispatch order: %v", order)
	}
}
