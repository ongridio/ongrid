// Package discovery is the auto-discovery module's use-case layer: the rules
// that say what can be discovered, the credentials they run with, and — in the
// next increment — the targets that bind them to a model and the pool of what
// agents reported.
//
// IT RUNS NOTHING. cantor's `test` endpoint returns three hard-coded lines and
// dispatches nothing; the probing is an agent's job, and the agent PULLS the
// rules it should run rather than being told. So this package has no SNMP
// client, no cloud SDK and no call into another bounded context — the
// "provider" of a rule is data, and the catalogue that describes it is embedded
// reference data.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Repo is the persistence the rule and account layers need.
//
// EACH METHOD IS ONE TRANSACTION, as everywhere else in this bounded context:
// the data packages own GORM and the use cases never see it, so atomicity is
// expressed by making the repository call the unit of work.
type Repo interface {
	// ListRules returns every rule, ordered by id.
	ListRules(ctx context.Context) ([]discoverymodel.Rule, error)

	// LoadRule returns a rule by id, or nil.
	LoadRule(ctx context.Context, id model.ID) (*discoverymodel.Rule, error)

	// FindRuleByName returns a rule by its unique name, or nil.
	FindRuleByName(ctx context.Context, name string) (*discoverymodel.Rule, error)

	// CreateRule inserts a rule and returns it with its assigned id.
	CreateRule(ctx context.Context, in RuleWrite) (*discoverymodel.Rule, error)

	// UpdateRule applies the fields a write supplies and returns the row.
	UpdateRule(ctx context.Context, id model.ID, in RuleWrite) (*discoverymodel.Rule, error)

	// DeleteRule removes a rule and the accounts that belong to it, in one
	// transaction.
	DeleteRule(ctx context.Context, id model.ID) error

	// UpsertRules applies a template import — a list of rules keyed by NAME —
	// in one transaction, returning how many were created and how many updated.
	//
	// IT IS ONE TRANSACTION because cantor's loop committed per rule: an import
	// of twenty templates that failed on the tenth left nine written and no
	// record of which.
	UpsertRules(ctx context.Context, in []RuleWrite) (created, updated int, err error)

	// ListAccounts returns a rule's accounts, ordered by id.
	ListAccounts(ctx context.Context, adrID model.ID) ([]discoverymodel.Account, error)

	// LoadAccount returns an account by id, or nil.
	LoadAccount(ctx context.Context, id model.ID) (*discoverymodel.Account, error)

	// SaveAccount creates or replaces one account and returns the row. Its
	// Config is stored as given — see the account use case for who encrypts it.
	SaveAccount(ctx context.Context, in AccountWrite) (*discoverymodel.Account, error)

	// DeleteAccount removes an account, reporting whether a live row was there.
	DeleteAccount(ctx context.Context, id model.ID) (bool, error)

	// ReplaceAccountsForRule makes a rule's account set exactly the named ones,
	// in one transaction, reporting the ids it removed.
	//
	// A FULL REPLACE, because that is what the console sends: its account list
	// is one form whose save is the whole set, and per-account calls would make
	// deleting one a request the console has to remember to make.
	ReplaceAccountsForRule(ctx context.Context, adrID model.ID, keep []AccountWrite, owner model.ID) ([]model.ID, error)

	// ListTargets returns every target built on one model, ordered by id.
	ListTargets(ctx context.Context, typeID model.ID) ([]discoverymodel.Target, error)

	// ListAllTargets returns every target, ordered by id.
	//
	// THE AGENT'S POLL IS NOT SCOPED TO A MODEL. An agent asks "what should I
	// run" and gets the targets whose agent binding or expression selects it,
	// which may span models; asking once per model would be a query per model on
	// every poll.
	ListAllTargets(ctx context.Context) ([]discoverymodel.Target, error)

	// LoadTarget returns a target by id, or nil.
	LoadTarget(ctx context.Context, id model.ID) (*discoverymodel.Target, error)

	// CreateTarget inserts a target in one transaction that also confirms the
	// rule it names still exists.
	//
	// THE CHECK IS INSIDE THE TRANSACTION because the two halves race otherwise:
	// a rule deleted between the check and the insert leaves a target pointing at
	// nothing, and the delete's own guard — which counts targets — was evaluated
	// before the target existed.
	CreateTarget(ctx context.Context, in TargetWrite) (*discoverymodel.Target, error)

	// UpdateTarget applies the fields a write supplies and returns the row.
	//
	// A CHANGE TO THE AGENT BINDING ALSO CLEARS THE HANDOVER LOG, in the same
	// transaction: those rows record what a specific agent was given, and after
	// the binding changes they describe a configuration that no longer exists.
	UpdateTarget(ctx context.Context, id model.ID, in TargetWrite) (*discoverymodel.Target, error)

	// DeleteTarget removes a target, its handover log, and the relation rows of
	// its model that no remaining target can produce, in one transaction.
	DeleteTarget(ctx context.Context, id model.ID) (*TargetDeleteResult, error)

	// ListTargetRelations returns one model's discovered relations, ordered by id.
	ListTargetRelations(ctx context.Context, typeID model.ID) ([]discoverymodel.TargetRelation, error)

	// ReplaceTargetRelations makes a model's relation set exactly the named ones,
	// in one transaction, reporting what was added and what was removed.
	ReplaceTargetRelations(ctx context.Context, typeID model.ID, in []TargetRelationWrite) (*RelationReplaceResult, error)

	// ListSyncHistories returns one target's handover log, newest first.
	ListSyncHistories(ctx context.Context, adtID model.ID, limit, offset int) ([]discoverymodel.RuleSyncHistory, int64, error)

	// RecordSyncHistories records, in one transaction, that a set of targets was
	// handed to one agent — one row per target, UPDATED when that pair already
	// has one.
	//
	// IT IS AN UPSERT KEYED BY (target, agent), and that is what keeps the table
	// small: cantor wrote a row per poll per target, so one agent polling every
	// minute against twenty targets would have added nearly thirty thousand rows
	// a day all saying the same thing. What the log actually answers is "which
	// agents have this target, and when did each last collect it" — one row per
	// pair.
	//
	// ONE TRANSACTION, where cantor committed the batch in a background task
	// after the response had gone: a failure there was logged and the rows were
	// dropped, so the console's sync column would show an agent that had never
	// collected a target it collected every minute.
	RecordSyncHistories(ctx context.Context, in []SyncHistoryWrite) error
}

