package store

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	biz "github.com/ongridio/ongrid/internal/manager/biz/device"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// NetworkDiscoveryRepo is the GORM-backed candidate observation store.
type NetworkDiscoveryRepo struct {
	db *gorm.DB
}

func NewNetworkDiscoveryRepo(db *gorm.DB) *NetworkDiscoveryRepo {
	return &NetworkDiscoveryRepo{db: db}
}

var _ biz.NetworkDiscoveryRepo = (*NetworkDiscoveryRepo)(nil)
var _ biz.NetworkPromotionRepo = (*NetworkDiscoveryRepo)(nil)

// UpsertCandidates keeps the latest observation while preserving the first
// seen timestamp. The unique observation key makes retries idempotent.
func (r *NetworkDiscoveryRepo) UpsertCandidates(ctx context.Context, candidates []*model.NetworkDiscoveryCandidate) error {
	if len(candidates) == 0 {
		return nil
	}
	keys := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		keys = append(keys, candidate.ObservationKey)
	}
	var verifiedRows []*model.NetworkDiscoveryCandidate
	if err := r.db.WithContext(ctx).
		Where("observation_key IN ? AND source = ? AND status IN ?", keys, "snmp", []string{
			biz.NetworkCandidateStatusSNMPVerified,
			biz.NetworkCandidateStatusPromoted,
		}).
		Find(&verifiedRows).Error; err != nil {
		return err
	}
	verifiedByKey := make(map[string]*model.NetworkDiscoveryCandidate, len(verifiedRows))
	for _, row := range verifiedRows {
		verifiedByKey[row.ObservationKey] = row
	}
	for _, candidate := range candidates {
		verified := verifiedByKey[candidate.ObservationKey]
		if verified == nil || candidate.Source == "snmp" {
			continue
		}
		// Passive LLDP/ARP refreshes keep liveness current but must not erase
		// the richer identity, interface and link snapshot established by a
		// successful authenticated SNMP read.
		candidate.Source = verified.Source
		if strings.TrimSpace(candidate.IPAddress) == "" {
			candidate.IPAddress = verified.IPAddress
		}
		candidate.SourceDataJSON = verified.SourceDataJSON
		candidate.InterfacesJSON = verified.InterfacesJSON
		if strings.TrimSpace(candidate.LinksJSON) == "" || strings.TrimSpace(candidate.LinksJSON) == "[]" {
			candidate.LinksJSON = verified.LinksJSON
		}
		if candidate.Confidence < verified.Confidence {
			candidate.Confidence = verified.Confidence
		}
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "observation_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"observer_edge_id", "ip_address", "mac", "interface_name", "source",
			"source_data_json", "interfaces_json", "links_json", "confidence",
			"last_seen_at", "expires_at",
		}),
	}).Create(&candidates).Error; err != nil {
		return err
	}

	// Status is a monotonic workflow state, not an observation field. Keep
	// terminal states intact while allowing a stronger LLDP/SNMP observation
	// to promote an earlier weak ARP/gateway candidate.
	for _, candidate := range candidates {
		if candidate.Status != biz.NetworkCandidateStatusIdentified {
			continue
		}
		if err := r.db.WithContext(ctx).Model(&model.NetworkDiscoveryCandidate{}).
			Where("observation_key = ? AND status = ?", candidate.ObservationKey, biz.NetworkCandidateStatusUnknown).
			Update("status", biz.NetworkCandidateStatusIdentified).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *NetworkDiscoveryRepo) ListCandidates(ctx context.Context, filter biz.NetworkCandidateFilter) ([]*model.NetworkDiscoveryCandidate, int64, error) {
	query := r.db.WithContext(ctx).Model(&model.NetworkDiscoveryCandidate{})
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	var rows []*model.NetworkDiscoveryCandidate
	if err := query.Order("last_seen_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if err := r.hydrateObserverSources(ctx, rows); err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

type observerEdgeRow struct {
	ID       uint64
	Name     string
	DeviceID *uint64 `gorm:"column:device_id"`
}

type observerDeviceRow struct {
	ID   uint64
	Name string
}

func (r *NetworkDiscoveryRepo) hydrateObserverSources(ctx context.Context, rows []*model.NetworkDiscoveryCandidate) error {
	if len(rows) == 0 {
		return nil
	}
	edgeIDs := make([]uint64, 0, len(rows))
	seen := make(map[uint64]struct{}, len(rows))
	for _, row := range rows {
		if row.ObserverEdgeID != 0 {
			if _, ok := seen[row.ObserverEdgeID]; !ok {
				edgeIDs = append(edgeIDs, row.ObserverEdgeID)
				seen[row.ObserverEdgeID] = struct{}{}
			}
		}
	}
	if len(edgeIDs) == 0 {
		return nil
	}
	var edges []observerEdgeRow
	if err := r.db.WithContext(ctx).Table("edges").
		Select("edges.id, edges.name, edge_devices.device_id").
		Joins("LEFT JOIN edge_devices ON edge_devices.edge_id = edges.id AND edge_devices.type = ? AND edge_devices.delete_marker = 0", model.EdgeDeviceRelationHost).
		Where("edges.id IN ?", edgeIDs).Find(&edges).Error; err != nil {
		return err
	}
	deviceIDs := make([]uint64, 0, len(edges))
	for _, edge := range edges {
		if edge.DeviceID != nil {
			deviceIDs = append(deviceIDs, *edge.DeviceID)
		}
	}
	devices := make(map[uint64]string, len(deviceIDs))
	if len(deviceIDs) > 0 {
		var hosts []observerDeviceRow
		if err := r.db.WithContext(ctx).Table("devices").Select("id, name").Where("id IN ?", deviceIDs).Find(&hosts).Error; err != nil {
			return err
		}
		for _, host := range hosts {
			devices[host.ID] = host.Name
		}
	}
	byEdge := make(map[uint64]observerEdgeRow, len(edges))
	for _, edge := range edges {
		byEdge[edge.ID] = edge
	}
	for _, row := range rows {
		edge, ok := byEdge[row.ObserverEdgeID]
		if !ok {
			continue
		}
		row.ObserverEdgeName = edge.Name
		if edge.DeviceID != nil {
			id := *edge.DeviceID
			row.ObserverHostDeviceID = &id
			row.ObserverHostName = devices[id]
		}
	}
	return nil
}

func (r *NetworkDiscoveryRepo) UpdateCandidate(ctx context.Context, candidate *model.NetworkDiscoveryCandidate) error {
	if candidate == nil || candidate.ID == 0 {
		return errs.ErrInvalid
	}
	result := r.db.WithContext(ctx).Model(&model.NetworkDiscoveryCandidate{}).
		Where("id = ?", candidate.ID).
		Updates(map[string]any{
			"ip_address": candidate.IPAddress, "source": candidate.Source,
			"source_data_json": candidate.SourceDataJSON, "interfaces_json": candidate.InterfacesJSON,
			"links_json": candidate.LinksJSON, "status": candidate.Status,
			"confidence": candidate.Confidence, "last_seen_at": candidate.LastSeenAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *NetworkDiscoveryRepo) GetCandidate(ctx context.Context, id uint64) (*model.NetworkDiscoveryCandidate, error) {
	var row model.NetworkDiscoveryCandidate
	if err := r.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (r *NetworkDiscoveryRepo) MarkCandidatePromoted(ctx context.Context, id, deviceID uint64) error {
	result := r.db.WithContext(ctx).Model(&model.NetworkDiscoveryCandidate{}).
		Where("id = ? AND status <> ?", id, biz.NetworkCandidateStatusIgnored).
		Updates(map[string]any{"status": biz.NetworkCandidateStatusPromoted, "promoted_device_id": deviceID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errs.ErrNotFound
	}
	return nil
}

func (r *NetworkDiscoveryRepo) UpsertDeviceNetwork(ctx context.Context, profile *model.DeviceNetwork) error {
	return r.db.WithContext(ctx).Save(profile).Error
}

func (r *NetworkDiscoveryRepo) GetNetworkDeviceDetail(ctx context.Context, deviceID uint64) (*biz.NetworkDeviceDetail, error) {
	var profile model.DeviceNetwork
	if err := r.db.WithContext(ctx).Where("device_id = ?", deviceID).First(&profile).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.ErrNotFound
		}
		return nil, err
	}

	var candidate model.NetworkDiscoveryCandidate
	err := r.db.WithContext(ctx).
		Where("promoted_device_id = ?", deviceID).
		Order("last_seen_at DESC, id DESC").
		First(&candidate).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	detail := &biz.NetworkDeviceDetail{Profile: &profile}
	if err == nil {
		rows := []*model.NetworkDiscoveryCandidate{&candidate}
		if err := r.hydrateObserverSources(ctx, rows); err != nil {
			return nil, err
		}
		detail.Candidate = &candidate
	}
	return detail, nil
}

func (r *NetworkDiscoveryRepo) ListDueNetworkPolls(ctx context.Context, now time.Time, limit int) ([]*biz.NetworkDeviceDetail, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var profiles []*model.DeviceNetwork
	dueCondition, dueArgs := networkPollDueCondition(r.db, now)
	if err := r.db.WithContext(ctx).
		Where("poll_enabled = ? AND poll_credential_name <> ?", true, "").
		Where(dueCondition, dueArgs...).
		Order("COALESCE(last_poll_at, created_at) ASC").Limit(limit).Find(&profiles).Error; err != nil {
		return nil, err
	}
	out := make([]*biz.NetworkDeviceDetail, 0, len(profiles))
	for _, profile := range profiles {
		detail, err := r.GetNetworkDeviceDetail(ctx, profile.DeviceID)
		if err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	return out, nil
}

func networkPollDueCondition(db *gorm.DB, now time.Time) (string, []any) {
	if db.Dialector.Name() == "sqlite" {
		return "last_poll_at IS NULL OR julianday(last_poll_at) <= julianday(?) - poll_interval_seconds / 86400.0", []any{now}
	}
	return "last_poll_at IS NULL OR DATE_ADD(last_poll_at, INTERVAL poll_interval_seconds SECOND) <= ?", []any{now}
}

func (r *NetworkDiscoveryRepo) ReplaceNetworkInterfaces(ctx context.Context, deviceID uint64, rows []*model.NetworkInterface) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("device_id = ?", deviceID).Delete(&model.NetworkInterface{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Create(&rows).Error
	})
}
