package collector

import (
	"context"
	"log/slog"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// CompositeCollector combines an embedded baseline collector with scrape
// targets. Host-role scrape targets replace the embedded host fast path;
// component-role targets only add rich-path Prometheus samples.
type CompositeCollector struct {
	embedded Collector
	scraper  *Scraper
	log      *slog.Logger
}

// NewComposite constructs the auto-mode collector.
func NewComposite(embedded Collector, scraper *Scraper, log *slog.Logger) *CompositeCollector {
	if log == nil {
		log = slog.Default()
	}
	return &CompositeCollector{embedded: embedded, scraper: scraper, log: log}
}

// CollectAll returns scrape outputs plus either host-role scrape fast-path
// outputs or, when no host scrape is currently successful, the embedded
// baseline output.
func (c *CompositeCollector) CollectAll(ctx context.Context) ([]CollectorOutput, error) {
	var out []CollectorOutput
	hostFromScrape := false

	if c.scraper != nil {
		scrapeOut, err := c.scraper.CollectAll(ctx)
		if err != nil {
			c.log.Warn("collector: scrape collect failed", slog.Any("err", err))
		}
		for _, o := range scrapeOut {
			if o.HostPointValid {
				hostFromScrape = true
			}
			out = append(out, o)
		}
	}

	if !hostFromScrape && c.embedded != nil {
		embeddedOut, err := c.embedded.CollectAll(ctx)
		if err != nil {
			return out, err
		}
		out = append(out, embeddedOut...)
	}
	return out, nil
}

func (c *CompositeCollector) HostInfo(ctx context.Context) (tunnel.HostInfo, error) {
	if c.embedded != nil {
		return c.embedded.HostInfo(ctx)
	}
	if c.scraper != nil {
		return c.scraper.HostInfo(ctx)
	}
	return tunnel.HostInfo{}, nil
}

func (c *CompositeCollector) GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error) {
	if c.scraper != nil {
		resp, err := c.scraper.GetHostLoad(ctx)
		if err != nil {
			return resp, err
		}
		if resp.CPUSampleStatus == "valid" || resp.CPUPct != 0 || resp.MemPct != 0 || resp.Load1 != 0 {
			return resp, nil
		}
	}
	if c.embedded != nil {
		return c.embedded.GetHostLoad(ctx)
	}
	return tunnel.GetHostLoadResponse{}, nil
}

func (c *CompositeCollector) GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error) {
	if c.embedded != nil {
		return c.embedded.GetProcessList(ctx, topN, sortBy)
	}
	if c.scraper != nil {
		return c.scraper.GetProcessList(ctx, topN, sortBy)
	}
	return tunnel.GetProcessListResponse{}, nil
}
