package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/ongridio/ongrid/internal/manager/biz/alert/investigator"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// investigationSessions follows actual delegation edges, not a caller-supplied
// root_session_id. Owner mismatches and incomplete trees fail closed.
func (r *SessionRepo) investigationSessions(ctx context.Context, root string) ([]string, uint64, bool, error) {
	var parent model.Session
	if err := r.db.WithContext(ctx).Select("id", "user_id").Where("id = ?", root).First(&parent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, false, errs.ErrNotFound
		}
		return nil, 0, false, err
	}
	ids, frontier := []string{root}, []string{root}
	seen := map[string]bool{root: true}
	for len(frontier) > 0 {
		var children []model.Session
		remaining := investigator.MaxEvidenceSessions - len(ids)
		if err := r.db.WithContext(ctx).Select("id", "user_id").
			Where("parent_session_id IN ?", frontier).
			Order("created_at ASC, id ASC").Limit(remaining + 1).Find(&children).Error; err != nil {
			return nil, 0, false, err
		}
		if len(children) > remaining {
			return ids, parent.UserID, true, nil
		}
		frontier = nil
		for _, child := range children {
			if seen[child.ID] || child.UserID != parent.UserID {
				return ids, parent.UserID, true, nil
			}
			seen[child.ID] = true
			ids = append(ids, child.ID)
			frontier = append(frontier, child.ID)
		}
	}
	return ids, parent.UserID, false, nil
}

func (r *SessionRepo) investigationCalls(ctx context.Context, ids []string, before time.Time) *gorm.DB {
	// SUBSTR bounds the SQL read; BoundContent omits oversized fields before redaction.
	// SQL character limits alone do not bound multibyte result payloads.
	byteLengths := "LENGTH(tc.arguments_json) AS arguments_bytes, LENGTH(tc.result_json) AS result_bytes"
	if r.db.Dialector.Name() == "sqlite" {
		byteLengths = "LENGTH(CAST(tc.arguments_json AS BLOB)) AS arguments_bytes, LENGTH(CAST(tc.result_json AS BLOB)) AS result_bytes"
	}
	return r.db.WithContext(ctx).Table("chat_tool_calls AS tc").
		Select(`tc.id, tc.message_id, tc.tool_name, tc.status, SUBSTR(tc.error, 1, 1024) AS error,
			tc.started_at, tc.ended_at, tc.device_id, tc.created_at,
			SUBSTR(tc.arguments_json, 1, ?) AS arguments_json,
			SUBSTR(tc.result_json, 1, ?) AS result_json,
			`+byteLengths+`,
			m.session_id`, investigator.MaxEvidenceResultBytes, investigator.MaxEvidenceResultBytes).
		Joins("JOIN chat_messages AS m ON m.id = tc.message_id").
		Where("m.session_id IN ? AND tc.created_at <= ?", ids, before)
}

func (r *SessionRepo) ListInvestigationCalls(ctx context.Context, root string, before time.Time) (investigator.EvidenceRecords, error) {
	ids, owner, incomplete, err := r.investigationSessions(ctx, root)
	if err != nil {
		return investigator.EvidenceRecords{}, err
	}
	out := investigator.EvidenceRecords{OwnerID: owner, Incomplete: incomplete}
	if err := r.investigationCalls(ctx, ids, before).Order("tc.created_at ASC, tc.id ASC").
		Limit(investigator.MaxEvidenceCalls + 1).Scan(&out.Calls).Error; err != nil {
		return investigator.EvidenceRecords{}, err
	}
	if len(out.Calls) > investigator.MaxEvidenceCalls {
		out.Incomplete = true
		out.Calls = out.Calls[:investigator.MaxEvidenceCalls]
	}
	for i := range out.Calls {
		out.Calls[i].BoundContent()
	}
	return out, nil
}

func (r *SessionRepo) GetInvestigationCall(ctx context.Context, root, callID string, before time.Time) (investigator.EvidenceCall, uint64, error) {
	ids, owner, _, err := r.investigationSessions(ctx, root)
	if err != nil {
		return investigator.EvidenceCall{}, 0, err
	}
	var rows []investigator.EvidenceCall
	if err := r.investigationCalls(ctx, ids, before).Where("tc.id = ?", callID).Limit(1).Scan(&rows).Error; err != nil {
		return investigator.EvidenceCall{}, 0, err
	}
	if len(rows) == 0 {
		return investigator.EvidenceCall{}, 0, errs.ErrNotFound
	}
	rows[0].BoundContent()
	return rows[0], owner, nil
}
