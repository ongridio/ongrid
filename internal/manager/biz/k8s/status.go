package k8s

import (
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
)

const ClusterOnlineTTL = 90 * time.Second

func EffectiveClusterStatus(c *model.Cluster, now time.Time) string {
	if c == nil {
		return ""
	}
	status := strings.TrimSpace(c.Status)
	if status != model.ClusterStatusOnline {
		return status
	}
	last := ClusterLastActivityAt(c)
	if last == nil {
		return model.ClusterStatusOffline
	}
	if now.Sub(last.UTC()) > ClusterOnlineTTL {
		return model.ClusterStatusOffline
	}
	return status
}

func ClusterLastActivityAt(c *model.Cluster) *time.Time {
	if c == nil {
		return nil
	}
	var out *time.Time
	if c.LastSeenAt != nil {
		out = c.LastSeenAt
	}
	if c.InventorySyncedAt != nil && (out == nil || c.InventorySyncedAt.After(*out)) {
		out = c.InventorySyncedAt
	}
	return out
}
