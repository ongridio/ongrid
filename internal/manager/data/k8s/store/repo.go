package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	biz "github.com/ongridio/ongrid/internal/manager/biz/k8s"
	model "github.com/ongridio/ongrid/internal/manager/model/k8s"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the GORM-backed Kubernetes onboarding repository.
type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

var _ biz.Repository = (*Repo)(nil)

func (r *Repo) CreateCluster(ctx context.Context, c *model.Cluster) error {
	if c == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Create(c).Error
}

func (r *Repo) GetCluster(ctx context.Context, id uint64) (*model.Cluster, error) {
	var c model.Cluster
	if err := r.db.WithContext(ctx).First(&c, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (r *Repo) GetClusterByControllerEdge(ctx context.Context, edgeID uint64) (*model.Cluster, error) {
	var c model.Cluster
	if err := r.db.WithContext(ctx).
		Where("controller_edge_id = ?", edgeID).
		First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

func (r *Repo) ListClusters(ctx context.Context, f biz.ListClustersFilter) ([]*model.Cluster, error) {
	tx := applyClusterFilters(r.db.WithContext(ctx).Model(&model.Cluster{}), f)
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Cluster
	if err := tx.Order("id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CountClusters(ctx context.Context, f biz.ListClustersFilter) (int64, error) {
	var total int64
	if err := applyClusterFilters(r.db.WithContext(ctx).Model(&model.Cluster{}), f).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func applyClusterFilters(tx *gorm.DB, f biz.ListClustersFilter) *gorm.DB {
	if f.Status != "" {
		tx = tx.Where("status = ?", f.Status)
	}
	if f.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+f.Name+"%")
	}
	if f.Mode != "" {
		tx = tx.Where("mode = ?", f.Mode)
	}
	return tx
}

func (r *Repo) UpdateClusterTokens(ctx context.Context, id uint64, controllerTokenHash, nodeTokenHash string, expiresAt *time.Time) error {
	res := r.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", id).Updates(map[string]any{
		"bootstrap_token_hash":       controllerTokenHash,
		"node_bootstrap_token_hash":  nodeTokenHash,
		"bootstrap_token_expires_at": expiresAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) ClearControllerBootstrapToken(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Model(&model.Cluster{}).
		Where("id = ?", id).
		UpdateColumn("bootstrap_token_hash", "").Error
}

func (r *Repo) UpdateClusterController(ctx context.Context, id uint64, in biz.ClusterControllerRegistration) error {
	res := r.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", id).Updates(map[string]any{
		"controller_edge_id":   in.EdgeID,
		"controller_node_name": in.NodeName,
		"controller_namespace": in.Namespace,
		"controller_pod_name":  in.PodName,
		"last_seen_at":         in.LastSeen,
		"status":               model.ClusterStatusOnline,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) BindControllerEnrollment(ctx context.Context, id uint64, registration biz.ClusterControllerRegistration, installation *model.Installation) error {
	if installation == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.Cluster{}).Where("id = ?", id).Updates(map[string]any{
			"controller_edge_id":   registration.EdgeID,
			"controller_node_name": registration.NodeName,
			"controller_namespace": registration.Namespace,
			"controller_pod_name":  registration.PodName,
			"last_seen_at":         registration.LastSeen,
			"status":               model.ClusterStatusOnline,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "cluster_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"mode":               installation.Mode,
				"scope_type":         installation.ScopeType,
				"namespace":          installation.Namespace,
				"controller_edge_id": installation.ControllerEdgeID,
				"capabilities_json":  installation.CapabilitiesJSON,
				"last_seen_at":       installation.LastSeenAt,
				"updated_at":         time.Now(),
			}),
		}).Create(installation).Error
	})
}

