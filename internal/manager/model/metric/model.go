// Package metric holds persistence entities for the manager/metric sub-domain.
// Post-pivot there is no org_id — rows are keyed by (edge_id, ts).
//
// Three tiers: raw samples (10s cadence), 5m buckets, 1h buckets. The
// Point / Bucket5m / Bucket1h types below are the domain-facing values
// used by biz.Writer / biz.Reader and are NOT gorm-tagged so the
// interface stays storage-agnostic. The HostMetric* struct family is the
// gorm-tagged row shape (one per physical table) and lives alongside
// for the data layer's use.
package metric

import (
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// Point is the domain value type for a single raw host-metric sample.
// Distinct from HostMetric (the gorm row) so biz.Writer / biz.Reader do
// not leak storage concerns into the ingest path.
type Point struct {
	EdgeID      uint64
	Ts          time.Time
	CPUPct      float64
	MemPct      float64
	Load1       float64
	Load5       float64
	Load15      float64
	NetRxBps    uint64
	NetTxBps    uint64
	DiskUsedPct float64
}

// FromTunnelPoint converts an on-wire tunnel point to a domain Point.
// The tunnel timestamp is unix seconds UTC; the resulting
// time.Time is explicitly UTC.
func FromTunnelPoint(edgeID uint64, p tunnel.HostMetricPoint) Point {
	return Point{
		EdgeID:      edgeID,
		Ts:          time.Unix(p.Ts, 0).UTC(),
		CPUPct:      p.CPUPct,
		MemPct:      p.MemPct,
		Load1:       p.Load1,
		Load5:       p.Load5,
		Load15:      p.Load15,
		NetRxBps:    p.NetRxBps,
		NetTxBps:    p.NetTxBps,
		DiskUsedPct: p.DiskUsedPct,
	}
}

// Bucket5m is a 5-minute aggregate row (domain value).
//
// Gauge fields carry avg + max; counter fields (net rx/tx) carry a sum
// over the bucket. Ts is the bucket-START timestamp aligned to 5-minute
// boundaries (floor).
type Bucket5m struct {
	EdgeID      uint64
	Ts          time.Time
	CPUAvg      float64
	CPUMax      float64
	MemAvg      float64
	MemMax      float64
	Load1Avg    float64
	Load1Max    float64
	Load5Avg    float64
	Load5Max    float64
	Load15Avg   float64
	Load15Max   float64
	NetRxSum    uint64
	NetTxSum    uint64
	DiskUsedAvg float64
	DiskUsedMax float64
}

// Bucket1h is a 1-hour aggregate row, same shape as Bucket5m with an
// hour-aligned Ts. Kept as a distinct named type (rather than an alias)
// so the two are not accidentally interchangeable at call sites.
type Bucket1h struct {
	EdgeID      uint64
	Ts          time.Time
	CPUAvg      float64
	CPUMax      float64
	MemAvg      float64
	MemMax      float64
	Load1Avg    float64
	Load1Max    float64
	Load5Avg    float64
	Load5Max    float64
	Load15Avg   float64
	Load15Max   float64
	NetRxSum    uint64
	NetTxSum    uint64
	DiskUsedAvg float64
	DiskUsedMax float64
}

// ---------------------------------------------------------------------
// GORM row types. One per physical table.
// ---------------------------------------------------------------------

// HostMetric is the host_metrics_raw row, managed by GORM AutoMigrate.
type HostMetric struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;column:id"`
	EdgeID      uint64    `gorm:"index:idx_host_metrics_raw_edge_ts,priority:1;column:edge_id;not null"`
	Ts          time.Time `gorm:"index:idx_host_metrics_raw_edge_ts,priority:2;column:ts;not null"`
	CPUPct      float64   `gorm:"column:cpu_pct;not null"`
	MemPct      float64   `gorm:"column:mem_pct;not null"`
	Load1       float64   `gorm:"column:load1;not null"`
	Load5       float64   `gorm:"column:load5;not null"`
	Load15      float64   `gorm:"column:load15;not null"`
	NetRxBps    uint64    `gorm:"column:net_rx_bps;not null"`
	NetTxBps    uint64    `gorm:"column:net_tx_bps;not null"`
	DiskUsedPct float64   `gorm:"column:disk_used_pct"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName pins the SQLite table (raw samples).
func (HostMetric) TableName() string { return "host_metrics_raw" }

// HostMetric5m is the host_metrics_5m row.
type HostMetric5m struct {
	EdgeID      uint64    `gorm:"primaryKey;priority:1;column:edge_id"`
	Ts          time.Time `gorm:"primaryKey;priority:2;column:ts"`
	CPUAvg      float64   `gorm:"column:cpu_avg"`
	CPUMax      float64   `gorm:"column:cpu_max"`
	MemAvg      float64   `gorm:"column:mem_avg"`
	MemMax      float64   `gorm:"column:mem_max"`
	Load1Avg    float64   `gorm:"column:load1_avg"`
	Load1Max    float64   `gorm:"column:load1_max"`
	Load5Avg    float64   `gorm:"column:load5_avg"`
	Load5Max    float64   `gorm:"column:load5_max"`
	Load15Avg   float64   `gorm:"column:load15_avg"`
	Load15Max   float64   `gorm:"column:load15_max"`
	NetRxSum    uint64    `gorm:"column:net_rx_sum"`
	NetTxSum    uint64    `gorm:"column:net_tx_sum"`
	DiskUsedAvg float64   `gorm:"column:disk_used_avg"`
	DiskUsedMax float64   `gorm:"column:disk_used_max"`
}

// TableName pins the SQLite table (5-minute aggregates).
func (HostMetric5m) TableName() string { return "host_metrics_5m" }

// HostMetric1h is the host_metrics_1h row (same shape as 5m, hour-aligned ts).
type HostMetric1h struct {
	EdgeID      uint64    `gorm:"primaryKey;priority:1;column:edge_id"`
	Ts          time.Time `gorm:"primaryKey;priority:2;column:ts"`
	CPUAvg      float64   `gorm:"column:cpu_avg"`
	CPUMax      float64   `gorm:"column:cpu_max"`
	MemAvg      float64   `gorm:"column:mem_avg"`
	MemMax      float64   `gorm:"column:mem_max"`
	Load1Avg    float64   `gorm:"column:load1_avg"`
	Load1Max    float64   `gorm:"column:load1_max"`
	Load5Avg    float64   `gorm:"column:load5_avg"`
	Load5Max    float64   `gorm:"column:load5_max"`
	Load15Avg   float64   `gorm:"column:load15_avg"`
	Load15Max   float64   `gorm:"column:load15_max"`
	NetRxSum    uint64    `gorm:"column:net_rx_sum"`
	NetTxSum    uint64    `gorm:"column:net_tx_sum"`
	DiskUsedAvg float64   `gorm:"column:disk_used_avg"`
	DiskUsedMax float64   `gorm:"column:disk_used_max"`
}

// TableName pins the SQLite table (1-hour aggregates).
func (HostMetric1h) TableName() string { return "host_metrics_1h" }

// DeadLetter is the host_metrics_dead_letter row. Populated when an
// ingest flush exhausts its retry budget; retained 7d.
type DeadLetter struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;column:id"`
	EdgeID      uint64    `gorm:"column:edge_id;not null"`
	Ts          time.Time `gorm:"column:ts;not null"`
	CPUPct      float64   `gorm:"column:cpu_pct;not null"`
	MemPct      float64   `gorm:"column:mem_pct;not null"`
	Load1       float64   `gorm:"column:load1;not null"`
	Load5       float64   `gorm:"column:load5;not null"`
	Load15      float64   `gorm:"column:load15;not null"`
	NetRxBps    uint64    `gorm:"column:net_rx_bps;not null"`
	NetTxBps    uint64    `gorm:"column:net_tx_bps;not null"`
	DiskUsedPct float64   `gorm:"column:disk_used_pct"`
	ErrorReason string    `gorm:"column:error_reason;not null;size:256"`
	FailedAt    time.Time `gorm:"column:failed_at;autoCreateTime"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName pins the SQLite table (dead letter for failed flushes).
func (DeadLetter) TableName() string { return "host_metrics_dead_letter" }
