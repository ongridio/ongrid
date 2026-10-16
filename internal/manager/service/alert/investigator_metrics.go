package alert

import (
	"context"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	"github.com/ongridio/ongrid/internal/pkg/prom"
)

// RunInvestigatorMetrics samples concurrency usage until shutdown.
func RunInvestigatorMetrics(ctx context.Context, investigator *investigator.Usecase) error {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			prom.InvestigatorInflight.Set(float64(investigator.InflightCount()))
		}
	}
}