func (r *Repo) UpdateClusterInventorySync(ctx context.Context, id uint64, in biz.ClusterInventorySync) error {
	res := r.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", id).Updates(map[string]any{
		"last_seen_at":                     in.SyncedAt,
		"status":                           model.ClusterStatusOnline,
		"inventory_resource_version":       in.ResourceVersion,
		"inventory_resource_versions_json": in.ResourceVersionsJSON,
		"inventory_scope":                  in.Scope,
		"inventory_namespace":              in.Namespace,
		"inventory_sync_duration_ms":       in.SyncDurationMS,
		"inventory_watch_lag_seconds":      in.WatchLagSeconds,
		"inventory_synced_at":              in.SyncedAt,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) UpdateClusterTopologyNode(ctx context.Context, id, nodeID uint64) error {
	res := r.db.WithContext(ctx).Model(&model.Cluster{}).
		Where("id = ? AND (node_id IS NULL OR node_id <> ?)", id, nodeID).
		Update("node_id", nodeID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		var exists int64
		if err := r.db.WithContext(ctx).Model(&model.Cluster{}).Where("id = ?", id).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return errs.ErrNotFound
		}
	}
	return nil
}

func (r *Repo) UpdateDeviceTopologyNode(ctx context.Context, id, nodeID uint64) error {
	res := r.db.WithContext(ctx).Table("devices").
		Where("id = ? AND deleted_at IS NULL AND (node_id IS NULL OR node_id <> ?)", id, nodeID).
		Update("node_id", nodeID)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		var exists int64
		if err := r.db.WithContext(ctx).Table("devices").Where("id = ? AND deleted_at IS NULL", id).Count(&exists).Error; err != nil {
			return err
		}
		if exists == 0 {
			return errs.ErrNotFound
		}
	}
	return nil
}

func (r *Repo) ListClusterEdgeIDs(ctx context.Context, clusterID uint64) ([]uint64, error) {
	var rows []struct {
		EdgeID uint64 `gorm:"column:edge_id"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT edge_id
		FROM (
			SELECT controller_edge_id AS edge_id
			FROM k8s_clusters
			WHERE id = ? AND controller_edge_id IS NOT NULL AND controller_edge_id <> 0
			UNION
			SELECT edge_id
			FROM k8s_nodes
			WHERE cluster_id = ? AND edge_id IS NOT NULL AND edge_id <> 0
			UNION
			SELECT controller_edge_id AS edge_id
			FROM k8s_installations
			WHERE cluster_id = ? AND controller_edge_id IS NOT NULL AND controller_edge_id <> 0
		) AS cluster_edges
		ORDER BY edge_id ASC
	`, clusterID, clusterID, clusterID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(rows))
	for _, row := range rows {
		if row.EdgeID != 0 {
			out = append(out, row.EdgeID)
		}
	}
	return out, nil
}

func (r *Repo) GetClusterIDByEdgeID(ctx context.Context, edgeID uint64) (uint64, error) {
	var row struct {
		ClusterID uint64 `gorm:"column:cluster_id"`
	}
	err := r.db.WithContext(ctx).Raw(`
		SELECT cluster_id
		FROM (
			SELECT id AS cluster_id, controller_edge_id AS edge_id
			FROM k8s_clusters
			WHERE deleted_at IS NULL
			UNION ALL
			SELECT cluster_id, edge_id FROM k8s_nodes
			UNION ALL
			SELECT cluster_id, controller_edge_id AS edge_id FROM k8s_installations
		) AS managed_edges
		WHERE edge_id = ?
		LIMIT 1
	`, edgeID).Scan(&row).Error
	if err != nil {
		return 0, err
	}
	if row.ClusterID == 0 {
		return 0, errs.ErrNotFound
	}
	return row.ClusterID, nil
}

func (r *Repo) DeleteCluster(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range []any{
			&model.Node{},
			&model.Workload{},
			&model.Pod{},
			&model.Event{},
			&model.Installation{},
		} {
			if err := tx.Where("cluster_id = ?", id).Delete(item).Error; err != nil {
				return err
			}
		}
		res := tx.Delete(&model.Cluster{}, id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errs.ErrNotFound
		}
		return nil
	})
}

