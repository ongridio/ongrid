package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	cibiz "github.com/ongridio/ongrid/internal/cmdb/biz/ci"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// The scan configuration and the two IPAM logs.
//
// THE SCAN RULE IS CONFIGURATION, the two others are records. That is why the
// rule is soft-deleted and the records are not — see the models.

// SubnetScans returns the scan rules of the given subnets.
func (r *Repo) SubnetScans(ctx context.Context, subnetIDs []model.ID) (map[model.ID]cibiz.ScanRule, error) {
	out := make(map[model.ID]cibiz.ScanRule, len(subnetIDs))
	if len(subnetIDs) == 0 {
		return out, nil
	}
	var rows []cimodel.IPAMSubnetScan
	if err := r.db.WithContext(ctx).
		Where("ci_id IN ? AND delete_marker = 0", subnetIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load scan rules: %w", err)
	}
	for _, row := range rows {
		out[row.CIID] = cibiz.ScanRule{
			SubnetID:      row.CIID,
			ScanEnabled:   row.ScanEnabled,
			AgentID:       derefString(row.AgentID),
			Cron:          derefString(row.Cron),
			RuleUpdatedAt: row.RuleUpdatedAt,
			LastScanTime:  row.LastScanTime,
		}
	}
	return out, nil
}

// SaveSubnetScan creates or replaces a subnet's scan rule.
//
// ONE ROW PER SUBNET, which is the shape of the table and the reason the write
// looks the rule up first rather than inserting unconditionally: two rules for
// one subnet would make "which cron wins" a question about insertion order.
func (r *Repo) SaveSubnetScan(ctx context.Context, in cibiz.ScanRuleSpec) error {
	if in.SubnetID == 0 {
		return errors.New("cmdb ci store: save scan rule: no subnet")
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row cimodel.IPAMSubnetScan
		findErr := tx.Where("ci_id = ? AND delete_marker = 0", in.SubnetID).First(&row).Error
		switch {
		case findErr == nil:
			updates := map[string]any{
				"scan_enabled": in.ScanEnabled,
				"agent_id":     nullableString(in.AgentID),
				"cron":         nullableString(in.Cron),
			}
			if in.TouchUpdatedAt {
				updates["rule_updated_at"] = time.Now()
			}
			return tx.Model(&cimodel.IPAMSubnetScan{}).Where("id = ?", row.ID).Updates(updates).Error
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			created := cimodel.IPAMSubnetScan{
				CIID: in.SubnetID, ScanEnabled: in.ScanEnabled,
				AgentID: nullableString(in.AgentID), Cron: nullableString(in.Cron),
			}
			if in.TouchUpdatedAt {
				now := time.Now()
				created.RuleUpdatedAt = &now
			}
			return tx.Create(&created).Error
		default:
			return fmt.Errorf("load scan rule: %w", findErr)
		}
	})
	if err != nil {
		return fmt.Errorf("cmdb ci: save scan rule: %w", err)
	}
	return nil
}

// DeleteSubnetScan removes a subnet's scan rule if it has one.
func (r *Repo) DeleteSubnetScan(ctx context.Context, subnetID model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Where("ci_id = ?", subnetID).Delete(&cimodel.IPAMSubnetScan{}).Error
	})
	if err != nil {
		return fmt.Errorf("cmdb ci: delete scan rule: %w", err)
	}
	return nil
}

// InsertScanHistory records one scan's outcome.
func (r *Repo) InsertScanHistory(ctx context.Context, in cibiz.ScanHistorySpec) error {
	row := cimodel.IPAMSubnetScanHistory{
		ExecID: nullableString(in.ExecID),
		CIDR:   nullableString(in.CIDR),
		Status: in.Status,
		Stdout: nullableString(in.Stdout),
		IPs:    in.IPs,
	}
	if in.ScanRuleID != 0 {
		row.SubnetScanID = &in.ScanRuleID
	}
	if in.IPNum > 0 {
		n := model.ID(in.IPNum)
		row.IPNum = &n
	}
	row.StartAt, row.EndAt = in.StartAt, in.EndAt
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("cmdb ci: record scan: %w", err)
	}
	return nil
}

