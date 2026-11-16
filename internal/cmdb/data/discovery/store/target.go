package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// --- targets ---

// ListTargets returns every target built on one model, ordered by id.
func (r *Repo) ListTargets(ctx context.Context, typeID model.ID) ([]discoverymodel.Target, error) {
	var rows []discoverymodel.Target
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: list targets: %w", err)
	}
	return rows, nil
}

// ListAllTargets returns every target, ordered by id.
func (r *Repo) ListAllTargets(ctx context.Context) ([]discoverymodel.Target, error) {
	var rows []discoverymodel.Target
	if err := r.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: list all targets: %w", err)
	}
	return rows, nil
}

// LoadTarget returns a target by id, or nil.
func (r *Repo) LoadTarget(ctx context.Context, id model.ID) (*discoverymodel.Target, error) {
	var row discoverymodel.Target
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb discovery: load target: %w", err)
	}
}

// CreateTarget inserts a target under the rule's own lock.
//
// THE LOCK IS WHAT MAKES THE PAIR SAFE. Deleting a rule counts the targets built
// on it before removing it; a create that read the rule, then waited while the
// delete ran, would insert a target pointing at a rule that no longer exists.
// Taking the rule row's lock here and in the delete means the two orderings both
// end somewhere consistent — either the delete sees this target and refuses, or
// this create finds no rule and refuses.
func (r *Repo) CreateTarget(ctx context.Context, in discoverybiz.TargetWrite) (*discoverymodel.Target, error) {
	var out *discoverymodel.Target
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.AdrID != nil && *in.AdrID != 0 {
			if err := lockRuleRow(tx, *in.AdrID); err != nil {
				return err
			}
		}
		row := discoverymodel.Target{
			Attributes: in.Attributes,
			AutoAccept: false,
			Enabled:    true,
		}
		if in.TypeID != 0 {
			typeID := in.TypeID
			row.TypeID = &typeID
		}
		row.AdrID = in.AdrID
		if in.AutoAccept != nil {
			row.AutoAccept = *in.AutoAccept
		}
		if in.Enabled != nil {
			row.Enabled = *in.Enabled
		}
		row.AgentID = in.AgentID
		row.QueryExpr = in.QueryExpr
		row.Interval = in.Interval
		row.Cron = in.Cron
		row.ExtraOption = in.ExtraOption
		if in.UID != 0 {
			uid := in.UID
			row.UID = &uid
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		out = &row
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: create target: %w", err)
	}
	return out, nil
}