func (r *Repo) GetNodeByClusterUID(ctx context.Context, clusterID uint64, nodeUID string) (*model.Node, error) {
	var n model.Node
	if err := r.db.WithContext(ctx).
		Where("cluster_id = ? AND node_uid = ?", clusterID, nodeUID).
		First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (r *Repo) GetNodeByEdgeID(ctx context.Context, edgeID uint64) (*model.Node, error) {
	var n model.Node
	if err := r.db.WithContext(ctx).Where("edge_id = ?", edgeID).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (r *Repo) GetLinkedNodeByClusterName(ctx context.Context, clusterID uint64, nodeName string) (*model.Node, error) {
	var n model.Node
	if err := r.db.WithContext(ctx).
		Where("cluster_id = ? AND node_name = ? AND (edge_id IS NOT NULL OR device_id IS NOT NULL)", clusterID, nodeName).
		Order("id DESC").
		First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

func (r *Repo) ListNodesByRefs(ctx context.Context, clusterID uint64, refs []biz.NodeRef) ([]*model.Node, error) {
	predicates := make([]string, 0, len(refs))
	args := make([]any, 0, len(refs))
	for _, ref := range refs {
		switch {
		case ref.UID != "":
			predicates = append(predicates, "node_uid = ?")
			args = append(args, ref.UID)
		case ref.Name != "":
			predicates = append(predicates, "node_name = ?")
			args = append(args, ref.Name)
		}
	}
	if len(predicates) == 0 {
		return nil, nil
	}
	var nodes []*model.Node
	err := r.db.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Where("("+strings.Join(predicates, " OR ")+")", args...).
		Find(&nodes).Error
	return nodes, err
}

func (r *Repo) ListStaleNodes(ctx context.Context, clusterID uint64, olderThan time.Time) ([]*model.Node, error) {
	var nodes []*model.Node
	err := r.db.WithContext(ctx).
		Where("cluster_id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", clusterID, olderThan).
		Find(&nodes).Error
	return nodes, err
}

func (r *Repo) UpsertNode(ctx context.Context, n *model.Node) error {
	if n == nil {
		return errs.ErrInvalid
	}
	assignments := map[string]any{
		"node_name":        n.NodeName,
		"provider_id":      n.ProviderID,
		"labels_json":      n.LabelsJSON,
		"taints_json":      n.TaintsJSON,
		"conditions_json":  n.ConditionsJSON,
		"capacity_json":    n.CapacityJSON,
		"allocatable_json": n.AllocatableJSON,
		"kubelet_version":  n.KubeletVersion,
		"last_seen_at":     n.LastSeenAt,
		"updated_at":       time.Now(),
	}
	if n.EdgeID != nil {
		assignments["edge_id"] = *n.EdgeID
	}
	if n.DeviceID != nil {
		assignments["device_id"] = *n.DeviceID
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "cluster_id"}, {Name: "node_uid"}},
		DoUpdates: clause.Assignments(assignments),
	}).Create(n).Error
}

func (r *Repo) DeleteDuplicateNodesByName(ctx context.Context, clusterID uint64, nodeName, keepUID string) error {
	return r.db.WithContext(ctx).
		Where("cluster_id = ? AND node_name = ? AND node_uid <> ?", clusterID, nodeName, keepUID).
		Delete(&model.Node{}).Error
}

func (r *Repo) UpdateNodeEdge(ctx context.Context, nodeID, edgeID uint64, deviceID *uint64, lastSeen time.Time) error {
	updates := map[string]any{
		"edge_id":      edgeID,
		"last_seen_at": lastSeen,
	}
	if deviceID != nil {
		updates["device_id"] = *deviceID
	}
	res := r.db.WithContext(ctx).Model(&model.Node{}).Where("id = ?", nodeID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *Repo) UpsertWorkloads(ctx context.Context, items []*model.Workload) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "cluster_id"},
			{Name: "kind"},
			{Name: "namespace"},
			{Name: "name"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"uid",
			"desired_replicas",
			"ready_replicas",
			"labels_json",
			"annotations_json",
			"conditions_json",
			"last_seen_at",
			"updated_at",
		}),
	}).CreateInBatches(&items, 200).Error
}

