// Package dbcli discovers database instances running on the edge by
// reading the databasemetrics and custommetrics plugin configs, probing
// each database target for version and configuration, and reporting the
// results to the manager via the tunnel.
//
// The Discoverer is not a plugin — it reads other plugins' configs and
// therefore can't live inside the supervisor's per-plugin config model.
// Instead it runs as a lightweight goroutine in the edge agent's lifecycle.
package dbcli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// defaultProbeTimeout is how long we wait for a TCP dial.
const defaultProbeTimeout = 5 * time.Second

// SourceSpec holds the parsed database source from a plugin config.
type SourceSpec struct {
	ID       string
	DBType   string
	Name     string
	Host     string
	Port     int
	Plugin   string // "databasemetrics" or "custommetrics"
}

// Discoverer probes configured database sources and reports findings
// to the manager via the tunnel.
type Discoverer struct {
	client tunnel.Client
	log    *slog.Logger
}

// NewDiscoverer creates a discoverer that reads plugin configs and
// reports findings through the given tunnel client.
func NewDiscoverer(client tunnel.Client, log *slog.Logger) *Discoverer {
	if log == nil {
		log = slog.Default()
	}
	return &Discoverer{client: client, log: log.With(slog.String("comp", "dbcli"))}
}

// RunOnce does a single discovery pass: fetch configs, probe each
// source, push results. Returns the number of instances found.
func (d *Discoverer) RunOnce(ctx context.Context) (int, error) {
	if d.client == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sources, err := d.discoverSources(ctx)
	if err != nil {
		return 0, fmt.Errorf("discover sources: %w", err)
	}
	if len(sources) == 0 {
		return 0, nil
	}
	instances := d.probeInstances(ctx, sources)
	if len(instances) == 0 {
		return 0, nil
	}
	if err := d.pushInstances(ctx, instances); err != nil {
		return 0, fmt.Errorf("push instances: %w", err)
	}
	return len(instances), nil
}

// discoverSources reads the databasemetrics and custommetrics plugin
// configs from the manager and extracts database source specs.
func (d *Discoverer) discoverSources(ctx context.Context) ([]SourceSpec, error) {
	var resp tunnel.GetPluginConfigsResponse
	if err := d.client.Call(ctx, tunnel.MethodGetPluginConfigs, struct{}{}, &resp); err != nil {
		return nil, fmt.Errorf("get plugin configs: %w", err)
	}

	var sources []SourceSpec

	// Parse databasemetrics sources.
	if dmCfg, ok := resp.Configs["databasemetrics"]; ok {
		sources = append(sources, parseDMSources(dmCfg.Spec)...)
	}

	// Parse custommetrics sources tagged as database.
	if cmCfg, ok := resp.Configs["custommetrics"]; ok {
		sources = append(sources, parseCMSources(cmCfg.Spec)...)
	}

	return sources, nil
}

// parseDMSources extracts database sources from databasemetrics spec.
func parseDMSources(spec map[string]any) []SourceSpec {
	raw, ok := spec["sources"]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []SourceSpec
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := m["id"].(string)
		dbType, _ := m["db_type"].(string)
		name, _ := m["name"].(string)
		if id == "" || dbType == "" {
			continue
		}
		conn, _ := m["connection"].(map[string]any)
		connPath, _ := conn["path"].(string)
		host, port := parseConnPath(connPath)
		if name == "" {
			name = id
		}
		out = append(out, SourceSpec{
			ID: id, DBType: dbType, Name: name,
			Host: host, Port: port, Plugin: "databasemetrics",
		})
	}
	return out
}

// parseCMSources extracts database sources from custommetrics spec.
func parseCMSources(spec map[string]any) []SourceSpec {
	raw, ok := spec["targets"]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []SourceSpec
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		res, _ := m["resource"].(map[string]any)
		if res == nil {
			continue
		}
		category, _ := res["category"].(string)
		if !strings.EqualFold(category, "database") {
			continue
		}
		dbType, _ := res["type"].(string)
		dbType = normalizeDBType(dbType)
		id, _ := m["id"].(string)
		if id == "" || dbType == "" {
			continue
		}
		url, _ := m["url"].(string)
		host, port := parseURLHostPort(url)
		name, _ := m["name"].(string)
		if name == "" {
			name = id
		}
		out = append(out, SourceSpec{
			ID: id, DBType: dbType, Name: name,
			Host: host, Port: port, Plugin: "custommetrics",
		})
	}
	return out
}

// probeInstances dials each source and collects version info.
func (d *Discoverer) probeInstances(ctx context.Context, sources []SourceSpec) []tunnel.DBInstanceInfo {
	var out []tunnel.DBInstanceInfo
	for _, s := range sources {
		info := tunnel.DBInstanceInfo{
			DBType:     s.DBType,
			Name:       s.Name,
			Host:       s.Host,
			Port:       s.Port,
			Status:     "unknown",
			PluginType: s.Plugin,
		}
		if s.Host == "" || s.Port == 0 {
			info.Status = "offline"
		} else {
			version, err := probeVersion(ctx, s)
			if err != nil {
				info.Status = "offline"
				d.log.Debug("probe failed", slog.String("id", s.ID), slog.Any("err", err))
			} else {
				info.Status = "online"
				info.Version = version
			}
		}
		out = append(out, info)
	}
	return out
}

// pushInstances sends discovered instances to the manager.
func (d *Discoverer) pushInstances(ctx context.Context, instances []tunnel.DBInstanceInfo) error {
	req := tunnel.PushDBInstanceInfoRequest{Instances: instances}
	var resp tunnel.PushDBInstanceInfoResponse
	if err := d.client.Call(ctx, tunnel.MethodPushDBInstanceInfo, req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("manager rejected push")
	}
	d.log.Info("discovered instances pushed", slog.Int("count", len(instances)))
	return nil
}

// probeVersion dials the database and attempts version detection.
func probeVersion(ctx context.Context, s SourceSpec) (string, error) {
	timeout := defaultProbeTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if t := time.Until(deadline); t > 0 && t < timeout {
			timeout = t
		}
	}
	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	conn.Close()
	return "detected", nil
}

// --- helpers ---

func parseConnPath(path string) (host string, port int) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(path))
	if err != nil {
		return strings.TrimSpace(path), 0
	}
	p, _ := strconv.Atoi(portStr)
	return host, p
}

func parseURLHostPort(url string) (string, int) {
	url = strings.TrimSpace(url)
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(url, prefix) {
			url = strings.TrimPrefix(url, prefix)
			break
		}
	}
	if idx := strings.Index(url, "/"); idx > 0 {
		url = url[:idx]
	}
	host, portStr, err := net.SplitHostPort(url)
	if err != nil {
		return url, 0
	}
	p, _ := strconv.Atoi(portStr)
	return host, p
}

func normalizeDBType(t string) string {
	switch strings.ToLower(t) {
	case "postgres", "pg":
		return "postgresql"
	case "mongo":
		return "mongodb"
	default:
		return t
	}
}
