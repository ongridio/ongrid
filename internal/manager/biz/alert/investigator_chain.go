package alert

import (
	managermodelalert "github.com/ongridio/ongrid/internal/manager/model/alert"
)

// ChainInvestigators fans an incident out to multiple alert.Investigator
// implementations (legacy ai_initial_diagnosis + new structured RCA).
// nil entries are skipped; an all-nil input returns nil so the caller
// can decide whether to even call SetInvestigator.
func ChainInvestigators(invs ...Investigator) Investigator {
	live := make([]Investigator, 0, len(invs))
	for _, i := range invs {
		if i != nil {
			live = append(live, i)
		}
	}
	if len(live) == 0 {
		return nil
	}
	if len(live) == 1 {
		return live[0]
	}
	return investigatorChain(live)
}

type investigatorChain []Investigator

func (c investigatorChain) InvestigateAsync(in *managermodelalert.Incident) {
	for _, i := range c {
		i.InvestigateAsync(in)
	}
}