func (r *Repo) UpsertPods(ctx context.Context, items []*model.Pod) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "cluster_id"},
			{Name: "namespace"},
			{Name: "name"},
			{Name: "uid"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"node_name",
			"phase",
			"owner_kind",
			"owner_name",
			"restart_count",
			"reason",
			"last_seen_at",
			"updated_at",
		}),
	}).CreateInBatches(&items, 200).Error
}

func (r *Repo) UpsertEvents(ctx context.Context, items []*model.Event) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "cluster_id"},
			{Name: "uid"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"namespace",
			"name",
			"type",
			"reason",
			"message",
			"involved_kind",
			"involved_namespace",
			"involved_name",
			"involved_uid",
			"source_component",
			"source_host",
			"reporting_controller",
			"reporting_instance",
			"action",
			"count",
			"first_timestamp",
			"last_timestamp",
			"event_time",
			"last_seen_at",
			"updated_at",
		}),
	}).CreateInBatches(&items, 200).Error
}

func (r *Repo) DeleteNodes(ctx context.Context, clusterID uint64, refs []biz.NodeRef) error {
	for _, ref := range refs {
		tx := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID)
		switch {
		case ref.UID != "":
			tx = tx.Where("node_uid = ?", ref.UID)
		case ref.Name != "":
			tx = tx.Where("node_name = ?", ref.Name)
		default:
			continue
		}
		if err := tx.Delete(&model.Node{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) DeleteWorkloads(ctx context.Context, clusterID uint64, refs []biz.WorkloadRef) error {
	for _, ref := range refs {
		if ref.Kind == "" || ref.Name == "" {
			continue
		}
		if err := r.db.WithContext(ctx).
			Where("cluster_id = ? AND kind = ? AND namespace = ? AND name = ?", clusterID, ref.Kind, ref.Namespace, ref.Name).
			Delete(&model.Workload{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) DeletePods(ctx context.Context, clusterID uint64, refs []biz.PodRef) error {
	for _, ref := range refs {
		tx := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID)
		switch {
		case ref.UID != "":
			tx = tx.Where("uid = ?", ref.UID)
		case ref.Name != "":
			tx = tx.Where("namespace = ? AND name = ?", ref.Namespace, ref.Name)
		default:
			continue
		}
		if err := tx.Delete(&model.Pod{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) DeleteEvents(ctx context.Context, clusterID uint64, refs []biz.EventRef) error {
	for _, ref := range refs {
		tx := r.db.WithContext(ctx).Where("cluster_id = ?", clusterID)
		switch {
		case ref.UID != "":
			tx = tx.Where("uid = ?", ref.UID)
		case ref.Name != "":
			tx = tx.Where("namespace = ? AND name = ?", ref.Namespace, ref.Name)
		default:
			continue
		}
		if err := tx.Delete(&model.Event{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) DeleteStaleWorkloads(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error {
	tx := r.db.WithContext(ctx).
		Where("cluster_id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", clusterID, olderThan)
	if namespace != nil {
		tx = tx.Where("namespace = ?", *namespace)
	}
	return tx.Delete(&model.Workload{}).Error
}

func (r *Repo) DeleteStalePods(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error {
	tx := r.db.WithContext(ctx).
		Where("cluster_id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", clusterID, olderThan)
	if namespace != nil {
		tx = tx.Where("namespace = ?", *namespace)
	}
	return tx.Delete(&model.Pod{}).Error
}

func (r *Repo) DeleteStaleEvents(ctx context.Context, clusterID uint64, namespace *string, olderThan time.Time) error {
	tx := r.db.WithContext(ctx).
		Where("cluster_id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)", clusterID, olderThan)
	if namespace != nil {
		tx = tx.Where("namespace = ?", *namespace)
	}
	return tx.Delete(&model.Event{}).Error
}

func (r *Repo) DeleteEventsBefore(ctx context.Context, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	ids, err := r.oldEventIDs(ctx, r.db.WithContext(ctx).Model(&model.Event{}).
		Where(eventTimestampExpr()+" < ?", cutoff).
		Order(eventTimestampExpr()+" ASC, id ASC").
		Limit(limit),
	)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return r.deleteEventsByIDs(ctx, ids)
}

func (r *Repo) DeleteOldestEvents(ctx context.Context, clusterID uint64, keep, limit int) (int64, error) {
	if keep < 0 || limit <= 0 {
		return 0, nil
	}
	ids, err := r.oldEventIDs(ctx, r.db.WithContext(ctx).Model(&model.Event{}).
		Where("cluster_id = ?", clusterID).
		Order(eventTimestampExpr()+" DESC, id DESC").
		Offset(keep).
		Limit(limit),
	)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	return r.deleteEventsByIDs(ctx, ids)
}

func (r *Repo) oldEventIDs(ctx context.Context, tx *gorm.DB) ([]uint64, error) {
	var ids []uint64
	if err := tx.WithContext(ctx).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Repo) deleteEventsByIDs(ctx context.Context, ids []uint64) (int64, error) {
	res := r.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.Event{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func eventTimestampExpr() string {
	return "COALESCE(last_timestamp, event_time, first_timestamp, last_seen_at, created_at)"
}

func (r *Repo) ListNodes(ctx context.Context, clusterID uint64) ([]*model.Node, error) {
	var out []*model.Node
	if err := r.db.WithContext(ctx).
		Where("cluster_id = ?", clusterID).
		Order("node_name ASC").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) ListTopologyNodeLinks(ctx context.Context, clusterID uint64) ([]biz.TopologyNodeLink, error) {
	var out []biz.TopologyNodeLink
	if err := r.db.WithContext(ctx).
		Table("k8s_nodes AS kn").
		Select("kn.node_name, kn.node_uid, kn.device_id, d.name AS device_name, d.node_id AS device_node_id").
		Joins("LEFT JOIN devices AS d ON d.id = kn.device_id AND d.deleted_at IS NULL").
		Where("kn.cluster_id = ?", clusterID).
		Order("kn.node_name ASC").
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CountNodes(ctx context.Context, clusterID uint64) (int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).
		Model(&model.Node{}).
		Where("cluster_id = ?", clusterID).
		Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repo) GetNodeCoverage(ctx context.Context, clusterID uint64) (biz.NodeCoverage, error) {
	out := biz.NodeCoverage{ClusterID: clusterID}
	if err := r.db.WithContext(ctx).
		Model(&model.Node{}).
		Select(`
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN edge_id IS NOT NULL THEN 1 ELSE 0 END), 0) AS edge_linked,
			COALESCE(SUM(CASE WHEN device_id IS NOT NULL THEN 1 ELSE 0 END), 0) AS device_linked
		`).
		Where("cluster_id = ?", clusterID).
		Scan(&out).Error; err != nil {
		return biz.NodeCoverage{}, err
	}
	return out, nil
}

func (r *Repo) GetNodeCoverageByClusterIDs(ctx context.Context, clusterIDs []uint64) (map[uint64]biz.NodeCoverage, error) {
	out := make(map[uint64]biz.NodeCoverage, len(clusterIDs))
	if len(clusterIDs) == 0 {
		return out, nil
	}
	var rows []biz.NodeCoverage
	if err := r.db.WithContext(ctx).
		Model(&model.Node{}).
		Select(`
			cluster_id,
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN edge_id IS NOT NULL THEN 1 ELSE 0 END), 0) AS edge_linked,
			COALESCE(SUM(CASE WHEN device_id IS NOT NULL THEN 1 ELSE 0 END), 0) AS device_linked
		`).
		Where("cluster_id IN ?", clusterIDs).
		Group("cluster_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.ClusterID] = row
	}
	for _, clusterID := range clusterIDs {
		if _, ok := out[clusterID]; !ok {
			out[clusterID] = biz.NodeCoverage{ClusterID: clusterID}
		}
	}
	return out, nil
}

const edgeAttachmentsQuery = `
	SELECT edge_id, cluster_id, cluster_name, cluster_mode, node_name, kind
	FROM (
		SELECT c.controller_edge_id AS edge_id, c.id AS cluster_id, c.name AS cluster_name,
			c.mode AS cluster_mode, c.controller_node_name AS node_name, 'k8s-controller' AS kind
		FROM k8s_clusters c
		WHERE c.delete_marker = 0 AND c.controller_edge_id IS NOT NULL AND c.controller_edge_id <> 0
		UNION ALL
		SELECT n.edge_id, c.id, c.name, c.mode, n.node_name, 'k8s-node'
		FROM k8s_nodes n
		JOIN k8s_clusters c ON c.id = n.cluster_id AND c.delete_marker = 0
		WHERE n.edge_id IS NOT NULL AND n.edge_id <> 0
		UNION ALL
		SELECT n.edge_id, c.id, c.name, c.mode, n.node_name, 'k8s-controller-runtime'
		FROM k8s_nodes n
		JOIN k8s_clusters c ON c.id = n.cluster_id AND c.delete_marker = 0
		WHERE n.edge_id IS NOT NULL AND n.edge_id <> 0
			AND c.controller_node_name <> '' AND n.node_name = c.controller_node_name
	) attachments`

func (r *Repo) ListEdgeAttachments(ctx context.Context, limit, offset int) ([]biz.EdgeAttachment, int64, error) {
	var total int64
	if err := r.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM (" + edgeAttachmentsQuery + ") counted").Scan(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []biz.EdgeAttachment
	if err := r.db.WithContext(ctx).Raw(
		edgeAttachmentsQuery+" ORDER BY cluster_id ASC, edge_id ASC, kind ASC LIMIT ? OFFSET ?",
		limit,
		offset,
	).Scan(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func (r *Repo) ListWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) ([]*model.Workload, error) {
	tx := applyWorkloadFilter(r.db.WithContext(ctx).Model(&model.Workload{}), f)
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Workload
	if err := tx.Order("namespace ASC, kind ASC, name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CountWorkloads(ctx context.Context, f biz.ListWorkloadsFilter) (int64, error) {
	var total int64
	if err := applyWorkloadFilter(r.db.WithContext(ctx).Model(&model.Workload{}), f).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repo) ListPods(ctx context.Context, f biz.ListPodsFilter) ([]*model.Pod, error) {
	tx := applyPodFilter(r.db.WithContext(ctx).Model(&model.Pod{}), f)
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Pod
	if err := tx.Order("namespace ASC, name ASC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CountPods(ctx context.Context, f biz.ListPodsFilter) (int64, error) {
	var total int64
	if err := applyPodFilter(r.db.WithContext(ctx).Model(&model.Pod{}), f).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repo) ListEvents(ctx context.Context, f biz.ListEventsFilter) ([]*model.Event, error) {
	tx := applyEventFilter(r.db.WithContext(ctx).Model(&model.Event{}), f)
	if f.Limit > 0 {
		tx = tx.Limit(f.Limit)
	}
	if f.Offset > 0 {
		tx = tx.Offset(f.Offset)
	}
	var out []*model.Event
	if err := tx.Order("COALESCE(last_timestamp, event_time, last_seen_at, created_at) DESC, id DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repo) CountEvents(ctx context.Context, f biz.ListEventsFilter) (int64, error) {
	var total int64
	if err := applyEventFilter(r.db.WithContext(ctx).Model(&model.Event{}), f).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (r *Repo) UpsertInstallation(ctx context.Context, in *model.Installation) error {
	if in == nil {
		return errs.ErrInvalid
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "cluster_id"},
			{Name: "mode"},
			{Name: "scope_type"},
			{Name: "namespace"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"controller_edge_id": in.ControllerEdgeID,
			"capabilities_json":  in.CapabilitiesJSON,
			"last_seen_at":       in.LastSeenAt,
			"updated_at":         time.Now(),
		}),
	}).Create(in).Error
}

func applyWorkloadFilter(tx *gorm.DB, f biz.ListWorkloadsFilter) *gorm.DB {
	tx = tx.Where("cluster_id = ?", f.ClusterID)
	if f.Namespace != "" {
		tx = tx.Where("namespace = ?", f.Namespace)
	}
	if f.Kind != "" {
		tx = tx.Where("kind = ?", f.Kind)
	}
	if f.IssueOnly {
		tx = tx.Where("ready_replicas < desired_replicas")
	}
	tx = applyLikeAny(tx, f.Query, []string{"namespace", "kind", "name", "uid"})
	return tx
}

func applyPodFilter(tx *gorm.DB, f biz.ListPodsFilter) *gorm.DB {
	tx = tx.Where("cluster_id = ?", f.ClusterID)
	if f.Namespace != "" {
		tx = tx.Where("namespace = ?", f.Namespace)
	}
	if f.NodeName != "" {
		tx = tx.Where("node_name = ?", f.NodeName)
	}
	if f.Phase != "" {
		tx = tx.Where("phase = ?", f.Phase)
	}
	if f.Reason != "" {
		tx = tx.Where("reason = ?", f.Reason)
	}
	if f.IssueOnly {
		tx = tx.Where(
			"phase IN ? OR reason IN ?",
			[]string{"Pending", "Failed"},
			[]string{"CrashLoopBackOff", "OOMKilled", "ImagePullBackOff", "ErrImagePull"},
		)
	}
	tx = applyLikeAny(tx, f.Query, []string{"namespace", "name", "uid", "node_name", "phase", "reason", "owner_kind", "owner_name"})
	return tx
}

func applyEventFilter(tx *gorm.DB, f biz.ListEventsFilter) *gorm.DB {
	tx = tx.Where("cluster_id = ?", f.ClusterID)
	if f.Namespace != "" {
		tx = tx.Where("namespace = ?", f.Namespace)
	}
	if f.Type != "" {
		tx = tx.Where("type = ?", f.Type)
	}
	if f.Reason != "" {
		tx = tx.Where("reason = ?", f.Reason)
	}
	if f.InvolvedKind != "" {
		tx = tx.Where("involved_kind = ?", f.InvolvedKind)
	}
	if f.InvolvedName != "" {
		tx = tx.Where("involved_name = ?", f.InvolvedName)
	}
	if f.InvolvedPodUID != "" {
		tx = tx.Where("involved_kind = ? AND involved_uid = ?", "Pod", f.InvolvedPodUID)
	}
	if f.IssueOnly {
		tx = tx.Where("type = ?", "Warning")
	}
	tx = applyLikeAny(tx, f.Query, []string{
		"namespace",
		"name",
		"type",
		"reason",
		"message",
		"involved_kind",
		"involved_namespace",
		"involved_name",
		"source_component",
		"reporting_controller",
	})
	return tx
}

func applyLikeAny(tx *gorm.DB, query string, columns []string) *gorm.DB {
	query = strings.TrimSpace(query)
	if query == "" || len(columns) == 0 {
		return tx
	}
	clauses := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))
	pattern := "%" + query + "%"
	for _, column := range columns {
		clauses = append(clauses, column+" LIKE ?")
		args = append(args, pattern)
	}
	tx = tx.Where("("+strings.Join(clauses, " OR ")+")", args...)
	return tx
}