// ScanHistories lists recorded scans, newest first.
func (r *Repo) ScanHistories(ctx context.Context, f cibiz.ScanHistoryFilter) ([]cimodel.IPAMSubnetScanHistory, int64, error) {
	q := r.db.WithContext(ctx).Model(&cimodel.IPAMSubnetScanHistory{})
	if f.CIDR != "" {
		q = q.Where("cidr = ?", f.CIDR)
	}
	if f.ExecID != "" {
		q = q.Where("exec_id = ?", f.ExecID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: count scan history: %w", err)
	}
	var rows []cimodel.IPAMSubnetScanHistory
	if err := q.Order("id DESC").Limit(f.Limit).Offset(f.Offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: scan history: %w", err)
	}
	return rows, total, nil
}

// IPAMHistory lists recorded operations, newest first.
func (r *Repo) IPAMHistory(ctx context.Context, f cibiz.IPAMHistoryFilter) ([]cimodel.IPAMOperationHistory, int64, error) {
	q := r.db.WithContext(ctx).Model(&cimodel.IPAMOperationHistory{})
	if f.CIDR != "" {
		q = q.Where("cidr = ?", f.CIDR)
	}
	if len(f.OperateTypes) > 0 {
		q = q.Where("operate_type IN ?", f.OperateTypes)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: count ipam history: %w", err)
	}
	var rows []cimodel.IPAMOperationHistory
	if err := q.Order("id DESC").Limit(f.Limit).Offset(f.Offset).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb ci: ipam history: %w", err)
	}
	return rows, total, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- assigning and releasing addresses ---

// ApplyAddressChange writes one batch of address assignments or releases.
//
// ONE TRANSACTION FOR THE WHOLE BATCH, which cantor's was not: its `assign_ips`
// took a per-subnet Redis lock and then committed once per address, once for
// the "clear the other addresses" pass, and once more for the subnet's
// counters. A crash between any two left the counters describing a subnet that
// no longer existed — and the counters are what the page's usage bar draws, so
// the wrong number was the only thing on screen.
func (r *Repo) ApplyAddressChange(ctx context.Context, change cibiz.AddressChange) (*cibiz.AddressChangeResult, error) {
	var result cibiz.AddressChangeResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = applyAddressChangeTx(ctx, tx, change)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: address change: %w", err)
	}
	return &result, nil
}

func applyAddressChangeTx(ctx context.Context, tx *gorm.DB, change cibiz.AddressChange) (cibiz.AddressChangeResult, error) {
	var result cibiz.AddressChangeResult

	if err := lockCI(tx, change.SubnetID); err != nil {
		return result, err
	}

	// THE RANGE IS CHECKED BEFORE ANYTHING IS WRITTEN. cantor never compared
	// the address to the subnet's range: its lookup was "does this subnet
	// already have an address with this string", and an address it had never
	// seen was created wherever the caller pointed. A subnet could hold
	// addresses from another network entirely.
	if change.CIDR != "" {
		rng, err := cibiz.RangeOf(change.CIDR)
		if err != nil {
			return result, err
		}
		for _, ip := range change.IPs {
			if !rng.ContainsAddress(ip) {
				return result, fmt.Errorf("%w: %s is not in %s",
					cibiz.ErrAddressOutsideSubnet, ip, rng.String())
			}
		}
	}

	// The addresses this subnet already holds, by address string.
	existing, err := subnetAddressesTx(ctx, tx, change)
	if err != nil {
		return result, err
	}

	result.AddressIDs = make([]model.ID, 0, len(change.IPs))
	for _, ip := range change.IPs {
		values := map[model.ID]any{}
		for id, v := range change.Attributes {
			values[id] = v
		}
		if change.Attrs.IP != 0 {
			values[change.Attrs.IP] = ip
		}
		if change.Attrs.Label != 0 {
			values[change.Attrs.Label] = ip
		}
		if change.SetStatus && change.Attrs.AssignStatus != 0 {
			values[change.Attrs.AssignStatus] = int32(change.Status)
		}
		if change.SetIsUsed && change.Attrs.IsUsed != 0 {
			values[change.Attrs.IsUsed] = boolInt(change.IsUsed)
		}

		if id, ok := existing[ip]; ok {
			if _, err := applyValues(tx, id, change.SubnetTypeID, change.AddressSpecs, values, change.Audit); err != nil {
				return result, err
			}
			result.AddressIDs = append(result.AddressIDs, id)
			continue
		}

		row := &cimodel.CI{TypeID: change.SubnetTypeID}
		if err := insertCIWithValues(tx, row, change.AddressSpecs, values, change.Audit); err != nil {
			return result, err
		}
		if err := relateToSubnetTx(tx, change, row.ID); err != nil {
			return result, err
		}
		existing[ip] = row.ID
		result.AddressIDs = append(result.AddressIDs, row.ID)
		result.Created++
	}

	// The addresses that were in use and are not among the ones just written
	// are no longer in use: a scanner reports the addresses it SAW, so a batch
	// that names a new set means the ones it left out were not seen.
	if change.SetIsUsed && change.IsUsed && change.Attrs.IsUsed != 0 {
		keep := map[model.ID]bool{}
		for _, id := range result.AddressIDs {
			keep[id] = true
		}
		for _, id := range existing {
			if keep[id] || id == 0 {
				continue
			}
			if _, err := applyValues(tx, id, change.SubnetTypeID, change.AddressSpecs,
				map[model.ID]any{change.Attrs.IsUsed: int32(0)}, change.Audit); err != nil {
				return result, err
			}
		}
	}

	counters, err := recountSubnetTx(ctx, tx, change)
	if err != nil {
		return result, err
	}
	result.AssignCount, result.UsedCount, result.FreeCount = counters.Assign, counters.Used, counters.Free

	for _, entry := range change.History {
		row := cimodel.IPAMOperationHistory{
			CIDR:        nullableString(entry.CIDR),
			OperateType: nullableString(entry.Operate),
			Description: nullableString(entry.Description),
		}
		if change.Audit.Actor != 0 {
			uid := change.Audit.Actor
			row.UID = &uid
		}
		if err := tx.Create(&row).Error; err != nil {
			return result, fmt.Errorf("write operation history: %w", err)
		}
	}
	return result, nil
}

