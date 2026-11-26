package ci

import (
	"context"
	"fmt"
	"strings"

	historybiz "github.com/ongridio/ongrid/internal/cmdb/biz/history"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// HistoryCINames names instances for the operation-history page.
//
// IT USES THE MODEL'S SHOW ATTRIBUTE, falling back to the identifying one — the
// same rule every other CMDB list uses to title a row. A history row that said
// only `#100` would be unreadable in exactly the place a person is trying to
// work out what changed.
//
// THE VALUES ARE READ IN ONE QUERY PER MODEL, not one per instance: a page of
// history routinely names twenty instances, and the model graph — which
// attribute titles a row — is the same for all of them.
type HistoryCINames struct {
	uc *Usecase
}

// NewHistoryCINames builds the adapter.
func NewHistoryCINames(uc *Usecase) *HistoryCINames { return &HistoryCINames{uc: uc} }

var _ historybiz.CINameResolver = (*HistoryCINames)(nil)

// historyNameAttribute picks the attribute that titles an instance in history.
//
// THE NORMAL READ PATH HIDES PASSWORDS, LINKS AND FILES — see
// renderInstance — and history is readable by any authenticated caller, so a
// model that marks its show attribute as a password must fall back to the
// unique attribute. When both are sensitive the caller gets no name and the
// console prints the id, which is ugly but safe.
func historyNameAttribute(show, unique AttributeSpec) AttributeSpec {
	if show.ID != 0 && show.Modifiers().IsPasswordSafe() {
		return show
	}
	if unique.ID != 0 && unique.Modifiers().IsPasswordSafe() {
		return unique
	}
	return AttributeSpec{}
}

// ResolveCINames returns the display name of each instance it can find.
func (r *HistoryCINames) ResolveCINames(ctx context.Context, ids []model.ID) (map[model.ID]string, error) {
	out := map[model.ID]string{}
	if r == nil || r.uc == nil || len(ids) == 0 {
		return out, nil
	}
	loaded, err := r.uc.repo.LoadCIs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("cmdb ci: resolve history instances: %w", err)
	}
	byType := map[model.ID][]model.ID{}
	for _, id := range ids {
		if id == 0 {
			continue
		}
		ci := loaded[id]
		if ci == nil {
			continue
		}
		byType[ci.TypeID] = append(byType[ci.TypeID], id)
	}

	for typeID, ciIDs := range byType {
		set, err := r.uc.types.ResolveForCI(ctx, fmt.Sprint(typeID))
		if err != nil {
			return nil, err
		}
		if set == nil {
			continue
		}
		attr := historyNameAttribute(set.ShowAttr, set.UniqueAttr)
		if attr.ID == 0 {
			continue
		}
		rows, err := r.uc.repo.ValuesForAttribute(ctx, attr.ID, ciIDs)
		if err != nil {
			return nil, fmt.Errorf("cmdb ci: resolve history instance names: %w", err)
		}
		for ciID, list := range rows {
			parts := make([]string, 0, len(list))
			for _, row := range list {
				if text := strings.TrimSpace(rowText(row)); text != "" {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				out[ciID] = strings.Join(parts, ", ")
			}
		}
	}
	return out, nil
}