// UpdateTarget writes the fields a write supplies.
//
// ONLY THE MENTIONED FIELDS, which is why the write's optionals are pointers: a
// write that mentions only the mapping must not clear the agent it is bound to.
//
// A CHANGE TO THE BINDING CLEARS THE HANDOVER LOG in the same transaction. Those
// rows say "this agent collects this target", and after the binding changes that
// is no longer what the target means — leaving them would show an agent
// collecting a target it will never be handed again.
func (r *Repo) UpdateTarget(ctx context.Context, id model.ID, in discoverybiz.TargetWrite) (*discoverymodel.Target, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row discoverymodel.Target
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %d", discoverybiz.ErrTargetNotFound, id)
			}
			return err
		}

		updates := map[string]any{}
		if in.TypeID != 0 {
			updates["type_id"] = in.TypeID
		}
		if in.AdrID != nil {
			updates["adr_id"] = *in.AdrID
		}
		if in.SetAttributes {
			updates["attributes"] = in.Attributes
		}
		if in.AutoAccept != nil {
			updates["auto_accept"] = *in.AutoAccept
		}
		if in.AgentID != nil {
			updates["agent_id"] = *in.AgentID
		}
		if in.QueryExpr != nil {
			updates["query_expr"] = *in.QueryExpr
		}
		if in.Interval != nil {
			updates["interval"] = *in.Interval
		}
		if in.Cron != nil {
			updates["cron"] = *in.Cron
		}
		if in.SetExtraOption {
			updates["extra_option"] = in.ExtraOption
		}
		if in.Enabled != nil {
			updates["enabled"] = *in.Enabled
		}
		if in.UID != 0 {
			updates["uid"] = in.UID
		}
		if len(updates) > 0 {
			if err := tx.Model(&discoverymodel.Target{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				return err
			}
		}

		if bindingChanged(row, in) {
			if err := tx.Where("adt_id = ?", id).Delete(&discoverymodel.RuleSyncHistory{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: update target: %w", err)
	}
	return r.LoadTarget(ctx, id)
}

// bindingChanged reports whether a write moves a target to a different
// collector.
func bindingChanged(row discoverymodel.Target, in discoverybiz.TargetWrite) bool {
	if in.AgentID != nil && *in.AgentID != derefText(row.AgentID) {
		return true
	}
	if in.QueryExpr != nil && *in.QueryExpr != derefText(row.QueryExpr) {
		return true
	}
	return false
}

// DeleteTarget removes a target and everything that hung off it.
//
// ONE TRANSACTION OVER FOUR THINGS: the target, its handover log, its model's
// relation rows that have become unreachable, and nothing else. cantor committed
// the first, then looped over the relations in a second, so a crash between them
// left relations naming a field no remaining target can produce.
//
// THE RELATIONS ARE THE MODEL'S, NOT THE TARGET'S. cantor keyed them by CI type,
// so two targets of one model share a relation set; what the cascade removes is
// therefore "the fields this model can no longer receive", which is why the
// surviving set is recomputed from the targets that remain rather than from the
// one being deleted.
func (r *Repo) DeleteTarget(ctx context.Context, id model.ID) (*discoverybiz.TargetDeleteResult, error) {
	out := &discoverybiz.TargetDeleteResult{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// TAKE THE TARGET ROW'S LOCK BEFORE COUNTING. A report path locks the
		// same row in UpsertPool, so a report cannot insert its pool row after
		// this count and before the delete; the two operations are serialised
		// on MySQL. SQLite ignores the clause — its write transaction already
		// holds the database.
		row, err := lockTargetRow(tx, id)
		if err != nil {
			return err
		}
		// A TARGET WITH SIGHTINGS IS NOT REMOVED. The pool rows name it as their
		// source, and deleting it would leave them unmappable: a row that can
		// never be accepted and whose reason no longer exists. The count and the
		// delete share this transaction, so a report cannot slip in between.
		var bound int64
		if err := tx.Model(&discoverymodel.PoolEntry{}).Where("adt_id = ?", id).Count(&bound).Error; err != nil {
			return err
		}
		if bound > 0 {
			return fmt.Errorf("%w: %d pool rows", discoverybiz.ErrTargetReferenced, bound)
		}
		if err := tx.Where("adt_id = ?", id).Delete(&discoverymodel.RuleSyncHistory{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&discoverymodel.Target{}, id).Error; err != nil {
			return err
		}
		if row.TypeID == nil {
			return nil
		}

		live, err := liveAdKeysTx(tx, *row.TypeID)
		if err != nil {
			return err
		}
		removed, err := pruneRelationsTx(tx, *row.TypeID, live)
		if err != nil {
			return err
		}
		out.RelationsRemoved = removed
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: delete target: %w", err)
	}
	return out, nil
}

// --- relations ---

// ListTargetRelations returns one model's discovered relations, ordered by id.
func (r *Repo) ListTargetRelations(ctx context.Context, typeID model.ID) ([]discoverymodel.TargetRelation, error) {
	var rows []discoverymodel.TargetRelation
	if err := r.db.WithContext(ctx).Where("ad_type_id = ?", typeID).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: list target relations: %w", err)
	}
	return rows, nil
}

// ReplaceTargetRelations makes a model's relation set exactly the ones given.
func (r *Repo) ReplaceTargetRelations(ctx context.Context, typeID model.ID, in []discoverybiz.TargetRelationWrite) (*discoverybiz.RelationReplaceResult, error) {
	out := &discoverybiz.RelationReplaceResult{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []discoverymodel.TargetRelation
		if err := tx.Where("ad_type_id = ?", typeID).Find(&existing).Error; err != nil {
			return err
		}
		byKey := make(map[string]discoverymodel.TargetRelation, len(existing))
		for _, row := range existing {
			byKey[relationKey(derefText(row.AdKey), row.PeerTypeID, row.PeerAttrID)] = row
		}

		keep := make(map[string]bool, len(in))
		for _, item := range in {
			key := relationKey(item.AdKey, item.PeerTypeID, item.PeerAttrID)
			keep[key] = true
			if _, ok := byKey[key]; ok {
				continue
			}
			adKey := item.AdKey
			row := discoverymodel.TargetRelation{
				AdTypeID:   typeID,
				AdKey:      &adKey,
				PeerTypeID: item.PeerTypeID,
				PeerAttrID: item.PeerAttrID,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			out.Added++
		}
		for _, row := range existing {
			if keep[relationKey(derefText(row.AdKey), row.PeerTypeID, row.PeerAttrID)] {
				continue
			}
			if err := tx.Delete(&discoverymodel.TargetRelation{}, row.ID).Error; err != nil {
				return err
			}
			out.Removed++
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: replace target relations: %w", err)
	}

	rows, err := r.ListTargetRelations(ctx, typeID)
	if err != nil {
		return nil, err
	}
	out.Relations = make([]discoverybiz.TargetRelation, 0, len(rows))
	for _, row := range rows {
		out.Relations = append(out.Relations, discoverybiz.TargetRelation{
			ID: row.ID, AdKey: derefText(row.AdKey),
			PeerTypeID: row.PeerTypeID, PeerAttrID: row.PeerAttrID,
		})
	}
	return out, nil
}

// --- the handover log ---

// ListSyncHistories returns one target's handover log, newest first.
func (r *Repo) ListSyncHistories(ctx context.Context, adtID model.ID, limit, offset int) ([]discoverymodel.RuleSyncHistory, int64, error) {
	base := r.db.WithContext(ctx).Model(&discoverymodel.RuleSyncHistory{}).Where("adt_id = ?", adtID)
	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: count sync histories: %w", err)
	}
	var rows []discoverymodel.RuleSyncHistory
	q := base.Order("id DESC")
	if limit > 0 {
		q = q.Limit(limit).Offset(offset)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: list sync histories: %w", err)
	}
	return rows, total, nil
}

// RecordSyncHistories records one handover batch in one transaction.
//
// THE ROW IS KEYED BY (target, agent) AND UPDATED, not appended: cantor's CRUD
// was an upsert, which is what keeps this table small. It committed the batch in
// a background task after answering, so a failure there was logged and dropped —
// the console then showed an agent that had never collected a target it collects
// every minute. This writes before answering, and a failure is the request's
// failure.
func (r *Repo) RecordSyncHistories(ctx context.Context, in []discoverybiz.SyncHistoryWrite) error {
	if len(in) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, entry := range in {
			var row discoverymodel.RuleSyncHistory
			adtID := entry.AdtID
			agentID := entry.OneagentID
			agentName := entry.OneagentName
			err := tx.Where("adt_id = ? AND oneagent_id = ? AND oneagent_name = ?",
				adtID, agentID, agentName).First(&row).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				fresh := discoverymodel.RuleSyncHistory{
					AdtID:        &adtID,
					OneagentID:   &agentID,
					OneagentName: &agentName,
					SyncAt:       &entry.SyncAt,
				}
				if err := tx.Create(&fresh).Error; err != nil {
					return err
				}
			case err != nil:
				return err
			default:
				if err := tx.Model(&discoverymodel.RuleSyncHistory{}).Where("id = ?", row.ID).
					Update("sync_at", entry.SyncAt).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("cmdb discovery: record sync histories: %w", err)
	}
	return nil
}

// --- the shared helpers ---

// lockTargetRow takes the target row's lock, which serialises a delete against
// a concurrent report's pool insert.
func lockTargetRow(tx *gorm.DB, id model.ID) (*discoverymodel.Target, error) {
	var target discoverymodel.Target
	q := tx.Model(&discoverymodel.Target{}).Where("id = ?", id)
	if tx.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %d", discoverybiz.ErrTargetNotFound, id)
		}
		return nil, err
	}
	return &target, nil
}

// lockRuleRow takes the rule row's lock, which is what serialises a target's
// creation against its rule's deletion.
//
// The row is read and discarded; only the lock matters. SQLite is left alone
// because the clause is meaningless there — a write transaction already holds
// the database — and GORM's SQLite driver would reject the syntax.
func lockRuleRow(tx *gorm.DB, id model.ID) error {
	var rule discoverymodel.Rule
	q := tx.Model(&discoverymodel.Rule{}).Where("id = ?", id)
	if tx.Dialector.Name() != "sqlite" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.First(&rule).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: rule %d", discoverybiz.ErrRuleNotFound, id)
		}
		return err
	}
	return nil
}