// relatedAddressTypeID is the model of the instances a scan change writes.
//
// IT IS THE CALLER'S, carried on the change: an address is not a subnet, and
// the store learns neither from the request.
func relatedAddressTypeID(change cibiz.AddressChange) model.ID { return change.SubnetTypeID }

// subnetAddressesTx returns the subnet's addresses, keyed by address string.
func subnetAddressesTx(ctx context.Context, tx *gorm.DB, change cibiz.AddressChange) (map[string]model.ID, error) {
	out := map[string]model.ID{}
	if change.Attrs.IP == 0 {
		return out, nil
	}
	var rows []struct {
		ID model.ID `gorm:"column:id"`
		IP string   `gorm:"column:ip"`
	}
	// The addresses are the subnet's CHILDREN, which is what makes them its
	// addresses: an instance of the address model that belongs to another subnet
	// is not this subnet's to write.
	if err := tx.WithContext(ctx).Model(&cimodel.CI{}).
		Select("ci.id AS id, v.value AS ip").
		Table("c_cis AS ci").
		Joins("JOIN c_ci_relations cr ON cr.second_ci_id = ci.id AND cr.delete_marker = 0").
		Joins("JOIN c_value_index_texts v ON v.ci_id = ci.id AND v.attr_id = ?", change.Attrs.IP).
		Where("ci.delete_marker = 0 AND cr.first_ci_id = ?", change.SubnetID).
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load subnet addresses: %w", err)
	}
	for _, row := range rows {
		out[row.IP] = row.ID
	}
	return out, nil
}

// relateToSubnetTx adds the containment edge for a new address.
func relateToSubnetTx(tx *gorm.DB, change cibiz.AddressChange, addressID model.ID) error {
	rule := cibiz.RelationAddSpec{
		FirstCIID:  change.SubnetID,
		SecondCIID: addressID,
		// The model pair's rule is the caller's; a deployment that has not
		// declared one leaves it zero, and the write then applies no cardinality
		// constraint rather than a guessed one.
		FirstTypeID:       change.SubnetTypeID,
		EnforceConstraint: false,
		Actor:             change.Audit.Actor,
	}
	_, err := addRelationTx(tx, rule)
	return err
}

