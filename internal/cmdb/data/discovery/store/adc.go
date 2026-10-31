package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"gorm.io/gorm"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// The discovery pool's persistence.
//
// THE POOL MERGES BY `(type_id, unique_value)`, which is NOT a unique index:
// cantor's adoption left duplicate pairs behind, and a unique index would make
// the table impossible to adopt. The merge therefore looks first and writes
// second; two simultaneous reports of the same pair can still both insert, and
// the next report merges whichever one the lookup finds first. That is cantor's
// behaviour, and it is why the composite index exists as a lookup index rather
// than as a constraint.

// ListPool returns one page of a model's pool, newest first, accepted first.
func (r *Repo) ListPool(ctx context.Context, f discoverybiz.PoolFilter) ([]discoverymodel.PoolEntry, int64, error) {
	page, size := f.Page, f.PageSize
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = discoverybiz.DefaultPoolPageSize
	}
	if size > discoverybiz.MaxPoolPageSize {
		size = discoverybiz.MaxPoolPageSize
	}

	q := r.db.WithContext(ctx).Model(&discoverymodel.PoolEntry{}).Where("type_id = ?", f.TypeID)
	if f.IsAccept != nil {
		q = q.Where("is_accept = ?", *f.IsAccept)
	}
	if f.UniqueValue != "" {
		q = q.Where("unique_value = ?", f.UniqueValue)
	}
	if f.OneagentID != "" {
		q = q.Where("oneagent_id = ?", f.OneagentID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: count pool: %w", err)
	}
	var rows []discoverymodel.PoolEntry
	if err := q.Order("is_accept DESC").Order("id DESC").
		Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: list pool: %w", err)
	}
	return rows, total, nil
}

// LoadPool returns one row, or nil.
func (r *Repo) LoadPool(ctx context.Context, id model.ID) (*discoverymodel.PoolEntry, error) {
	var row discoverymodel.PoolEntry
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb discovery: load pool entry: %w", err)
	}
}

// UpsertPool merges one report.
//
// AN UNCHANGED REPORT STILL TOUCHES THE ROW, to record which agent last saw it.
// Acceptance survives that; only a CHANGE clears it, because what a person
// accepted is no longer what the provider says.
func (r *Repo) UpsertPool(ctx context.Context, in discoverybiz.PoolWrite) (*discoverybiz.PoolUpsertResult, error) {
	var out discoverybiz.PoolUpsertResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// THE TARGET ROW IS THE SERIALISATION POINT for a report and a target
		// delete. Without this lock the delete's count can miss a report that
		// commits its pool row after the count, leaving a dangling adt_id.
		if _, err := lockTargetRow(tx, in.AdtID); err != nil {
			return err
		}
		var existing discoverymodel.PoolEntry
		err := tx.Where("type_id = ? AND unique_value = ?", in.TypeID, in.UniqueValue).
			Order("id").First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := discoverymodel.PoolEntry{
				TypeID: ptrID(in.TypeID), AdtID: ptrID(in.AdtID),
				UniqueValue: ptrText(in.UniqueValue), Instance: in.Instance,
				OneagentID: ptrText(in.OneagentID), OneagentName: ptrText(in.OneagentName),
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("cmdb discovery: create pool entry: %w", err)
			}
			out = discoverybiz.PoolUpsertResult{Row: &row, Created: true, Changed: true}
			return nil
		case err != nil:
			return fmt.Errorf("cmdb discovery: find pool entry: %w", err)
		}

		changed := !sameInstance(existing.Instance, in.Instance)
		updates := map[string]any{
			"adt_id":        in.AdtID,
			"oneagent_id":   in.OneagentID,
			"oneagent_name": in.OneagentName,
			"updated_at":    time.Now(),
		}
		if changed {
			// A MAP UPDATE BYPASSES THE FIELD'S JSON SERIALIZER, so the merged
			// report is encoded here. Handing GORM the map itself fails at the
			// driver with "unsupported type map[string]interface {}".
			merged, err := json.Marshal(mergeInstance(existing.Instance, in.Instance))
			if err != nil {
				return fmt.Errorf("cmdb discovery: encode merged instance: %w", err)
			}
			updates["instance"] = string(merged)
			// A CHANGED SIGHTING LOSES ITS ACCEPTANCE and its instance link.
			// The link is kept out: the row may become a different machine
			// after a mapping change, and pointing at the old instance would be
			// a lie the next accept would correct only if someone noticed.
			updates["is_accept"] = false
			updates["accept_by"] = nil
			updates["accept_time"] = nil
			updates["ci_id"] = nil
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return fmt.Errorf("cmdb discovery: update pool entry: %w", err)
		}
		var reloaded discoverymodel.PoolEntry
		if err := tx.First(&reloaded, existing.ID).Error; err != nil {
			return fmt.Errorf("cmdb discovery: reload pool entry: %w", err)
		}
		out = discoverybiz.PoolUpsertResult{Row: &reloaded, Changed: changed}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// sameInstance compares two reports as JSON.