// TargetWrite is one write of a target.
//
// THE OPTIONALS ARE POINTERS OR FLAGGED SETS, for the reason the rule write's
// are: an omitted field leaves the stored value alone and a supplied empty one
// writes it. The console always sends `agent_id`, `query_expr` and `cron` — as
// empty strings when a mode is unused — so an empty string here is a VALUE that
// clears a binding, and treating it as an absence would leave a target pointing
// at an agent the operator had just removed.
type TargetWrite struct {
	// TypeID is the model the target discovers. Required on a create.
	TypeID model.ID

	// AdrID is the rule the target runs. It is a POINTER because the console
	// sends `adr_id: 0` while no provider has been chosen and omits the field
	// entirely on a write that is not about the provider — and those are two
	// different instructions: clear the binding, or leave it alone.
	AdrID *model.ID

	// Attributes is the `ad_key → model attribute` mapping.
	Attributes    map[string]any
	SetAttributes bool

	AutoAccept *bool
	AgentID    *string
	QueryExpr  *string
	Interval   *model.Int32
	Cron       *string

	// ExtraOption is the target's own settings. Its credential entries are
	// ALREADY SEALED: the use case seals them, so the store never sees a
	// plaintext secret and cannot leak one by logging a row.
	ExtraOption    map[string]any
	SetExtraOption bool

	// Relation is cantor's deprecated pre-2.4.5 column. Nothing here writes it;
	// a write that mentions it is refused rather than silently dropped.
	Enabled *bool

	UID model.ID
}

// TargetRelationWrite is one discovered relation.
type TargetRelationWrite struct {
	AdKey      string
	PeerTypeID model.ID
	PeerAttrID model.ID
}

