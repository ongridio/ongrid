package discovery

import (
	"context"
	"fmt"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// The credential layer: what a discovery runs with.
//
// AN ACCOUNT IS A SECRET AND A NAME. cantor encrypted the `secret` and
// `password` entries of its config and decrypted them on the way out — for any
// privileged caller, and for the agent that pulls the rule. Everything about
// this file follows from that: the secret is sealed when it is written, it is
// NEVER returned by a read, and the one path that needs the plaintext — the
// agent's rule pull — is the next increment's, where a value that cannot be
// decrypted must fail rather than be sent.
//
// CANTOR'S READ PATH HANDED BACK THE CIPHERTEXT WHEN DECRYPTION FAILED. It
// logged and left `config['secret']` as the stored blob, so the console showed
// `v1:...` in a password field — and saving that form again sealed the
// ciphertext, which is double encryption as a side effect of pressing save. The
// port does not decrypt on a read at all, so the state cannot arise.

// The config keys that are credentials.
const (
	// AccountSecretKey is a cloud access key's secret.
	AccountSecretKey = "secret"

	// AccountPasswordKey is a login password.
	AccountPasswordKey = "password"
)

// credentialKeys are the entries sealed at rest, in a stable order.
var credentialKeys = []string{AccountPasswordKey, AccountSecretKey}

// Account is one credential as the API presents it.
//
// THE CONFIG COMES BACK WITHOUT ITS SECRETS, and with flags saying whether each
// is set. That is the same posture the password attributes and the webhook
// authorization take: a caller learns that a credential exists and can replace
// it, and cannot read it back.
type Account struct {
	ID    model.ID  `json:"id"`
	Name  string    `json:"name"`
	AdrID model.ID  `json:"adr_id,omitempty"`
	UID   *model.ID `json:"uid,omitempty"`

	Config map[string]any `json:"config"`

	HasSecret   bool `json:"has_secret"`
	HasPassword bool `json:"has_password"`
}

// AccountInput is one write of an account.
//
// A NIL CONFIG'S CREDENTIALS ARE LEFT ALONE. The read path does not return
// them, so a console that round-trips what it read sends a config without them;
// treating the absence as "clear" would wipe the credential on every save. An
// explicitly EMPTY entry is different, and clears it — see sealAccountConfig.
type AccountInput struct {
	ID     model.ID
	Name   string
	Config map[string]any
}

// AccountUpsertResult is what a rule's account write did.
type AccountUpsertResult struct {
	Accounts []Account `json:"accounts"`

	// Removed are the accounts the write took away, by name.
	Removed []string `json:"removed,omitempty"`
}

// ListAccounts returns a rule's accounts, without their secrets.
func (u *Usecase) ListAccounts(ctx context.Context, ruleRef string) ([]Account, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rule, err := u.resolveRule(ctx, ruleRef)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, ruleRef)
	}
	rows, err := u.repo.ListAccounts(ctx, rule.ID)
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(rows))
	for _, row := range rows {
		out = append(out, renderAccount(row))
	}
	return out, nil
}

// GetAccount returns one account, without its secrets.
func (u *Usecase) GetAccount(ctx context.Context, id model.ID) (*Account, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.repo.LoadAccount(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: account %d", errs.ErrNotFound, id)
	}
	view := renderAccount(*row)
	return &view, nil
}

// UpsertAccounts makes a rule's account set exactly the ones named.
//
// A FULL REPLACE, which is what the console sends: its account list is one form
// whose save is the whole set. Per-account calls would make deleting one a
// request the console has to remember to make — and forgetting it leaves a
// credential the operator believes they removed.
//
// ONLY THE OWNER WRITES A CREDENTIAL. An account belongs to the person who
// created it, and someone else editing it would be putting a secret into
// another operator's rule, which then runs with it.
func (u *Usecase) UpsertAccounts(ctx context.Context, ruleRef string, in []AccountInput, actor Actor) (*AccountUpsertResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	rule, err := u.resolveRule(ctx, ruleRef)
	if err != nil {
		return nil, err
	}
	if rule == nil {
		return nil, fmt.Errorf("%w: %s", ErrRuleNotFound, ruleRef)
	}

	existing, err := u.repo.ListAccounts(ctx, rule.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[model.ID]discoverymodel.Account, len(existing))
	for _, row := range existing {
		byID[row.ID] = row
	}

	writes := make([]AccountWrite, 0, len(in))
	seen := map[string]bool{}
	for i, item := range in {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: account %d", ErrAccountNameRequired, i+1)
		}
		if seen[name] {
			return nil, fmt.Errorf("%w: %q appears twice", ErrAccountNameRequired, name)
		}
		seen[name] = true

		prior, isExisting := discoverymodel.Account{}, false
		if item.ID != 0 {
			prior, isExisting = byID[item.ID]
			if !isExisting {
				// An id that names nothing: the console's list is stale. It is
				// refused rather than turned into a create, because a create
				// would silently give the account a new owner.
				return nil, fmt.Errorf("%w: account %d", errs.ErrNotFound, item.ID)
			}
		}
		if isExisting {
			if !mayWriteCredential(prior, item.Config, actor) {
				return nil, fmt.Errorf("%w: %q", ErrAccountSecretNotYours, prior.Name)
			}
		}

		sealed, err := sealAccountConfig(item.Config, prior.Config)
		if err != nil {
			return nil, err
		}
		write := AccountWrite{ID: item.ID, Name: name, Config: sealed}
		if isExisting && prior.UID != nil {
			write.UID = *prior.UID
		} else {
			write.UID = actor.UserID
		}
		writes = append(writes, write)
	}

	removed, err := u.repo.ReplaceAccountsForRule(ctx, rule.ID, writes, actor.UserID)
	if err != nil {
		return nil, err
	}

	rows, err := u.repo.ListAccounts(ctx, rule.ID)
	if err != nil {
		return nil, err
	}
	out := &AccountUpsertResult{Accounts: make([]Account, 0, len(rows))}
	for _, row := range rows {
		out.Accounts = append(out.Accounts, renderAccount(row))
	}
	_ = removed
	return out, nil
}

