package discovery

import (
	"context"
	"errors"
	"strconv"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

// A repository double for the rule and account tests.

type stubRepo struct {
	rules     []discoverymodel.Rule
	accounts  []discoverymodel.Account
	targets   []discoverymodel.Target
	relations []discoverymodel.TargetRelation
	histories []discoverymodel.RuleSyncHistory
	nextID    model.ID

	lastRule       RuleWrite
	lastAccounts   []AccountWrite
	lastTarget     TargetWrite
	lastRelationIn []TargetRelationWrite
	lastSync       []SyncHistoryWrite
	removed        []model.ID
	deletedTarget  model.ID
}

func newStub() *stubRepo { return &stubRepo{nextID: 100} }

func (s *stubRepo) ListRules(context.Context) ([]discoverymodel.Rule, error) { return s.rules, nil }

func (s *stubRepo) LoadRule(_ context.Context, id model.ID) (*discoverymodel.Rule, error) {
	for i := range s.rules {
		if s.rules[i].ID == id {
			return &s.rules[i], nil
		}
	}
	return nil, nil
}

func (s *stubRepo) FindRuleByName(_ context.Context, name string) (*discoverymodel.Rule, error) {
	for i := range s.rules {
		if s.rules[i].Name == name {
			return &s.rules[i], nil
		}
	}
	return nil, nil
}

func (s *stubRepo) CreateRule(_ context.Context, in RuleWrite) (*discoverymodel.Rule, error) {
	s.lastRule = in
	s.nextID++
	// The double mirrors the store: it carries the write's fields into the row,
	// because a double that dropped them would make every "did the write keep
	// this" assertion vacuous.
	row := discoverymodel.Rule{
		Name: in.Name, Type: in.Type, PluginScript: in.PluginScript,
		UniqueKey: in.UniqueKey, Attributes: in.Attributes, Option: in.Option,
	}
	if in.IsPlugin != nil {
		row.IsPlugin = *in.IsPlugin
	}
	if in.IsInner != nil {
		row.IsInner = *in.IsInner
	}
	if in.Owner != 0 {
		row.Owner = idp(in.Owner)
	}
	row.ID = s.nextID
	s.rules = append(s.rules, row)
	return &row, nil
}

func (s *stubRepo) UpdateRule(_ context.Context, id model.ID, in RuleWrite) (*discoverymodel.Rule, error) {
	s.lastRule = in
	row, _ := s.LoadRule(context.Background(), id)
	return row, nil
}

func (s *stubRepo) DeleteRule(context.Context, model.ID) error { return nil }

func (s *stubRepo) UpsertRules(_ context.Context, in []RuleWrite) (int, int, error) {
	return len(in), 0, nil
}

func (s *stubRepo) ListAccounts(context.Context, model.ID) ([]discoverymodel.Account, error) {
	return s.accounts, nil
}

func (s *stubRepo) LoadAccount(_ context.Context, id model.ID) (*discoverymodel.Account, error) {
	for i := range s.accounts {
		if s.accounts[i].ID == id {
			return &s.accounts[i], nil
		}
	}
	return nil, nil
}

func (s *stubRepo) SaveAccount(_ context.Context, in AccountWrite) (*discoverymodel.Account, error) {
	s.lastAccounts = append(s.lastAccounts, in)
	row := discoverymodel.Account{Name: in.Name, Config: in.Config}
	row.ID = 1
	return &row, nil
}

func (s *stubRepo) DeleteAccount(context.Context, model.ID) (bool, error) { return true, nil }

func (s *stubRepo) ReplaceAccountsForRule(_ context.Context, _ model.ID, keep []AccountWrite, owner model.ID) ([]model.ID, error) {
	s.lastAccounts = keep
	return s.removed, nil
}

// --- the target layer's half of the double ---

func (s *stubRepo) ListTargets(_ context.Context, typeID model.ID) ([]discoverymodel.Target, error) {
	out := []discoverymodel.Target{}
	for _, row := range s.targets {
		if row.TypeID != nil && *row.TypeID == typeID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *stubRepo) ListAllTargets(context.Context) ([]discoverymodel.Target, error) {
	return s.targets, nil
}

func (s *stubRepo) LoadTarget(_ context.Context, id model.ID) (*discoverymodel.Target, error) {
	for i := range s.targets {
		if s.targets[i].ID == id {
			return &s.targets[i], nil
		}
	}
	return nil, nil
}

func (s *stubRepo) CreateTarget(_ context.Context, in TargetWrite) (*discoverymodel.Target, error) {
	s.lastTarget = in
	s.nextID++
	row := targetFromWrite(in)
	row.ID = s.nextID
	s.targets = append(s.targets, row)
	return &row, nil
}

func (s *stubRepo) UpdateTarget(_ context.Context, id model.ID, in TargetWrite) (*discoverymodel.Target, error) {
	s.lastTarget = in
	for i := range s.targets {
		if s.targets[i].ID != id {
			continue
		}
		// The double applies only what the write mentions, like the store: a
		// double that wrote every field would make "an omitting update keeps
		// this" pass without the pointer discipline that makes it true.
		if in.TypeID != 0 {
			s.targets[i].TypeID = idp(in.TypeID)
		}
		if in.AdrID != nil {
			s.targets[i].AdrID = idp(*in.AdrID)
		}
		if in.SetAttributes {
			s.targets[i].Attributes = in.Attributes
		}
		if in.AutoAccept != nil {
			s.targets[i].AutoAccept = *in.AutoAccept
		}
		if in.AgentID != nil {
			s.targets[i].AgentID = in.AgentID
		}
		if in.QueryExpr != nil {
			s.targets[i].QueryExpr = in.QueryExpr
		}
		if in.Cron != nil {
			s.targets[i].Cron = in.Cron
		}
		if in.SetExtraOption {
			s.targets[i].ExtraOption = in.ExtraOption
		}
		if in.Enabled != nil {
			s.targets[i].Enabled = *in.Enabled
		}
		return &s.targets[i], nil
	}
	return nil, nil
}

func (s *stubRepo) DeleteTarget(_ context.Context, id model.ID) (*TargetDeleteResult, error) {
	s.deletedTarget = id
	out := &TargetDeleteResult{}
	for i := range s.targets {
		if s.targets[i].ID == id {
			// The double performs the cascade the store performs: relations whose
			// field no remaining target of the model can produce go with it.
			if s.targets[i].TypeID != nil {
				out.RelationsRemoved = s.prune(*s.targets[i].TypeID, id)
			}
			s.targets = append(s.targets[:i], s.targets[i+1:]...)
			break
		}
	}
	return out, nil
}

// prune mirrors the store's cascade, so a test can assert on what it reports.
func (s *stubRepo) prune(typeID, except model.ID) []string {
	live := map[string]bool{}
	for _, target := range s.targets {
		if target.ID == except || target.TypeID == nil || *target.TypeID != typeID {
			continue
		}
		for _, rule := range s.rules {
			if rule.ID != derefID(target.AdrID) {
				continue
			}
			keys, err := AdKeys(rule, target)
			if err != nil {
				continue
			}
			for _, key := range keys {
				live[key] = true
			}
		}
	}
	kept := []discoverymodel.TargetRelation{}
	removed := []string{}
	for _, row := range s.relations {
		if row.AdTypeID != typeID {
			kept = append(kept, row)
			continue
		}
		if live[derefString(row.AdKey)] {
			kept = append(kept, row)
			continue
		}
		removed = append(removed, derefString(row.AdKey))
	}
	s.relations = kept
	sortStrings(removed)
	return removed
}

func (s *stubRepo) ListTargetRelations(_ context.Context, typeID model.ID) ([]discoverymodel.TargetRelation, error) {
	out := []discoverymodel.TargetRelation{}
	for _, row := range s.relations {
		if row.AdTypeID == typeID {
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *stubRepo) ReplaceTargetRelations(_ context.Context, typeID model.ID, in []TargetRelationWrite) (*RelationReplaceResult, error) {
	s.lastRelationIn = in
	keep := map[string]bool{}
	for _, item := range in {
		keep[relationKey(item)] = true
	}
	kept := []discoverymodel.TargetRelation{}
	removed := 0
	for _, row := range s.relations {
		if row.AdTypeID != typeID {
			kept = append(kept, row)
			continue
		}
		if keep[relationKeyOfRow(row)] {
			continue
		}
		removed++
	}
	added := 0
	for _, item := range in {
		exists := false
		for _, row := range kept {
			if row.AdTypeID == typeID && relationKeyOfRow(row) == relationKey(item) {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		adKey := item.AdKey
		row := discoverymodel.TargetRelation{
			AdTypeID: typeID, AdKey: &adKey,
			PeerTypeID: item.PeerTypeID, PeerAttrID: item.PeerAttrID,
		}
		row.ID = s.nextID
		s.nextID++
		kept = append(kept, row)
		added++
	}
	s.relations = kept

	out := &RelationReplaceResult{Added: added, Removed: removed, Relations: []TargetRelation{}}
	for _, row := range kept {
		if row.AdTypeID != typeID {
			continue
		}
		out.Relations = append(out.Relations, TargetRelation{
			ID: row.ID, AdKey: derefString(row.AdKey),
			PeerTypeID: row.PeerTypeID, PeerAttrID: row.PeerAttrID,
		})
	}
	return out, nil
}

func (s *stubRepo) ListSyncHistories(_ context.Context, adtID model.ID, limit, offset int) ([]discoverymodel.RuleSyncHistory, int64, error) {
	out := []discoverymodel.RuleSyncHistory{}
	for i := len(s.histories) - 1; i >= 0; i-- {
		if s.histories[i].AdtID != nil && *s.histories[i].AdtID == adtID {
			out = append(out, s.histories[i])
		}
	}
	total := int64(len(out))
	if limit > 0 && offset < len(out) {
		end := offset + limit
		if end > len(out) {
			end = len(out)
		}
		out = out[offset:end]
	}
	return out, total, nil
}

func (s *stubRepo) RecordSyncHistories(_ context.Context, in []SyncHistoryWrite) error {
	s.lastSync = in
	for _, entry := range in {
		replaced := false
		for i := range s.histories {
			row := &s.histories[i]
			if derefID(row.AdtID) == entry.AdtID &&
				derefString(row.OneagentID) == entry.OneagentID &&
				derefString(row.OneagentName) == entry.OneagentName {
				// The upsert, so a test can prove polling twice leaves one row.
				at := entry.SyncAt
				row.SyncAt = &at
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}
		s.nextID++
		adtID, agentID, agentName := entry.AdtID, entry.OneagentID, entry.OneagentName
		at := entry.SyncAt
		s.histories = append(s.histories, discoverymodel.RuleSyncHistory{
			AdtID: &adtID, OneagentID: &agentID, OneagentName: &agentName, SyncAt: &at,
		})
	}
	return nil
}

// targetFromWrite mirrors the store's create.
func targetFromWrite(in TargetWrite) discoverymodel.Target {
	row := discoverymodel.Target{
		Attributes:  in.Attributes,
		Enabled:     true,
		AdrID:       in.AdrID,
		AgentID:     in.AgentID,
		QueryExpr:   in.QueryExpr,
		Interval:    in.Interval,
		Cron:        in.Cron,
		ExtraOption: in.ExtraOption,
	}
	if in.TypeID != 0 {
		row.TypeID = idp(in.TypeID)
	}
	if in.AutoAccept != nil {
		row.AutoAccept = *in.AutoAccept
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}
	if in.UID != 0 {
		row.UID = idp(in.UID)
	}
	return row
}

// relationKeyOfRow mirrors the store's identity for a relation, so a test can
// compare what the use case sent with what the double stored.
func relationKeyOfRow(row discoverymodel.TargetRelation) string {
	return derefString(row.AdKey) + "\x00" + fmtID(row.PeerTypeID) + "\x00" + fmtID(row.PeerAttrID)
}

func fmtID(id model.ID) string { return strconv.Itoa(int(id)) }

var errStub = errors.New("stub")

func idp(id model.ID) *model.ID { return &id }
func strp(s string) *string     { return &s }
func boolp(b bool) *bool        { return &b }