// RelationReplaceResult is what a relation write did.
type RelationReplaceResult struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`

	// Relations is the set as it now stands.
	Relations []TargetRelation `json:"relations"`
}

// TargetDeleteResult is what a target delete took away.
type TargetDeleteResult struct {
	// RelationsRemoved names the ad_keys whose relation rows went with it,
	// because the model's remaining targets cannot produce them. It is reported
	// so an operator can tell "the relation went away because I deleted the
	// provider that described it" from "somebody else edited the relations".
	RelationsRemoved []string `json:"relations_removed,omitempty"`
}

// SyncHistoryWrite is one appended handover record.
type SyncHistoryWrite struct {
	AdtID        model.ID
	OneagentID   string
	OneagentName string
	SyncAt       time.Time
}

// RuleWrite is one write of a rule.
//
// THE OPTIONAL FIELDS ARE POINTERS, so a write says which of them it mentions:
// nil leaves the stored value alone on an update, and an empty value that was
// supplied is written. cantor's update ran with `filter_none=False`, which is
// the same rule — an empty string is a value a caller may mean.
type RuleWrite struct {
	// Name is the rule's identity. It is required on a create and, when
	// supplied on an update, must not collide.
	Name string

	Type         *string
	IsPlugin     *bool
	PluginScript *string
	UniqueKey    *string
	Attributes   discoverymodel.AttributeList
	Option       map[string]any

	// IsInner marks a rule that ships with the product rather than one a
	// deployment wrote. It is set only by the built-in bootstrap — see
	// EnsureBuiltinRules — and never by an operator's own write, because a
	// caller that could claim `is_inner` could make its own rule look like one
	// the console groups separately and no one is allowed to edit.
	IsInner *bool

	// Owner is the Ongrid user id the rule belongs to. On the template-import
	// path it is the importing caller's.
	Owner model.ID
}

// AccountWrite is one write of an account.
type AccountWrite struct {
	// ID is zero for a create.
	ID model.ID

	// Name identifies the account within a rule.
	Name string

	// Config is the credential, ALREADY SEALED: the use case encrypts, so the
	// store never sees a plaintext secret and cannot leak one by logging a row.
	Config map[string]any

	// UID is the account's owner.
	UID model.ID
}

// The module's errors.
var (
	// ErrRuleNotFound is returned when a rule id or name matches nothing.
	ErrRuleNotFound = fmt.Errorf("%w: rule does not exist", errs.ErrNotFound)

	// ErrRuleNameTaken is returned when a rule's name is already in use. The
	// name is the rule's identity, and a target's row is labelled by it.
	ErrRuleNameTaken = fmt.Errorf("%w: another rule already has that name", errs.ErrConflict)

	// ErrRuleReferenced is returned when a rule is deleted while a target is
	// built on it.
	//
	// THE GUARD IS EVALUATED INSIDE THE DELETE'S OWN TRANSACTION, not before it:
	// a target created between the count and the delete would be orphaned, and
	// the create path takes the same rule row's lock so the two orderings are
	// both safe.
	ErrRuleReferenced = fmt.Errorf("%w: a discovery target uses this rule", errs.ErrConflict)

	// ErrTargetNotFound is returned when a target id matches nothing.
	ErrTargetNotFound = fmt.Errorf("%w: discovery target does not exist", errs.ErrNotFound)

	// ErrTargetReferenced is returned when a target is deleted while a pool entry
	// refers to it.
	//
	// THE GUARD IS INSIDE THE DELETE'S TRANSACTION, like the rule guard: a
	// report accepted between a count and a delete would leave a pool row whose
	// target no longer exists, which is a row nothing can ever accept.
	ErrTargetReferenced = fmt.Errorf("%w: discovered instances in the pool refer to this target", errs.ErrConflict)

	// ErrTargetMappingNeedsUniqueKey is returned for a target whose mapping does
	// not say which attribute identifies a discovered instance.
	//
	// IT IS THE ONE MAPPING RULE THERE IS. Without it nothing says whether a
	// reported row is a new machine or a second sighting of one already known,
	// so the target could only ever create duplicates — or overwrite whichever
	// instance happened to sort first.
	ErrTargetMappingNeedsUniqueKey = fmt.Errorf(
		"%w: the mapping has to carry the attribute that identifies an instance", errs.ErrInvalid)

	// ErrTargetCatchAllNeedsAdmin is returned when a caller who is not an
	// administrator creates a target that applies to every agent.
	//
	// A TARGET WITH NO AGENT AND NO EXPRESSION runs everywhere. cantor gated it
	// on the application-admin role; the rest of that same function also
	// consulted the duty attributes of the instances an expression selected,
	// which is the resource-level authorization this port has not built — see
	// ErrTargetScopingUnenforced.
	ErrTargetCatchAllNeedsAdmin = fmt.Errorf(
		"%w: a target that applies to every agent is an administrative write", errs.ErrForbidden)

	// ErrTargetScopingUnenforced names the half of cantor's target authorization
	// this port does not implement.
	//
	// CANTOR REQUIRED THE CALLER TO BE ON `op_duty` OR `rd_duty` FOR EVERY
	// INSTANCE AN EXPRESSION SELECTED. Those are deployment-defined attributes on
	// deployment-defined models, and the check belongs to the resource-level
	// authorization that is not built yet. It is named here rather than left
	// implicit so that the gap is a recorded obligation instead of an absence —
	// the same treatment the rule-delete guard got before its table existed.
	ErrTargetScopingUnenforced = fmt.Errorf(
		"%w: per-instance target authorization is not implemented", errs.ErrNotWiredYet)

	// ErrTargetRelationIncomplete is returned for a relation that does not name
	// all three of its parts.
	ErrTargetRelationIncomplete = fmt.Errorf(
		"%w: a discovered relation needs a field, a peer model and a peer attribute", errs.ErrInvalid)

	// ErrDiscoverTestUnsupported is returned by the target test endpoint.
	//
	// CANTOR'S ANSWERED WITH THREE HARD-CODED LINES and dispatched nothing —
	// no probe, no agent round trip. A route that replies 200 having tested
	// nothing sends an operator to debug their credentials when the thing that
	// is missing is the test.
	ErrDiscoverTestUnsupported = fmt.Errorf(
		"%w: the port does not run a discovery probe; agents pull targets and report back",
		errs.ErrNotWiredYet)

	// ErrCredentialUnreadable is returned when a credential cannot be decrypted
	// for the agent that is asking for it.
	//
	// THIS IS THE FAIL-CLOSED PATH. cantor logged the failure and left the
	// CIPHERTEXT in the payload, so the agent received a password that was not
	// one — it would fail to authenticate with no way to tell that from a wrong
	// password. Refusing the whole poll names the target and the problem, and it
	// happens the first time an adopted database is polled, which is when
	// somebody should find out.
	//
	// It wraps no shared sentinel on purpose: nothing the caller sent was wrong
	// and nothing about the request is unsupported, so it maps to 500, which is
	// what "the server's own data is unreadable" is.
	ErrCredentialUnreadable = errors.New("cmdb discovery: a stored credential cannot be read")

	// ErrTargetIncomplete is returned when a stored target is missing something
	// the module needs in order to use it — a model, or a rule to run.
	ErrTargetIncomplete = fmt.Errorf("%w: a target is not runnable", errs.ErrInvalid)

	// ErrPluginScriptUnsupported is returned for a plugin rule whose field list
	// the caller expects the script to supply.
	//
	// THE PORT DOES NOT RUN PYTHON. cantor loaded the script, instantiated the
	// class it found and called `attributes()` on it — arbitrary code execution
	// as the way to read a list of field names. There is no Python runtime here
	// and there will not be one, so a plugin rule is created WITH its field
	// list supplied by the caller: `unique_key` and `attributes` travel in the
	// request, and the script is stored for a deployment that needs to read it.
	ErrPluginScriptUnsupported = fmt.Errorf(
		"%w: this rule's field list has to be supplied, because the port does not run plugin scripts",
		errs.ErrNotWiredYet)

	// ErrAccountNameRequired is returned for an account with no name, which is
	// what a rule's account list is keyed by.
	ErrAccountNameRequired = fmt.Errorf("%w: an account needs a name", errs.ErrInvalid)

	// ErrAccountSecretNotYours is returned when a caller who does not own an
	// account tries to write a credential into it.
	//
	// CANTOR'S ONE OWNERSHIP RULE, and the only one that matters here: reading
	// an account's list shows its names, but a secret belongs to the person who
	// put it there. Anyone else changing it would be writing a credential into
	// somebody else's rule — which then runs with it.
	ErrAccountSecretNotYours = fmt.Errorf("%w: the account's secret belongs to its owner", errs.ErrForbidden)
)
