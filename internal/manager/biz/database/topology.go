package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	topobiz "github.com/ongridio/ongrid/internal/manager/biz/topology"
	model "github.com/ongridio/ongrid/internal/manager/model/database"
	topomodel "github.com/ongridio/ongrid/internal/manager/model/topology"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// TopologySyncer keeps the topology graph in sync with database instance
// lifecycle events. Every database instance gets a `database`-type node
// connected to its host device via a `deployed_on` relation.
type TopologySyncer struct {
	topo *topobiz.Usecase
	log  *slog.Logger
}

// NewTopologySyncer constructs the syncer. topo may be nil — SyncDBInstance
// becomes a no-op when topology isn't wired (graceful degradation during
// boot or for deployments that disable topology).
func NewTopologySyncer(topo *topobiz.Usecase, log *slog.Logger) *TopologySyncer {
	if log == nil {
		log = slog.Default()
	}
	return &TopologySyncer{topo: topo, log: log}
}

// SyncDBInstance creates or updates the topology node + relation for a
// database instance. Idempotent — called on every Create and Update.
func (s *TopologySyncer) SyncDBInstance(ctx context.Context, inst *model.DatabaseInstance) error {
	if s.topo == nil {
		return nil // no-op when topology not wired
	}
	if inst == nil {
		return nil
	}

	// Build props with instance metadata for the topology graph.
	props := map[string]any{
		"db_type": inst.DBType,
		"host":    inst.Host,
		"port":    inst.Port,
		"version": inst.Version,
		"status":  inst.Status,
	}
	propsJSON, _ := json.Marshal(props)
	nodeName := fmt.Sprintf("%s (%s:%d)", inst.Name, inst.Host, inst.Port)

	// Try to find existing node by listing database-type nodes matching
	// our name. Since Node.Type="database" + the node name is stable
	// (instance name + host:port), this gives us idempotency.
	nodes, _, err := s.topo.ListNodes(ctx, topobiz.NodeListFilter{
		Type: string(topomodel.NodeTypeDB),
		Q:    nodeName,
	})
	if err != nil && !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("topology sync: list nodes: %w", err)
	}

	var nodeID uint64
	if len(nodes) > 0 {
		// Update existing node.
		nodeID = nodes[0].ID
		if err := s.topo.UpdateNode(ctx, nodeID, nodeName, string(propsJSON)); err != nil {
			return fmt.Errorf("topology sync: update node: %w", err)
		}
	} else {
		// Create new node.
		node, err := s.topo.CreateNode(ctx, string(topomodel.NodeTypeDB), nodeName, string(propsJSON))
		if err != nil {
			return fmt.Errorf("topology sync: create node: %w", err)
		}
		nodeID = node.ID
	}

	// Link to host device via deployed_on relation. Find the device node
	// by listing nodes with Type="device" matching the edge id.
	devices, _, err := s.topo.ListNodes(ctx, topobiz.NodeListFilter{
		Type: string(topomodel.NodeTypeDevice),
		Q:    strconv.FormatUint(inst.EdgeID, 10),
	})
	if err != nil || len(devices) == 0 {
		s.log.Warn("topology sync: device node not found, skipping relation",
			slog.Uint64("edge_id", inst.EdgeID))
		return nil
	}

	// We model "database deployed_on device" as database → device Src→Dst
	// with RelDeployedOn. Failure on device propagates to database.
	if _, err := s.topo.CreateRelation(ctx, nodeID, devices[0].ID, topomodel.RelDeployedOn, "{}"); err != nil {
		if errors.Is(err, errs.ErrConflict) {
			return nil // relation already exists
		}
		return fmt.Errorf("topology sync: create relation: %w", err)
	}
	return nil
}

// RemoveDBInstance cleans up the topology node and relations when a
// database instance is deleted.
func (s *TopologySyncer) RemoveDBInstance(ctx context.Context, inst *model.DatabaseInstance) error {
	if s.topo == nil || inst == nil {
		return nil
	}
	nodeName := fmt.Sprintf("%s (%s:%d)", inst.Name, inst.Host, inst.Port)
	nodes, _, err := s.topo.ListNodes(ctx, topobiz.NodeListFilter{
		Type: string(topomodel.NodeTypeDB),
		Q:    nodeName,
	})
	if err != nil {
		return nil // nothing to clean up
	}
	for _, node := range nodes {
		if err := s.topo.DeleteNode(ctx, node.ID); err != nil {
			s.log.Warn("topology sync: delete node", slog.Uint64("id", node.ID), slog.Any("err", err))
		}
	}
	return nil
}