// liveAdKeysTx returns every field name the remaining targets of a model can
// collect.
//
// THE RULE IS THE BUSINESS LAYER'S, called here because the cascade has to
// evaluate it inside the transaction: reading the targets, then deciding outside
// it, would let a target created in between be judged against a field set that
// never included it. Same shape as the rack and subnet arithmetic the data-centre
// and address stores call mid-transaction.
func liveAdKeysTx(tx *gorm.DB, typeID model.ID) ([]string, error) {
	var targets []discoverymodel.Target
	if err := tx.Where("type_id = ?", typeID).Find(&targets).Error; err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return []string{}, nil
	}
	var rules []discoverymodel.Rule
	if err := tx.Find(&rules).Error; err != nil {
		return nil, err
	}
	byID := make(map[model.ID]discoverymodel.Rule, len(rules))
	for _, rule := range rules {
		byID[rule.ID] = rule
	}
	out := []string{}
	for _, target := range targets {
		rule, ok := byID[derefModelID(target.AdrID)]
		if !ok {
			continue
		}
		keys, err := discoverybiz.AdKeys(rule, target)
		if err != nil {
			return nil, err
		}
		out = append(out, keys...)
	}
	return out, nil
}

// pruneRelationsTx removes the relations of a model that no remaining target can
// produce, reporting the field names that went.
func pruneRelationsTx(tx *gorm.DB, typeID model.ID, live []string) ([]string, error) {
	keep := make(map[string]bool, len(live))
	for _, key := range live {
		keep[key] = true
	}
	var rows []discoverymodel.TargetRelation
	if err := tx.Where("ad_type_id = ?", typeID).Find(&rows).Error; err != nil {
		return nil, err
	}
	removed := []string{}
	seen := map[string]bool{}
	for _, row := range rows {
		key := derefText(row.AdKey)
		if keep[key] {
			continue
		}
		if err := tx.Delete(&discoverymodel.TargetRelation{}, row.ID).Error; err != nil {
			return nil, err
		}
		if key != "" && !seen[key] {
			seen[key] = true
			removed = append(removed, key)
		}
	}
	sortText(removed)
	return removed, nil
}

// derefText reads a nullable text column.
func derefText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefModelID reads a nullable numeric column.
func derefModelID(id *model.ID) model.ID {
	if id == nil {
		return 0
	}
	return *id
}

// relationKey is a relation's identity, which is the whole triple.
func relationKey(adKey string, peerTypeID, peerAttrID model.ID) string {
	return fmt.Sprintf("%s\x00%d\x00%d", adKey, peerTypeID, peerAttrID)
}

// sortText orders a small slice of names in place.
func sortText(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}
