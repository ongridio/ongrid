// Package store is the auto-discovery module's persistence.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
)

var _ discoverybiz.Repo = (*Repo)(nil)

// Repo implements the discovery module's persistence port over GORM.
type Repo struct {
	db *gorm.DB
}

// NewRepo builds the repository.
func NewRepo(db *gorm.DB) *Repo { return &Repo{db: db} }

// Migrate registers the discovery tables.
//
// THE SEVEN THE MODULE NEEDS: a rule, its credentials, a target that binds one
// to a model, the relations a target's fields establish, the log of what was
// handed to which agent, and the two pool tables — the sightings themselves and
// the log of what happened to them.
func Migrate(db *gorm.DB) error {
	if db == nil {
		return errors.New("cmdb discovery store: nil db")
	}
	return db.AutoMigrate(
		&discoverymodel.Rule{},
		&discoverymodel.Account{},
		&discoverymodel.Target{},
		&discoverymodel.TargetRelation{},
		&discoverymodel.RuleSyncHistory{},
		&discoverymodel.PoolEntry{},
		&discoverymodel.ExecHistory{},
	)
}

// errNilDB mirrors the other stores' refusal to run against no database.
var errNilDB = errors.New("cmdb discovery store: nil db")

// --- rules ---

// ListRules returns every rule, ordered by id.
func (r *Repo) ListRules(ctx context.Context) ([]discoverymodel.Rule, error) {
	var rows []discoverymodel.Rule
	if err := r.db.WithContext(ctx).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: list rules: %w", err)
	}
	return rows, nil
}

// LoadRule returns a rule by id, or nil.
func (r *Repo) LoadRule(ctx context.Context, id model.ID) (*discoverymodel.Rule, error) {
	var row discoverymodel.Rule
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb discovery: load rule: %w", err)
	}
}

// FindRuleByName returns a rule by its unique name, or nil.
//
// THE COMPARISON FOLDS CASE, matching the column's collation on both dialects:
// a rule named `aliyun` and one named `Aliyun` are the same name to the
// database's unique index, so they have to be the same name here or the check
// would pass and the insert would fail.
func (r *Repo) FindRuleByName(ctx context.Context, name string) (*discoverymodel.Rule, error) {
	var row discoverymodel.Rule
	q := r.db.WithContext(ctx)
	if r.db.Dialector.Name() == "sqlite" {
		// The comparison form the dialect can serve — see §3.4.2. Case folding
		// matters here because the unique index folds too: without it the check
		// would pass and the insert would then fail on the index.
		// The collation goes on the COLUMN side — see §3.4.2, where the other
		// placement was measured to fall back to a scan.
		q = q.Where("name COLLATE NOCASE = ?", strings.TrimSpace(name))
	} else {
		q = q.Where("name = ?", strings.TrimSpace(name))
	}
	err := q.First(&row).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb discovery: find rule by name: %w", err)
	}
}

// CreateRule inserts a rule.
func (r *Repo) CreateRule(ctx context.Context, in discoverybiz.RuleWrite) (*discoverymodel.Rule, error) {
	row := discoverymodel.Rule{
		Name: in.Name, Type: in.Type,
		PluginScript: in.PluginScript, UniqueKey: in.UniqueKey,
		Attributes: in.Attributes, Option: in.Option,
	}
	if in.IsPlugin != nil {
		row.IsPlugin = *in.IsPlugin
	}
	if in.IsInner != nil {
		row.IsInner = *in.IsInner
	}
	if in.Owner != 0 {
		owner := in.Owner
		row.Owner = &owner
	}
	if err := r.db.WithContext(ctx).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: create rule: %w", err)
	}
	return &row, nil
}

