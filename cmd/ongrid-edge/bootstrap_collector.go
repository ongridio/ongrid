package main

import (
	"context"
	"fmt"
	"log/slog"

	edgebiz "github.com/ongridio/ongrid/internal/edgeagent/biz"
	edgecollector "github.com/ongridio/ongrid/internal/edgeagent/collector"
	"github.com/ongridio/ongrid/internal/pkg/config"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
	"golang.org/x/sync/errgroup"
)

// buildCollector constructs the collector matching cfg.Edge.CollectorMode.
// For scrape mode the per-target scrape goroutines are added to eg so
// they share the agent's lifecycle.
//
// Modes:
//
//	off / "" — preferred for installs running the hostmetrics +
//	  procmetrics plugins. CollectAll is a no-op so no node_* samples
//	  are pushed via the tunnel; manager-side Prom scrapes the
//	  node_exporter / process-exporter subprocesses directly through
//	  the docker bridge. On-demand RPCs (host_info / get_host_load /
//	  get_host_processes) still work via the embedded gopsutil
//	  snapshot path.
//	auto — legacy: embedded (gopsutil push) + scraper.
//	embedded — embedded push only.
//	scrape — scraper only.
func buildCollector(ctx context.Context, cfg *config.Config, log *slog.Logger, eg *errgroup.Group) (edgebiz.Collector, *edgecollector.Scraper, error) {
	switch cfg.Edge.CollectorMode {
	case "off", "none", "":
		// Default for fresh installs: don't push anything periodically.
		// On-demand RPCs still hit gopsutil via the wrapped embedded
		// collector so AIOps tools and EdgeDetail cards keep working.
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		return collectorAdapter{c: edgecollector.NewNoopPush(em), network: edgecollector.NewNetworkDiscovery()}, nil, nil

	case "auto":
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		sc, err := edgecollector.LoadScrapeConfig(cfg.Edge.ScrapeConfigFile)
		if err != nil {
			log.Warn("scrape config unavailable; using embedded baseline only", slog.Any("err", err))
			return collectorAdapter{c: em, network: edgecollector.NewNetworkDiscovery()}, nil, nil
		}
		scraper := edgecollector.NewScraper(sc, log)
		eg.Go(func() error { return scraper.Run(ctx) })
		return collectorAdapter{c: edgecollector.NewComposite(em, scraper, log), network: edgecollector.NewNetworkDiscovery()}, scraper, nil

	case "scrape":
		sc, err := edgecollector.LoadScrapeConfig(cfg.Edge.ScrapeConfigFile)
		if err != nil {
			return nil, nil, fmt.Errorf("scrape config: %w", err)
		}
		scraper := edgecollector.NewScraper(sc, log)
		eg.Go(func() error { return scraper.Run(ctx) })
		return collectorAdapter{c: scraper, network: edgecollector.NewNetworkDiscovery()}, scraper, nil

	case "embedded":
		em, err := edgecollector.NewEmbedded(log)
		if err != nil {
			return nil, nil, fmt.Errorf("embedded collector: %w", err)
		}
		return collectorAdapter{c: em, network: edgecollector.NewNetworkDiscovery()}, nil, nil
	default:
		return nil, nil, fmt.Errorf("unknown collector mode %q", cfg.Edge.CollectorMode)
	}
}

// collectorAdapter bridges the collector package's Collector interface to
// the biz package's identical-shaped interface. Two interfaces, one
// implementation — the seam exists so biz/agent.go does not import
// internal/edgeagent/collector (avoids cycles when the collector package
// in turn depends on tunnel types).
type collectorAdapter struct {
	c       edgecollector.Collector
	network *edgecollector.NetworkDiscoveryCollector
}

func (a collectorAdapter) CollectAll(ctx context.Context) ([]edgebiz.CollectorOutput, error) {
	outs, err := a.c.CollectAll(ctx)
	if err != nil {
		return nil, err
	}
	bizOuts := make([]edgebiz.CollectorOutput, 0, len(outs))
	for _, o := range outs {
		bizOuts = append(bizOuts, edgebiz.CollectorOutput{
			Source:         o.Source,
			HostPoint:      o.HostPoint,
			HostPointValid: o.HostPointValid,
			SnapshotID:     o.SnapshotID,
			Samples:        o.Samples,
		})
	}
	return bizOuts, nil
}

func (a collectorAdapter) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	return a.c.HostInfo(ctx)
}

func (a collectorAdapter) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	return a.c.GetHostLoad(ctx)
}

func (a collectorAdapter) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	return a.c.GetProcessList(ctx, topN, sortBy)
}

func (a collectorAdapter) CollectNetworkDiscovery(ctx context.Context) (tunnel.NetworkDiscoveryRequest, error) {
	if a.network == nil {
		return tunnel.NetworkDiscoveryRequest{}, nil
	}
	return a.network.CollectNetworkDiscovery(ctx)
}