//
// THE COMPARISON IS BY ENCODING, not by reflect.DeepEqual: a value that came
// from a request decoder may be a json.Number while the same value read back
// from the database is a float64, and DeepEqual would call every sync a change
// — resetting every acceptance on every agent run.
func sameInstance(a, b map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	left, err := json.Marshal(a)
	if err != nil {
		return reflect.DeepEqual(a, b)
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

// mergeInstance is cantor's `instance.update`: the new report wins for the
// fields it mentions, and everything else survives.
func mergeInstance(old, next map[string]any) map[string]any {
	out := make(map[string]any, len(old)+len(next))
	for key, value := range old {
		out[key] = value
	}
	for key, value := range next {
		out[key] = value
	}
	return out
}

// DeletePool removes one sighting, never the instance it became.
func (r *Repo) DeletePool(ctx context.Context, id model.ID) error {
	if err := r.db.WithContext(ctx).Delete(&discoverymodel.PoolEntry{}, id).Error; err != nil {
		return fmt.Errorf("cmdb discovery: delete pool entry: %w", err)
	}
	return nil
}

// MarkPoolAccepted records the instance a row became.
func (r *Repo) MarkPoolAccepted(ctx context.Context, id, ciID model.ID, acceptBy string, at time.Time) error {
	updates := map[string]any{
		"ci_id":       ciID,
		"is_accept":   true,
		"accept_time": at,
		"updated_at":  time.Now(),
	}
	if acceptBy != "" {
		updates["accept_by"] = acceptBy
	}
	if err := r.db.WithContext(ctx).Model(&discoverymodel.PoolEntry{}).
		Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("cmdb discovery: mark pool entry accepted: %w", err)
	}
	return nil
}

// PoolCounters derives the pool's counters from the rows and the model's
// targets.
func (r *Repo) PoolCounters(ctx context.Context, typeID model.ID, now time.Time) (discoverybiz.PoolCounters, error) {
	var out discoverybiz.PoolCounters

	var targets int64
	if err := r.db.WithContext(ctx).Model(&discoverymodel.Target{}).
		Where("type_id = ?", typeID).Count(&targets).Error; err != nil {
		return out, fmt.Errorf("cmdb discovery: count targets: %w", err)
	}
	out.ExecTargetCount = targets

	if err := r.db.WithContext(ctx).Model(&discoverymodel.Target{}).
		Where("type_id = ?", typeID).Distinct("adr_id").Count(&out.RuleCount).Error; err != nil {
		return out, fmt.Errorf("cmdb discovery: count rules: %w", err)
	}

	countPool := func(where string, args ...any) (int64, error) {
		q := r.db.WithContext(ctx).Model(&discoverymodel.PoolEntry{}).Where("type_id = ?", typeID)
		if where != "" {
			q = q.Where(where, args...)
		}
		var n int64
		return n, q.Count(&n).Error
	}

	var err error
	if out.InstanceCount, err = countPool(""); err != nil {
		return out, fmt.Errorf("cmdb discovery: count pool rows: %w", err)
	}
	if out.AcceptCount, err = countPool("is_accept = ?", true); err != nil {
		return out, fmt.Errorf("cmdb discovery: count accepted: %w", err)
	}

	weekStart := startOfWeek(now)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastWeekStart := weekStart.AddDate(0, 0, -7)
	lastMonthStart := monthStart.AddDate(0, -1, 0)

	if out.ThisWeekCount, err = countPool("created_at >= ?", weekStart); err != nil {
		return out, fmt.Errorf("cmdb discovery: count this week: %w", err)
	}
	if out.ThisMonthCount, err = countPool("created_at >= ?", monthStart); err != nil {
		return out, fmt.Errorf("cmdb discovery: count this month: %w", err)
	}
	if out.LastWeekCount, err = countPool("created_at >= ? AND created_at < ?", lastWeekStart, weekStart); err != nil {
		return out, fmt.Errorf("cmdb discovery: count last week: %w", err)
	}
	if out.LastMonthCount, err = countPool("created_at >= ? AND created_at < ?", lastMonthStart, monthStart); err != nil {
		return out, fmt.Errorf("cmdb discovery: count last month: %w", err)
	}
	return out, nil
}

// startOfWeek is Monday 00:00 in the clock's own location.
func startOfWeek(now time.Time) time.Time {
	offset := (int(now.Weekday()) + 6) % 7
	day := now.AddDate(0, 0, -offset)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
}

// --- exec history ---

// ListExecHistories returns one page of a model's log, newest first.
func (r *Repo) ListExecHistories(ctx context.Context, typeID model.ID, page, size int) ([]discoverymodel.ExecHistory, int64, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = discoverybiz.DefaultPoolPageSize
	}
	if size > discoverybiz.MaxPoolPageSize {
		size = discoverybiz.MaxPoolPageSize
	}
	q := r.db.WithContext(ctx).Model(&discoverymodel.ExecHistory{}).Where("type_id = ?", typeID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: count exec histories: %w", err)
	}
	var rows []discoverymodel.ExecHistory
	if err := q.Order("id DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("cmdb discovery: list exec histories: %w", err)
	}
	return rows, total, nil
}

// CreateExecHistory appends one log line.
func (r *Repo) CreateExecHistory(ctx context.Context, typeID model.ID, stdout string) error {
	row := discoverymodel.ExecHistory{TypeID: ptrID(typeID), Stdout: ptrText(stdout)}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("cmdb discovery: create exec history: %w", err)
	}
	return nil
}

// DeleteExecHistories clears one model's log.
func (r *Repo) DeleteExecHistories(ctx context.Context, typeID model.ID) error {
	if err := r.db.WithContext(ctx).Where("type_id = ?", typeID).
		Delete(&discoverymodel.ExecHistory{}).Error; err != nil {
		return fmt.Errorf("cmdb discovery: delete exec histories: %w", err)
	}
	return nil
}

// idp and strp are the nullable-column helpers the module already uses.
func ptrID(v model.ID) *model.ID { return &v }
func ptrText(s string) *string   { return &s }
