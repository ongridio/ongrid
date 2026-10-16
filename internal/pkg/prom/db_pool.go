package prom

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// RunDBPoolSampler exports database/sql pool statistics every ten seconds.
func RunDBPoolSampler(ctx context.Context, db *sql.DB, errDB error, log *slog.Logger) error {
	if errDB != nil {
		log.Warn("db pool sampler: gorm.DB() failed; pool gauges will stay at zero", slog.Any("err", errDB))
		return nil
	}
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	var lastWait int64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s := db.Stats()
			DBPoolOpenConns.Set(float64(s.OpenConnections))
			DBPoolInUse.Set(float64(s.InUse))
			DBPoolIdle.Set(float64(s.Idle))
			if s.WaitCount > lastWait {
				DBPoolWaitCountTotal.Add(float64(s.WaitCount - lastWait))
				lastWait = s.WaitCount
			}
		}
	}
}