// DeleteAccount removes one account.
func (u *Usecase) DeleteAccount(ctx context.Context, id model.ID, actor Actor) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	row, err := u.repo.LoadAccount(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%w: account %d", errs.ErrNotFound, id)
	}
	if !ownsAccount(*row, actor) {
		return fmt.Errorf("%w: %q", ErrAccountSecretNotYours, row.Name)
	}
	removed, err := u.repo.DeleteAccount(ctx, id)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("%w: account %d", errs.ErrNotFound, id)
	}
	return nil
}

// renderAccount strips the credential entries and reports whether they were
// there.
//
// THE STRIPPING IS THE READ PATH'S WHOLE JOB. It is one function so no endpoint
// can forget it, which is the same reason the instance layer's secret filter
// lives in one place.
func renderAccount(row discoverymodel.Account) Account {
	view := Account{
		ID: row.ID, Name: row.Name,
		AdrID:  derefID(row.AdrID),
		UID:    row.UID,
		Config: map[string]any{},
	}
	for k, v := range row.Config {
		switch k {
		case AccountSecretKey:
			view.HasSecret = isSet(v)
		case AccountPasswordKey:
			view.HasPassword = isSet(v)
		default:
			view.Config[k] = v
		}
	}
	return view
}

// sealAccountConfig encrypts the credential entries of a config.
//
// THREE CASES, and telling them apart is what keeps a credential alive:
//
//   - the key is ABSENT from the caller's config — the credential is KEPT as it
//     is, because a read never returned it and a round-tripped form therefore
//     never sends it back;
//   - the key is present and EMPTY — the credential is CLEARED, which is how an
//     operator removes one;
//   - the key is present with a value — it is REPLACED, and sealed.
//
// A VALUE THAT IS ALREADY SEALED IS CARRIED THROUGH rather than sealed again.
// This is the defect the trigger credentials taught: sealing a stored
// ciphertext destroys the credential while leaving a value that still looks
// like one, so nothing reports it.
func sealAccountConfig(incoming, stored map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(incoming)+len(stored))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		out[k] = v
	}
	for _, key := range credentialKeys {
		raw, mentioned := incoming[key]
		if !mentioned {
			continue
		}
		text, _ := raw.(string)
		if strings.TrimSpace(text) == "" {
			// Present and empty: the operator cleared it.
			delete(out, key)
			continue
		}
		if prior, ok := stored[key].(string); ok && prior != "" && prior == text {
			// A caller that sent the STORED ciphertext back means "leave it".
			// cantor's read path handed the ciphertext out whenever decryption
			// failed, so a client that round-trips what it read sends one — and
			// sealing it again is how a credential is destroyed while still
			// looking sealed.
			out[key] = text
			continue
		}
		sealed, err := secretbox.Encrypt(text)
		if err != nil {
			return nil, fmt.Errorf("cmdb discovery: seal %s: %w", key, err)
		}
		out[key] = sealed
	}
	return out, nil
}

// mayWriteCredential reports whether a caller may replace an account's secret.
func mayWriteCredential(prior discoverymodel.Account, incoming map[string]any, actor Actor) bool {
	if incoming == nil {
		return true
	}
	touches := false
	for _, key := range credentialKeys {
		if v, ok := incoming[key]; ok && strings.TrimSpace(fmt.Sprint(v)) != "" {
			touches = true
		}
	}
	if !touches {
		return true
	}
	return ownsAccount(prior, actor)
}

// ownsAccount reports whether the caller owns an account.
//
// AN ACCOUNT WITH NO OWNER IS NOBODY'S, and cantor let anyone write it — the
// same shape as the model-ownership gap the port closed elsewhere. Here it is
// closed by treating an ownerless account as administrable only: a credential
// that nobody set is a credential nobody should be replacing on somebody
// else's behalf.
func ownsAccount(row discoverymodel.Account, actor Actor) bool {
	if row.UID == nil {
		return actor.IsAdmin
	}
	return *row.UID == actor.UserID || actor.IsAdmin
}

// isSet reports whether a stored credential entry holds anything.
func isSet(raw any) bool {
	if raw == nil {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(raw)) != ""
}

func derefID(id *model.ID) model.ID {
	if id == nil {
		return 0
	}
	return *id
}
