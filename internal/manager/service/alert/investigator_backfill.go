package alert

import (
	"context"
	"log/slog"
	"time"

	managerbizalert "github.com/ongridio/ongrid/internal/manager/biz/alert"
	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
)

// BackfillInvestigations compensates for incidents skipped before the investigator was available.
func BackfillInvestigations(investigator *investigator.Usecase, alerts *managerbizalert.Usecase, log *slog.Logger) {
	bfCtx, bfCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer bfCancel()
	n, err := investigator.BackfillUnstartedIncidents(
		bfCtx,
		time.Now().Add(-24*time.Hour),
		100,
		alerts.GetIncident,
	)
	if err != nil {
		log.Warn("alert: unstarted-investigation backfill failed", slog.Any("err", err))
		return
	}
	if n > 0 {
		log.Info("alert: backfilled unstarted investigations on boot", slog.Int("dispatched", n))
	}
}
