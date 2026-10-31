package discovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
)

// The agent's side of the module: what a collector asks for, and what it is
// given.
//
// THIS IS THE ONLY PATH IN THE MODULE THAT HANDS OUT A PLAINTEXT CREDENTIAL.
// Everywhere else the `secret` and `password` entries are stripped on the way
// out — the rule's accounts, the target's own settings — and here they are
// opened, because an agent that cannot authenticate cannot collect. Everything
// else follows from that: this is the one entry point whose caller must be
// trusted as the deployment itself, and the one entry point that FAILS CLOSED
// when a credential cannot be read.

// InstanceQueries is what the poll needs from the instance side.
//
// THE PORT IS DECLARED BY THE CONSUMER, as everywhere in this bounded context,
// and implemented by the instance module's adapter. Two questions, and the
// second is why the port exists: a target may be bound to a QUERY EXPRESSION
// rather than to an agent, and answering "should this agent run it" means asking
// whether the agent's own machine is one of the instances that expression
// selects.
type InstanceQueries interface {
	// LocateAgentInstance returns the instance an agent identified itself as
	// running on, or zero when there is none — which includes a deployment that
	// does not model agent placement at all.
	LocateAgentInstance(ctx context.Context, oneagentID, oneagentName string) (model.ID, error)

	// MatchInstance reports whether a query expression selects one instance.
	MatchInstance(ctx context.Context, expr string, ciID model.ID) (bool, error)
}

// SyncRequest is one agent's poll.
type SyncRequest struct {
	// CIID is the instance the agent asked on behalf of. The handler resolves it
	// from the agent id before calling; a zero means the deployment does not say
	// which instance an agent runs on.
	CIID model.ID

	// OneagentID and OneagentName identify the agent.
	OneagentID   string
	OneagentName string

	// LastUpdateAt is the timestamp the agent was last given, as it sent it
	// back. Empty means "I have nothing".
	LastUpdateAt string
}

// SyncResult is what an agent is handed.
type SyncResult struct {
	// Rules is one entry per target this agent should run. It is EMPTY when
	// nothing has changed since LastUpdateAt, which is the common answer.
	Rules []SyncRule `json:"rules"`

	// LastUpdateAt is the timestamp the agent should send back next time. It is
	// returned even when Rules is empty, and it advances for changes that
	// produce no rule — a target switched off, or a rule's definition edited.
	LastUpdateAt string `json:"last_update_at"`
}

