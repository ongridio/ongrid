package device

import (
	"context"
	"log/slog"
	"time"
)

// NetworkPollScheduler scans for due, explicitly configured SNMP targets.
// Polling is deliberately bounded and sequential because every request is a
// reverse-tunnel RPC through an Edge rather than a local database operation.
type NetworkPollScheduler struct {
	uc       *NetworkDiscoveryUsecase
	log      *slog.Logger
	interval time.Duration
}

func NewNetworkPollScheduler(uc *NetworkDiscoveryUsecase, log *slog.Logger) *NetworkPollScheduler {
	if log == nil {
		log = slog.Default()
	}
	return &NetworkPollScheduler{uc: uc, log: log.With(slog.String("comp", "network-poll-scheduler")), interval: 30 * time.Second}
}

func (s *NetworkPollScheduler) Start(ctx context.Context) {
	if s == nil || s.uc == nil {
		return
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("network poll scheduler panic", slog.Any("panic", recovered))
			}
		}()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		s.log.Info("network poll scheduler started", slog.Duration("interval", s.interval))
		var topologyCursor uint64
		for {
			select {
			case <-ctx.Done():
				s.log.Info("network poll scheduler stopped")
				return
			case now := <-ticker.C:
				next, err := s.uc.ReconcileCollectionTopology(ctx, topologyCursor)
				topologyCursor = next
				if err != nil {
					s.log.Warn("network topology reconciliation failed", slog.Any("err", err))
				}
				if _, err := s.uc.PollDueNetworkDevices(ctx, now.UTC(), 10); err != nil {
					s.log.Warn("network poll tick failed", slog.Any("err", err))
				}
			}
		}
	}()
}
