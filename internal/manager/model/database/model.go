// Package database holds persistence entities for database instance management.
//
// Phase 1 (Jun 2026): database_instances is the core asset table, tracking
// every database that the platform manages — MySQL, PostgreSQL, Redis,
// MongoDB, Oracle, SelectDB (Apache Doris). Each row belongs to an edge
// (the ongrid-edge agent that proxies connections and runs the exporter).
// Monitoring metrics are stored externally in Prometheus and rendered via
// PromQL on the instance detail page.
package database

import (
	"time"

	"gorm.io/gorm"
)

// DatabaseInstance is a managed database endpoint. The edge agent proxies
// read-only SQL queries through its tunnel and runs the Prometheus exporter
// subprocess for metrics collection.
type DatabaseInstance struct {
	ID      uint64 `gorm:"primaryKey;autoIncrement"`
	EdgeID  uint64 `gorm:"not null;index;column:edge_id"`
	// Name is the operator-friendly display label. Unique per edge so
	// operators can name their instances without collision.
	Name    string `gorm:"size:128;not null;uniqueIndex:uk_edge_name,priority:1;column:name"`
	// DBType is one of: mysql, postgresql, redis, mongodb, oracle, selectdb.
	DBType  string `gorm:"size:32;not null;column:db_type;check:db_type IN ('mysql','postgresql','redis','mongodb','oracle','selectdb')"`
	// Host is the database server hostname or IP reachable from the edge.
	Host    string `gorm:"size:255;not null;column:host"`
	Port    int    `gorm:"not null;default:0;column:port"`
	// Version is auto-detected on first connect (e.g. "8.0.32", "15.4").
	Version string `gorm:"size:64;not null;default:'';column:version"`
	// Status is the current connectivity state.
	Status  string `gorm:"size:16;default:unknown;check:status IN ('online','offline','unknown');column:status"`
	// ConfigJSON holds database configuration parameters discovered or
	// specified by the operator (max_connections, autovacuum, etc.).
	ConfigJSON string `gorm:"type:text;column:config_json"`
	// Labels are user-defined key=value tags for filtering and grouping.
	Labels string `gorm:"type:text;column:labels"`
	// Description is an optional operator note about this instance.
	Description string `gorm:"size:512;not null;default:'';column:description"`

	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at"` // soft delete
}

// TableName pins the table name for gorm.
func (DatabaseInstance) TableName() string { return "database_instances" }

// Status constants.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// DBType constants.
const (
	DBTypeMySQL      = "mysql"
	DBTypePostgreSQL = "postgresql"
	DBTypeRedis      = "redis"
	DBTypeMongoDB    = "mongodb"
	DBTypeOracle     = "oracle"
	DBTypeSelectDB   = "selectdb"
)

// AllDBTypes returns every supported database type.
func AllDBTypes() []string {
	return []string{
		DBTypeMySQL,
		DBTypePostgreSQL,
		DBTypeRedis,
		DBTypeMongoDB,
		DBTypeOracle,
		DBTypeSelectDB,
	}
}