// recountSubnetTx recomputes a subnet's three maintained numbers from its
// addresses and writes them.
func recountSubnetTx(ctx context.Context, tx *gorm.DB, change cibiz.AddressChange) (cibiz.AddressCounters, error) {
	states, err := subnetAddressStatesTx(ctx, tx, change)
	if err != nil {
		return cibiz.AddressCounters{}, err
	}
	counters := cibiz.CountAddresses(change.Hosts, states)

	values := map[model.ID]any{}
	if change.Attrs.AssignCount != 0 {
		values[change.Attrs.AssignCount] = counters.Assign
	}
	if change.Attrs.UsedCount != 0 {
		values[change.Attrs.UsedCount] = counters.Used
	}
	if change.Attrs.FreeCount != 0 {
		values[change.Attrs.FreeCount] = counters.Free
	}
	if len(values) == 0 {
		return counters, nil
	}
	_, err = applyValues(tx, change.SubnetID, change.SubnetTypeID, change.SubnetSpecs, values, change.Audit)
	return counters, err
}

// subnetAddressStatesTx reads the subnet's addresses as the counter rule wants
// them.
func subnetAddressStatesTx(ctx context.Context, tx *gorm.DB, change cibiz.AddressChange) ([]cibiz.AddressState, error) {
	if change.Attrs.IP == 0 {
		return nil, nil
	}
	var ids []model.ID
	if err := tx.WithContext(ctx).Model(&cimodel.CI{}).
		Select("ci.id").
		Table("c_cis AS ci").
		Joins("JOIN c_ci_relations cr ON cr.second_ci_id = ci.id AND cr.delete_marker = 0").
		Where("ci.delete_marker = 0 AND cr.first_ci_id = ?", change.SubnetID).
		Pluck("ci.id", &ids).Error; err != nil {
		return nil, fmt.Errorf("cmdb ci: load subnet addresses: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	statuses, err := valuesForAttribute(ctx, tx, change.Attrs.AssignStatus, ids)
	if err != nil {
		return nil, err
	}
	useds, err := valuesForAttribute(ctx, tx, change.Attrs.IsUsed, ids)
	if err != nil {
		return nil, err
	}

	out := make([]cibiz.AddressState, 0, len(ids))
	for _, id := range ids {
		state := cibiz.AddressState{}
		if n, ok := lastInt(statuses[id]); ok {
			state.Status = cibiz.AddressStatus(n)
			state.Known = state.Status.Valid()
		}
		if n, ok := lastInt(useds[id]); ok {
			// cantor's `is_used` is a BOOL stored in the integer table, so any
			// non-zero reading is "in use".
			state.Used = n != 0
		}
		out = append(out, state)
	}
	return out, nil
}

// boolInt renders a boolean the way the integer value table stores one.
func boolInt(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

// --- subnets ---

// ApplySubnetWrite creates or updates a subnet, checking its range in the same
// transaction.
//
// THE CHECK IS THE REASON THIS IS ONE CALL. A range claims part of the address
// space, so two callers adding sibling ranges concurrently must not both pass
// the same overlap test — cantor checked against the database and wrote in a
// later commit, which is exactly that race. The subnet model's row is locked
// first, so range claims against one model serialise.
func (r *Repo) ApplySubnetWrite(ctx context.Context, in cibiz.SubnetWrite) (*cibiz.WriteResult, error) {
	var result cibiz.WriteResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTypeRow(tx, in.SubnetTypeID); err != nil {
			return err
		}
		if err := validateSubnetRangeTx(ctx, tx, in); err != nil {
			return err
		}
		got, err := writeCITx(tx, in.WriteSpec)
		if err != nil {
			return err
		}
		result = got
		if in.Recount && in.AddressAttrs != nil {
			// THE COUNTERS FOLLOW THE RANGE. They are measured against the new
			// capacity and counted from the addresses that are actually there,
			// in the same transaction as the change — see SubnetWrite.Recount.
			if _, err := recountSubnetTx(ctx, tx, cibiz.AddressChange{
				SubnetID:     got.CIID,
				SubnetTypeID: in.SubnetTypeID,
				Hosts:        in.Hosts,
				Attrs:        *in.AddressAttrs,
				SubnetSpecs:  in.WriteSpec.Specs,
			}); err != nil {
				return err
			}
		}
		for _, entry := range in.History {
			row := cimodel.IPAMOperationHistory{
				CIDR:        nullableString(entry.CIDR),
				OperateType: nullableString(entry.Operate),
				Description: nullableString(entry.Description),
			}
			if in.Audit.Actor != 0 {
				uid := in.Audit.Actor
				row.UID = &uid
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("write operation history: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: subnet write: %w", err)
	}
	return &result, nil
}

// validateSubnetRangeTx applies the range rules against the rows in the
// transaction.
func validateSubnetRangeTx(ctx context.Context, tx *gorm.DB, in cibiz.SubnetWrite) error {
	range_, err := cibiz.RangeOf(in.CIDR)
	if err != nil {
		return err
	}

	// The subnet being written is excluded from its own comparison, which is
	// what makes an update that does not change the range a no-op rather than a
	// self-overlap.
	selfID := in.WriteSpec.ValidatingID

	if in.ValidateParentID != 0 {
		parentRange, has, err := subnetRangeTx(ctx, tx, in, in.ValidateParentID)
		if err != nil {
			return err
		}
		if has {
			parent, err := cibiz.RangeOf(parentRange)
			if err != nil {
				// The parent's own range is unreadable. A child cannot be
				// checked against it, and refusing says so — cantor aborted
				// with the same message for an unset parent range.
				return fmt.Errorf("%w: %s", cibiz.ErrParentCIDRMissing, parentRange)
			}
			if parent.PrefixLen() >= range_.PrefixLen() {
				return fmt.Errorf("%w: %s is not longer than %s", cibiz.ErrSubnetPrefix, in.CIDR, parentRange)
			}
			if !parent.Contains(range_) {
				return fmt.Errorf("%w: %s is not inside %s", cibiz.ErrSubnetOutsideParent, in.CIDR, parentRange)
			}
		}
		return checkSiblingsTx(ctx, tx, in, selfID, range_)
	}
	return checkRootsTx(ctx, tx, in, selfID, range_)
}

// subnetRangeTx reads a subnet's range.
func subnetRangeTx(ctx context.Context, tx *gorm.DB, in cibiz.SubnetWrite, subnetID model.ID) (string, bool, error) {
	if in.Attrs.CIDR == 0 {
		return "", false, nil
	}
	rows, err := valuesForAttribute(ctx, tx, in.Attrs.CIDR, []model.ID{subnetID})
	if err != nil {
		return "", false, err
	}
	for _, row := range rows[subnetID] {
		if row.Text != "" {
			return row.Text, true, nil
		}
	}
	return "", false, nil
}

// checkSiblingsTx refuses a range that overlaps the other children of its
// parent.
func checkSiblingsTx(ctx context.Context, tx *gorm.DB, in cibiz.SubnetWrite, selfID model.ID, want cibiz.SubnetRange) error {
	var ids []model.ID
	if err := tx.WithContext(ctx).Model(&cimodel.Relation{}).
		Where("delete_marker = 0 AND first_ci_id = ?", in.ValidateParentID).
		Pluck("second_ci_id", &ids).Error; err != nil {
		return fmt.Errorf("load siblings: %w", err)
	}
	return compareRanges(ctx, tx, in, ids, selfID, want)
}

// checkRootsTx refuses a range that overlaps the other subnets with no parent.
func checkRootsTx(ctx context.Context, tx *gorm.DB, in cibiz.SubnetWrite, selfID model.ID, want cibiz.SubnetRange) error {
	// Every subnet that has no parent at all, which is what "root" means here:
	// a subnet inside a scope is not a root even though the scope is not a
	// subnet.
	var ids []model.ID
	if err := tx.WithContext(ctx).Model(&cimodel.CI{}).
		Select("ci.id").
		Table("c_cis AS ci").
		Where("ci.delete_marker = 0 AND ci.type_id = ?", in.SubnetTypeID).
		Where("NOT EXISTS (SELECT 1 FROM c_ci_relations cr WHERE cr.delete_marker = 0 AND cr.second_ci_id = ci.id)").
		Pluck("ci.id", &ids).Error; err != nil {
		return fmt.Errorf("load root subnets: %w", err)
	}
	return compareRanges(ctx, tx, in, ids, selfID, want)
}

// compareRanges refuses an overlap with any of the given subnets.
func compareRanges(ctx context.Context, tx *gorm.DB, in cibiz.SubnetWrite, ids []model.ID, selfID model.ID, want cibiz.SubnetRange) error {
	others := make([]model.ID, 0, len(ids))
	for _, id := range ids {
		if id != selfID {
			others = append(others, id)
		}
	}
	if len(others) == 0 || in.Attrs.CIDR == 0 {
		return nil
	}
	ranges, err := valuesForAttribute(ctx, tx, in.Attrs.CIDR, others)
	if err != nil {
		return err
	}
	for _, id := range others {
		for _, row := range ranges[id] {
			if row.Text == "" {
				continue
			}
			got, err := cibiz.RangeOf(row.Text)
			if err != nil {
				// A row whose range does not parse cannot be compared. Skipping
				// it lets the write through rather than blocking a database on
				// one bad row, and the row is reported by the reachability
				// check.
				continue
			}
			if got.Overlaps(want) {
				return fmt.Errorf("%w: %s and %s", cibiz.ErrSubnetOverlap, want.String(), row.Text)
			}
		}
	}
	return nil
}

// CheckSubnetRange applies the range rules without writing.
func (r *Repo) CheckSubnetRange(ctx context.Context, in cibiz.SubnetWrite) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return validateSubnetRangeTx(ctx, tx, in)
	})
	if err != nil {
		return fmt.Errorf("cmdb ci: check subnet range: %w", err)
	}
	return nil
}

// MoveSubnet re-parents a subnet in one transaction.
//
// THE OLD EDGE AND THE NEW ONE AND THE RECORD ARE ONE CHANGE. cantor deleted
// the old relation, added the new one and wrote the log entry as three commits
// under a Redis lock, so a failure between them left a subnet with two parents
// or none — and a subnet with no parent is a ROOT, which changes which ranges it
// is validated against the next time anything is written.
func (r *Repo) MoveSubnet(ctx context.Context, in cibiz.SubnetMove) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockCI(tx, in.SubnetID); err != nil {
			return err
		}
		// The current container, which is the edge to remove. A subnet whose
		// parent is not of a kind this module knows is left alone rather than
		// having an unrelated edge removed.
		var current []cimodel.Relation
		if err := tx.WithContext(ctx).
			Where("delete_marker = 0 AND second_ci_id = ?", in.SubnetID).
			Find(&current).Error; err != nil {
			return fmt.Errorf("load the subnet's container: %w", err)
		}
		for _, edge := range current {
			var parent cimodel.CI
			if err := tx.First(&parent, edge.FirstCIID).Error; err != nil {
				continue
			}
			if parent.TypeID != in.SubnetTypeID && (in.ScopeTypeID == 0 || parent.TypeID != in.ScopeTypeID) {
				continue
			}
			row := edge
			if err := tx.Delete(&row).Error; err != nil {
				return fmt.Errorf("remove the old container: %w", err)
			}
		}

		if in.TargetParentID != 0 {
			_, err := addRelationTx(tx, cibiz.RelationAddSpec{
				FirstCIID: in.TargetParentID, SecondCIID: in.SubnetID,
				EnforceConstraint: false,
				Actor:             in.Audit.Actor,
			})
			if err != nil {
				return err
			}
		}

		row := cimodel.IPAMOperationHistory{
			CIDR:        nullableString(in.History.CIDR),
			OperateType: nullableString(in.History.Operate),
			Description: nullableString(in.History.Description),
		}
		if in.Audit.Actor != 0 {
			uid := in.Audit.Actor
			row.UID = &uid
		}
		return tx.Create(&row).Error
	})
	if err != nil {
		return fmt.Errorf("cmdb ci: move subnet: %w", err)
	}
	return nil
}

// RecordIPAMOperation writes one operation record on its own.
func (r *Repo) RecordIPAMOperation(ctx context.Context, entry cibiz.IPAMHistoryEntry, audit cibiz.AuditInfo) error {
	row := cimodel.IPAMOperationHistory{
		CIDR:        nullableString(entry.CIDR),
		OperateType: nullableString(entry.Operate),
		Description: nullableString(entry.Description),
	}
	if audit.Actor != 0 {
		uid := audit.Actor
		row.UID = &uid
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("cmdb ci: record ipam operation: %w", err)
	}
	return nil
}
