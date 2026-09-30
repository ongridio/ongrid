package collector

import (
	"context"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// CollectorSource is a typed alias for the Source string carried with each
// push_prom_samples request. Embedded collectors emit "embedded"; scrape
// collectors emit "scrape:<target_name>".
type CollectorSource = string

const (
	// SourceEmbedded marks samples produced by in-process gopsutil reads.
	SourceEmbedded CollectorSource = "embedded"
	// SourceScrapePrefix is prepended to the target name for scraper output.
	SourceScrapePrefix = "scrape:"
)

// CollectorOutput is one logical collection result for one source.
//
// Both fields are produced from the same MetricFamily snapshot:
//   - HostPoint is the 8-field fast-path used by the legacy
//     push_host_metrics wire method. Best-effort: zero values are
//     allowed for fields the source did not expose, and the rate-based
//     fields (CPUPct, NetRxBps, NetTxBps) return 0 on the very first
//     call because no prior sample is available.
//   - Samples is the flat open-set rich path consumed by the new
//     push_prom_samples wire method.
type CollectorOutput struct {
	// SnapshotID is nonzero only for cached scrapes. A new acquisition gets
	// a new ID even when every sample value is unchanged.
	SnapshotID     uint64
	Source         CollectorSource
	HostPoint      tunnel.HostMetricPoint
	HostPointValid bool
	Samples        []tunnel.PromSample
}

// Collector is the contract the edge agent requires of a metric source.
//
// Multi-target sources (scrape) return one or more CollectorOutput from
// CollectAll; single-source collectors (embedded) return a one-element
// slice. The agent loop iterates the slice and pushes each to cloud.
type Collector interface {
	// CollectAll returns one CollectorOutput per source on this tick.
	// An empty slice is valid (e.g. scrape with no successful targets
	// yet); the agent simply skips the push.
	CollectAll(ctx context.Context) ([]CollectorOutput, error)

	// HostInfo returns the static host description sent on register_edge.
	HostInfo(ctx context.Context) (tunnel.HostInfo, error)

	// GetHostLoad serves the cloud->edge get_host_load RPC.
	GetHostLoad(ctx context.Context) (tunnel.GetHostLoadResponse, error)

	// GetProcessList serves the cloud->edge get_process_list RPC.
	GetProcessList(ctx context.Context, topN int, sortBy string) (tunnel.GetProcessListResponse, error)
}