// UpdateRule writes the fields a write supplies.
//
// ONLY THE MENTIONED FIELDS, which is why the write's optionals are pointers.
// The updates go through a map so a nil pointer is simply absent — assigning
// the struct would write every zero value over what is stored, which is how a
// rename ends up clearing a rule's field list.
func (r *Repo) UpdateRule(ctx context.Context, id model.ID, in discoverybiz.RuleWrite) (*discoverymodel.Rule, error) {
	updates := map[string]any{}
	if in.Name != "" {
		updates["name"] = in.Name
	}
	if in.Type != nil {
		updates["type"] = *in.Type
	}
	if in.IsPlugin != nil {
		updates["is_plugin"] = *in.IsPlugin
	}
	if in.PluginScript != nil {
		updates["plugin_script"] = *in.PluginScript
	}
	if in.UniqueKey != nil {
		updates["unique_key"] = *in.UniqueKey
	}
	if in.Attributes != nil {
		updates["attributes"] = in.Attributes
	}
	if in.Option != nil {
		updates["option"] = in.Option
	}
	if len(updates) > 0 {
		if err := r.db.WithContext(ctx).Model(&discoverymodel.Rule{}).
			Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("cmdb discovery: update rule: %w", err)
		}
	}
	return r.LoadRule(ctx, id)
}

// DeleteRule removes a rule and its accounts in one transaction.
//
// THE ACCOUNTS GO WITH IT. They are credentials for a provider that is no
// longer defined, and leaving them behind leaves secrets at rest whose owner
// has no page to find them on.
//
// A RULE A TARGET IS BUILT ON IS NOT REMOVED, and the guard is evaluated HERE
// rather than in the use case so that it cannot be raced: the count and the
// delete are in one transaction, and the create path takes this same row's lock,
// so a target cannot slip in between them. The same shape the model-delete guard
// takes against the service tree.
func (r *Repo) DeleteRule(ctx context.Context, id model.ID) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockRuleRow(tx, id); err != nil {
			return err
		}
		var bound int64
		if err := tx.Model(&discoverymodel.Target{}).Where("adr_id = ?", id).Count(&bound).Error; err != nil {
			return err
		}
		if bound > 0 {
			return fmt.Errorf("%w: %d", discoverybiz.ErrRuleReferenced, bound)
		}
		if err := tx.Where("adr_id = ?", id).Delete(&discoverymodel.Account{}).Error; err != nil {
			return err
		}
		return tx.Delete(&discoverymodel.Rule{}, id).Error
	})
	if err != nil {
		return fmt.Errorf("cmdb discovery: delete rule: %w", err)
	}
	return nil
}

// UpsertRules applies a template import in one transaction.
func (r *Repo) UpsertRules(ctx context.Context, in []discoverybiz.RuleWrite) (int, int, error) {
	var created, updated int
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, write := range in {
			var row discoverymodel.Rule
			q := tx
			if tx.Dialector.Name() == "sqlite" {
				q = q.Where("name COLLATE NOCASE = ?", write.Name)
			} else {
				q = q.Where("name = ?", write.Name)
			}
			err := q.First(&row).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				got, err := createRuleTx(tx, write)
				if err != nil {
					return err
				}
				_ = got
				created++
			case err != nil:
				return err
			default:
				if err := updateRuleTx(tx, row.ID, write); err != nil {
					return err
				}
				updated++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("cmdb discovery: import templates: %w", err)
	}
	return created, updated, nil
}

// --- accounts ---

// ListAccounts returns a rule's accounts, ordered by id.
func (r *Repo) ListAccounts(ctx context.Context, adrID model.ID) ([]discoverymodel.Account, error) {
	var rows []discoverymodel.Account
	if err := r.db.WithContext(ctx).Where("adr_id = ?", adrID).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("cmdb discovery: list accounts: %w", err)
	}
	return rows, nil
}

// LoadAccount returns an account by id, or nil.
func (r *Repo) LoadAccount(ctx context.Context, id model.ID) (*discoverymodel.Account, error) {
	var row discoverymodel.Account
	err := r.db.WithContext(ctx).First(&row, id).Error
	switch {
	case err == nil:
		return &row, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, fmt.Errorf("cmdb discovery: load account: %w", err)
	}
}

// SaveAccount creates or replaces one account.
func (r *Repo) SaveAccount(ctx context.Context, in discoverybiz.AccountWrite) (*discoverymodel.Account, error) {
	var out *discoverymodel.Account
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := saveAccountTx(tx, in, 0)
		out = row
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: save account: %w", err)
	}
	return out, nil
}