// SyncRule is one target as an agent receives it.
//
// IT IS NOT THE CONSOLE'S SHAPE. `extra_option` here holds the OPENED
// credential, because the agent has to authenticate with it, and the rule's
// plugin script is still withheld. Both are deliberate: the console's view is
// for a person, this one is for the process that does the work.
type SyncRule struct {
	ID     model.ID `json:"id"`
	TypeID model.ID `json:"type_id,omitempty"`
	AdrID  model.ID `json:"adr_id,omitempty"`

	Attributes map[string]any `json:"attributes"`

	AutoAccept bool   `json:"auto_accept"`
	AgentID    string `json:"agent_id"`
	QueryExpr  string `json:"query_expr"`
	Cron       string `json:"cron"`

	Interval *model.Int32 `json:"interval,omitempty"`

	ExtraOption map[string]any `json:"extra_option"`

	UID     *model.ID `json:"uid,omitempty"`
	Enabled bool      `json:"enabled"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	Rule *SyncRuleRef `json:"adr,omitempty"`
}

// SyncRuleRef is the rule a handed-out target runs, without its field list.
//
// THE FIELD LIST IS REMOVED, which is cantor's own `pop("attributes")`: the
// agent maps by the target's `attributes`, and sending the provider's whole
// field list again would be the same information twice — with the risk that the
// two disagree.
type SyncRuleRef struct {
	ID        model.ID  `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type,omitempty"`
	IsInner   bool      `json:"is_inner"`
	IsPlugin  bool      `json:"is_plugin"`
	UniqueKey string    `json:"unique_key,omitempty"`
	Owner     *model.ID `json:"owner,omitempty"`

	Option map[string]any `json:"option,omitempty"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// WithInstances supplies the instance-side queries.
func (u *Usecase) WithInstances(queries InstanceQueries) *Usecase {
	u.instances = queries
	return u
}

// Sync hands an agent the targets it should run.
//
// THE DECISION IS THREE-WAY, and cantor's console writes the three modes
// explicitly: a target names ONE AGENT, or an EXPRESSION whose selected
// instances' agents should run it, or NEITHER — which means every agent, except
// the master, which is a control plane rather than a collector.
func (u *Usecase) Sync(ctx context.Context, req SyncRequest) (*SyncResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	targets, err := u.repo.ListAllTargets(ctx)
	if err != nil {
		return nil, err
	}
	rules, err := u.rulesByID(ctx)
	if err != nil {
		return nil, err
	}

	chosen := make([]discoverymodel.Target, 0, len(targets))
	newest := ""
	for _, target := range targets {
		if !target.Enabled {
			// A DISABLED TARGET STILL COUNTS TOWARDS THE TIMESTAMP. Switching one
			// off has to reach the agent, and the only channel is the timestamp:
			// the payload cannot say "you have one fewer rule" because an empty
			// payload already means "nothing changed".
			newest = laterOf(newest, cmdbTime(target.CreatedAt), cmdbTime(target.UpdatedAt))
			continue
		}
		applies, err := u.targetApplies(ctx, target, req, rules)
		if err != nil {
			return nil, err
		}
		if applies {
			chosen = append(chosen, target)
		}
	}

	out := &SyncResult{Rules: []SyncRule{}}
	for _, target := range chosen {
		rule, hasRule := rules[derefID(target.AdrID)]
		if hasRule {
			// THE RULE'S OWN TIMESTAMPS COUNT TOO. A provider's definition
			// changing is a change to every target built on it, and the target
			// row itself does not move when that happens.
			newest = laterOf(newest, cmdbTime(rule.CreatedAt), cmdbTime(rule.UpdatedAt))
		}
		newest = laterOf(newest, cmdbTime(target.CreatedAt), cmdbTime(target.UpdatedAt))

		view, err := u.renderSyncRule(ctx, target, rule, hasRule)
		if err != nil {
			return nil, err
		}
		out.Rules = append(out.Rules, *view)
	}

	// THE RECORD IS WRITTEN BEFORE THE ANSWER. It is an upsert per
	// (target, agent) pair, so a poll overwrites last time's row rather than
	// adding one — and it is written for the targets that were CHOSEN, whether or
	// not the payload below is empty, because "this agent collects this target"
	// is true either way.
	if err := u.recordSync(ctx, chosen, req); err != nil {
		return nil, err
	}

	out.LastUpdateAt = newest
	// NOTHING NEW MEANS AN EMPTY PAYLOAD, not a repeat of the last one. The
	// agent keeps what it has; the timestamp is returned either way so it can
	// still record progress.
	if req.LastUpdateAt != "" && newest <= req.LastUpdateAt {
		out.Rules = []SyncRule{}
	}
	return out, nil
}

// targetApplies decides whether one target belongs to the agent that asked.
func (u *Usecase) targetApplies(ctx context.Context, target discoverymodel.Target, req SyncRequest, rules map[model.ID]discoverymodel.Rule) (bool, error) {
	targetAgent := strings.TrimSpace(derefString(target.AgentID))
	expr := strings.TrimSpace(derefString(target.QueryExpr))

	switch {
	case targetAgent != "":
		// BOUND TO ONE AGENT, by its id. The comparison is exact and the agent
		// supplies its own id — there is no name matching here, which is what
		// makes renaming an agent harmless.
		return targetAgent == strings.TrimSpace(req.OneagentID), nil

	case expr != "":
		// BOUND TO AN EXPRESSION. Which instances it selects is a question for the
		// instance side, and one the port answers with the port's own query
		// language — see the query module's enumerated deviations, which apply
		// here too.
		if req.CIID == 0 || u.instances == nil {
			return false, nil
		}
		return u.instances.MatchInstance(ctx, stripQueryPrefix(expr), req.CIID)

	default:
		// BOUND TO NOTHING, which cantor's console calls "all": every collector,
		// with two conditions.
		if target.AdrID == nil || *target.AdrID == 0 {
			// A target with no rule has nothing to run. cantor required an adr here
			// and so does this.
			return false, nil
		}
		if isZeroAgentID(req.OneagentID) {
			// THE MASTER IS EXCLUDED. An agent whose id is all zeroes is the
			// control plane, not a collector, and a target that applies everywhere
			// would otherwise be handed to it — the one machine guaranteed to be
			// wrong for it.
			return false, nil
		}
		rule, ok := rules[*target.AdrID]
		if !ok {
			return false, nil
		}
		// A CLOUD OR NETWORK DEVICE IS NOT A HOST'S JOB. Those providers do not
		// collect from the machine the agent runs on, so a catch-all target built
		// on one is not handed out; it reaches an agent only by being bound to one
		// explicitly.
		switch derefString(rule.Type) {
		case discoverymodel.TypeSNMP, discoverymodel.TypeHTTP:
			return false, nil
		}
		return true, nil
	}
}

// renderSyncRule renders one target for an agent, opening its credential.
func (u *Usecase) renderSyncRule(ctx context.Context, target discoverymodel.Target, rule discoverymodel.Rule, hasRule bool) (*SyncRule, error) {
	option, err := u.openCredentials(ctx, target)
	if err != nil {
		return nil, err
	}
	view := &SyncRule{
		ID:          target.ID,
		TypeID:      derefID(target.TypeID),
		AdrID:       derefID(target.AdrID),
		Attributes:  map[string]any{},
		AutoAccept:  target.AutoAccept,
		AgentID:     derefString(target.AgentID),
		QueryExpr:   derefString(target.QueryExpr),
		Cron:        derefString(target.Cron),
		Interval:    target.Interval,
		ExtraOption: option,
		UID:         target.UID,
		Enabled:     target.Enabled,
		CreatedAt:   cmdbTime(target.CreatedAt),
		UpdatedAt:   cmdbTime(target.UpdatedAt),
	}
	for k, v := range target.Attributes {
		view.Attributes[k] = v
	}
	if hasRule {
		view.Rule = &SyncRuleRef{
			ID: rule.ID, Name: rule.Name,
			Type:      derefString(rule.Type),
			IsInner:   rule.IsInner,
			IsPlugin:  rule.IsPlugin,
			UniqueKey: derefString(rule.UniqueKey),
			Owner:     rule.Owner,
			Option:    rule.Option,
			CreatedAt: cmdbTime(rule.CreatedAt),
			UpdatedAt: cmdbTime(rule.UpdatedAt),
		}
	}
	return view, nil
}

// openCredentials returns a target's settings with its credential in the clear.
//
// TWO FAILURES ARE POSSIBLE AND BOTH REFUSE THE POLL:
//
//   - the stored ciphertext cannot be read — a key rotation, or a row adopted
//     from a cantor database whose key this deployment does not have;
//   - the account a target points at is gone.
//
// CANTOR DID NEITHER. It logged a decrypt failure and left the CIPHERTEXT in
// the payload, so the agent received `v1:...` as a password: it would fail to
// authenticate with nothing to distinguish that from a wrong password, and the
// deployment would look like it had the wrong credentials rather than the wrong
// key. It also dereferenced a missing account without checking.
func (u *Usecase) openCredentials(ctx context.Context, target discoverymodel.Target) (map[string]any, error) {
	out := make(map[string]any, len(target.ExtraOption))
	for k, v := range target.ExtraOption {
		out[k] = v
	}

	// A TARGET MAY NAME AN ACCOUNT INSTEAD OF HOLDING A KEY. The reference
	// replaces whatever credential the target itself carries, which is cantor's
	// behaviour — `pop` then `update`.
	if raw, ok := out["reference"]; !ok || raw == nil {
		if refRaw, ok := out["_reference"]; ok && refRaw != nil {
			id := toModelID(refRaw)
			if id == 0 {
				return nil, fmt.Errorf("%w: target %d points at account %v", ErrTargetIncomplete, target.ID, refRaw)
			}
			account, err := u.repo.LoadAccount(ctx, id)
			if err != nil {
				return nil, err
			}
			if account == nil {
				// cantor's `get_config_by_id` returned an empty mapping for a
				// missing account, so the target was handed out with no
				// credential at all and failed silently at the far end. A
				// reference that names nothing is a configuration error the
				// deployment can fix, and it is reported as one.
				return nil, fmt.Errorf("%w: target %d points at account %d, which does not exist",
					ErrTargetIncomplete, target.ID, id)
			}
			delete(out, AccountPasswordKey)
			delete(out, AccountSecretKey)
			opened, err := openCredentialEntries(account.Config)
			if err != nil {
				return nil, fmt.Errorf("%w: target %d, account %d, %v", ErrCredentialUnreadable, target.ID, id, err)
			}
			for k, v := range opened {
				out[k] = v
			}
		}
	}

	opened, err := openCredentialEntries(out)
	if err != nil {
		return nil, fmt.Errorf("%w: target %d (%v)", ErrCredentialUnreadable, target.ID, err)
	}
	return opened, nil
}

// openCredentialEntries decrypts the credential entries of one settings map.
func openCredentialEntries(config map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(config))
	for k, v := range config {
		out[k] = v
	}
	for _, key := range credentialKeys {
		raw, ok := out[key]
		if !ok {
			continue
		}
		text, _ := raw.(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		plain, err := secretbox.Decrypt(text)
		if err != nil {
			return nil, fmt.Errorf("%s cannot be decrypted: %w", key, err)
		}
		out[key] = plain
	}
	return out, nil
}

// recordSync writes the handover log for one poll.
func (u *Usecase) recordSync(ctx context.Context, chosen []discoverymodel.Target, req SyncRequest) error {
	if len(chosen) == 0 {
		return nil
	}
	at := u.Now()
	entries := make([]SyncHistoryWrite, 0, len(chosen))
	for _, target := range chosen {
		entries = append(entries, SyncHistoryWrite{
			AdtID:        target.ID,
			OneagentID:   strings.TrimSpace(req.OneagentID),
			OneagentName: strings.TrimSpace(req.OneagentName),
			SyncAt:       at,
		})
	}
	return u.repo.RecordSyncHistories(ctx, entries)
}

// laterOf returns the newest of a set of cantor-format timestamps.
//
// THE COMPARISON IS LEXICOGRAPHIC, and it is correct because the format is
// fixed-width and big-endian. That is also why the format is not a display
// choice: an agent's stored `last_update_at` is compared the same way on its
// next poll.
func laterOf(current string, more ...string) string {
	newest := current
	for _, candidate := range more {
		if candidate > newest {
			newest = candidate
		}
	}
	return newest
}

// stripQueryPrefix removes the `q=` an expression may be written with.
//
// IT PEELS CHARACTERS, not a prefix: cantor did `lstrip('q').lstrip('=')`, so
// any run of leading `q`s goes and then any run of `=`s. An expression written
// `q=host,cpu:8` and one written `host,cpu:8` are the same expression, which is
// what makes a value pasted out of the console's search bar usable here.
func stripQueryPrefix(expr string) string {
	return strings.TrimLeft(strings.TrimLeft(expr, "q"), "=")
}

// isZeroAgentID reports whether an agent id names the master.
//
// THE IDS ARE HEXADECIMAL, and the console writes the master as `0x0000` — so
// the prefix has to be peeled before the parse, which Go does not do for an
// explicit base and Python's `int(text, 16)` does. An id that is not hexadecimal
// is not the master, and neither is an empty one.
func isZeroAgentID(raw string) bool {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "0x"), "0X")
	if text == "" {
		return false
	}
	n, err := strconv.ParseInt(text, 16, 64)
	if err != nil {
		return false
	}
	return n == 0
}

// toModelID reads a JSON value as an id.
func toModelID(raw any) model.ID {
	switch v := raw.(type) {
	case nil:
		return 0
	case model.ID:
		return v
	case int:
		return model.ID(v)
	case int64:
		return model.ID(v)
	case float64:
		return model.ID(int64(v))
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
		if err != nil || n <= 0 {
			return 0
		}
		return model.ID(n)
	}
	return 0
}