// DeleteAccount soft-deletes an account, reporting whether one was there.
func (r *Repo) DeleteAccount(ctx context.Context, id model.ID) (bool, error) {
	removed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row discoverymodel.Account
		if err := tx.First(&row, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		removed = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("cmdb discovery: delete account: %w", err)
	}
	return removed, nil
}

// ReplaceAccountsForRule makes a rule's account set exactly the named ones.
//
// IT IS ONE TRANSACTION: adding, updating and removing are one operator action
// ("save this list"), and a half-applied list is a set of credentials nobody
// can reason about.
//
// A REMOVAL ONLY TOUCHES THE CALLER'S OWN ACCOUNTS. Someone else's credential
// on the same rule is not the caller's to delete, and leaving it means the list
// the operator sees is a superset of what they sent — which is what cantor did,
// and is the honest behaviour: it makes the shared account visible rather than
// silently discarding a credential its owner still needs.
func (r *Repo) ReplaceAccountsForRule(ctx context.Context, adrID model.ID, keep []discoverybiz.AccountWrite, owner model.ID) ([]model.ID, error) {
	var removed []model.ID
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		kept := map[model.ID]bool{}
		for _, write := range keep {
			row, err := saveAccountTx(tx, write, adrID)
			if err != nil {
				return err
			}
			kept[row.ID] = true
		}

		var existing []discoverymodel.Account
		if err := tx.Where("adr_id = ?", adrID).Find(&existing).Error; err != nil {
			return err
		}
		for _, row := range existing {
			if kept[row.ID] {
				continue
			}
			if row.UID != nil && *row.UID != owner {
				continue
			}
			if err := tx.Delete(&row).Error; err != nil {
				return err
			}
			removed = append(removed, row.ID)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cmdb discovery: replace accounts: %w", err)
	}
	return removed, nil
}

// saveAccountTx writes one account inside the caller's transaction.
func saveAccountTx(tx *gorm.DB, in discoverybiz.AccountWrite, adrID model.ID) (*discoverymodel.Account, error) {
	if in.ID != 0 {
		updates := map[string]any{"name": in.Name, "config": in.Config}
		if err := tx.Model(&discoverymodel.Account{}).Where("id = ?", in.ID).
			Updates(updates).Error; err != nil {
			return nil, err
		}
		var row discoverymodel.Account
		if err := tx.First(&row, in.ID).Error; err != nil {
			return nil, err
		}
		return &row, nil
	}

	row := discoverymodel.Account{Name: in.Name, Config: in.Config}
	if adrID != 0 {
		ref := adrID
		row.AdrID = &ref
	}
	if in.UID != 0 {
		uid := in.UID
		row.UID = &uid
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// createRuleTx and updateRuleTx are the template import's two halves.
func createRuleTx(tx *gorm.DB, in discoverybiz.RuleWrite) (*discoverymodel.Rule, error) {
	row := discoverymodel.Rule{
		Name: in.Name, Type: in.Type,
		PluginScript: in.PluginScript, UniqueKey: in.UniqueKey,
		Attributes: in.Attributes, Option: in.Option,
	}
	if in.IsPlugin != nil {
		row.IsPlugin = *in.IsPlugin
	}
	if in.IsInner != nil {
		row.IsInner = *in.IsInner
	}
	if in.Owner != 0 {
		owner := in.Owner
		row.Owner = &owner
	}
	if err := tx.Create(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func updateRuleTx(tx *gorm.DB, id model.ID, in discoverybiz.RuleWrite) error {
	updates := map[string]any{}
	if in.Type != nil {
		updates["type"] = *in.Type
	}
	if in.IsPlugin != nil {
		updates["is_plugin"] = *in.IsPlugin
	}
	if in.PluginScript != nil {
		updates["plugin_script"] = *in.PluginScript
	}
	if in.UniqueKey != nil {
		updates["unique_key"] = *in.UniqueKey
	}
	if in.Attributes != nil {
		updates["attributes"] = in.Attributes
	}
	if in.Option != nil {
		updates["option"] = in.Option
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&discoverymodel.Rule{}).Where("id = ?", id).Updates(updates).Error
}
