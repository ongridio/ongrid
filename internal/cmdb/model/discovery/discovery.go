// Package discovery holds the auto-discovery tables.
//
// THE MODULE IS TWO LAYERS, and the tables split along them. A RULE says what
// can be discovered — a provider, the fields it reports, and how they are
// grouped — and a TARGET says where: which model the results become, which
// agent runs it, and how the provider's fields map onto the model's attributes.
// A rule is shared; a target is one deployment's use of it.
//
// THE PORT NEVER RUNS A PROBE. cantor's `adt/{id}/test` endpoint RETURNS THREE
// HARD-CODED LINES (`stdout="1\n2\n3"`) — it dispatches nothing — and the
// discovery itself is performed by an agent that PULLS the rules it should run
// and PUSHES what it found back. So there is no SNMP library here, no cloud
// SDK, and no cross-boundary call: the module's job is the data and the pool.
package discovery

import (
	"encoding/json"
	"sort"
	"time"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The provider kinds, as cantor named them. They are stored on a rule and on a
// target, and they decide which catalogue answers a target's field list.
const (
	TypeAgent      = "agent"
	TypeSNMP       = "snmp"
	TypeHTTP       = "http"
	TypeComponents = "components"
)

// Rule is one auto-discovery rule (c_ad_rules).
//
// A RULE IS A TEMPLATE, not a job: it says a provider exists and what it can
// report. The job is a Target, which binds it to a model and an agent. The
// built-in rules (`is_inner`) are the ones cantor ships — 阿里云, 交换机, Nginx
// and the rest — and an operator's own rule is the same shape with a script.
type Rule struct {
	cmdbmodel.Base

	// Name identifies the rule and is unique across the table, because a
	// target's console row is labelled by it.
	Name string `gorm:"column:name;size:32;not null;uniqueIndex:idx_c_ad_rules_name"`

	// Type is one of the provider kinds. It decides which catalogue a target
	// built on this rule reads its field list from.
	Type *string `gorm:"column:type;size:16;index:idx_c_ad_rules_type"`

	// IsInner marks the built-in rules, which the console groups separately and
	// which no deployment created.
	IsInner bool `gorm:"column:is_inner;not null;default:false;index:idx_c_ad_rules_is_inner"`

	// Owner is the Ongrid user id that created the rule. It is what the
	// ownership rules compare, and it is an Ongrid id — cantor's user table is
	// not migrated, so its own uid space is never reused.
	Owner *cmdbmodel.ID `gorm:"column:owner;index:idx_c_ad_rules_owner"`

	// Option is the console's layout data: the icon and the category a provider
	// is drawn under. It is open-ended because cantor's is.
	Option map[string]any `gorm:"column:option;serializer:json"`

	// Attributes is a plugin rule's own field list, transcribed from the
	// script's `attributes()`. See PluginScript.
	Attributes AttributeList `gorm:"column:attributes;serializer:json"`

	IsPlugin bool `gorm:"column:is_plugin;not null;default:false"`

	// PluginScript is USER-SUPPLIED PYTHON and is NEVER PARSED OR RUN.
	//
	// THE COLUMN IS MODELLED SO AN ADOPTED DATABASE READS, and the script is
	// stored so a deployment keeps what it wrote — but nothing here loads it.
	// cantor called `load_class_from_script` and then `attributes()` on the
	// resulting object, which is arbitrary code execution as a feature; the
	// port has no Python runtime and will not gain one. See
	// ErrPluginScriptUnsupported: a rule whose field list can only come from its
	// script is refused rather than half-created.
	PluginScript *string `gorm:"column:plugin_script;type:text"`

	// UniqueKey names the field that identifies a discovered instance, for a
	// plugin rule. See PluginScript.
	UniqueKey *string `gorm:"column:unique_key;size:64"`
}

func (Rule) TableName() string { return "c_ad_rules" }

// AttributeList is a rule's own field list (c_ad_rules.attributes).
//
// THE SHAPE IS PART OF A SHARED SCHEMA, and getting it wrong is not cosmetic.
// cantor's only writer of this column was `parse_plugin_script`, which returned a
// JSON ARRAY of `{name, type, desc}` entries, and its only reader was
// `[i.get("name") for i in adr.attributes]` — which raises on a MAPPING, because
// a string has no `.get`.
//
// A port that declared this column as a mapping therefore failed in BOTH
// directions, and the failing direction was measured rather than reasoned about:
// pointing the repository at a cantor-written plugin rule made the list endpoint
// fail outright with `json: cannot unmarshal array into Go value of type
// map[string]interface {}`. An earlier revision of this port also WROTE a
// mapping, which is a shape cantor never produced.
//
// An object is still ACCEPTED on the way in so a database written by that
// revision reads; it is never written back.
type AttributeList []map[string]any

// UnmarshalJSON accepts the array cantor wrote, or an object keyed by field name.
func (a *AttributeList) UnmarshalJSON(raw []byte) error {
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err == nil {
		*a = list
		return nil
	}

	var mapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mapped); err != nil {
		return err
	}
	names := make([]string, 0, len(mapped))
	for name := range mapped {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(mapped))
	for _, name := range names {
		entry := map[string]any{}
		if err := json.Unmarshal(mapped[name], &entry); err != nil {
			// A bare value rather than an object: the key is the only name there
			// is, and an empty entry still names the field.
			entry = map[string]any{}
		}
		if _, ok := entry["name"]; !ok {
			entry["name"] = name
		}
		out = append(out, entry)
	}
	*a = out
	return nil
}

// Target is one discovery target (c_ad_ci_types): a rule bound to a model.
//
// A RULE SAYS WHAT CAN BE DISCOVERED; A TARGET SAYS WHERE IT LANDS. It names the
// model the results become, the `ad_key → model attribute` mapping that turns a
// provider's field names into this deployment's own, and WHICH WORK RUNS IT —
// either a named agent, or an expression selecting the instances whose agents
// should run it, or neither (which means every agent).
//
// THE THREE-WAY CHOICE IS THE POINT, and cantor's console writes it as three
// modes: `agent_id` names one agent, `query_expr` selects instances, `all`
// leaves both empty and means every collector. The console always sends all
// three fields, with the unused ones as empty strings — so an empty string is a
// VALUE here (it clears a mode), not an absence.
type Target struct {
	cmdbmodel.Base

	// TypeID is the model the discovered instances become. It is a foreign key
	// into c_ci_types and, like every other CMDB foreign key, is an Ongrid-safe
	// 32-bit id rather than a declared constraint: the tables must adopt a
	// cantor database in place, and cantor's constraints are MySQL-only.
	TypeID *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_ad_ci_types_type_id"`

	// AdrID is the rule this target runs.
	AdrID *cmdbmodel.ID `gorm:"column:adr_id;index:idx_c_ad_ci_types_adr_id"`

	// Attributes is the mapping `{ad_key: model attribute name}`. It is the
	// target's whole reason to exist: a provider reports `instance_id`, a model
	// calls it `hostname`, and this is the one place the two are tied together.
	//
	// THE MODEL'S IDENTIFYING ATTRIBUTE MUST APPEAR AMONG THE VALUES, or nothing
	// says which instance a discovered row is about — see the use case, which
	// refuses such a mapping rather than storing a target that can never accept
	// anything.
	Attributes map[string]any `gorm:"column:attributes;serializer:json"`

	// Relation is a deprecated pre-2.4.5 field `[{ad_key: {type_id, attr_id}}]`,
	// superseded by TargetRelation. MODELLED AND NEVER WRITTEN, so that an
	// adopted database reads and so that AutoMigrate does not consider the
	// column foreign. Nothing in this port produces one.
	Relation map[string]any `gorm:"column:relation;serializer:json"`

	// AutoAccept makes the pool accept a discovered instance without a person
	// confirming it. See the pool module.
	AutoAccept bool `gorm:"column:auto_accept;not null;default:false"`

	// AgentID names the one agent that runs this target. Empty means "not this
	// mode" — see QueryExpr.
	AgentID *string `gorm:"column:agent_id;size:64;index:idx_c_ad_ci_types_agent_id"`

	// QueryExpr selects the instances whose agents should run this target. It is
	// a query-language expression, evaluated against the CMDB itself, and it is
	// what makes a rule follow a machine rather than a name.
	QueryExpr *string `gorm:"column:query_expr;type:text"`

	// Interval is seconds between runs, deprecated since 2.4.5 in favour of Cron.
	// Carried because an adopted database has it.
	Interval *cmdbmodel.Int32 `gorm:"column:interval"`

	// Cron is the schedule the agent runs this target on.
	Cron *string `gorm:"column:cron;size:128"`

	// ExtraOption is the target's own settings, including the per-target
	// credential. Its `secret` and `password` entries are ciphertext, and the
	// read path does not return them — see the use case.
	ExtraOption map[string]any `gorm:"column:extra_option;serializer:json"`

	// UID is the Ongrid user the target belongs to. It decides who may replace
	// the target's credential.
	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_ad_ci_types_uid"`

	// Enabled switches the target off without deleting it. A disabled target is
	// not handed to an agent, and its timestamps still count towards what the
	// agent is told has changed — so switching one off reaches the agent.
	Enabled bool `gorm:"column:enabled;not null;default:true"`
}

func (Target) TableName() string { return "c_ad_ci_types" }

// TargetRelation is one discovered relation (c_ad_ci_type_relations).
//
// IT SAYS "THE VALUE THIS TARGET COLLECTS UNDER `ad_key` NAMES AN INSTANCE OF
// `peer_type_id`, MATCHED ON `peer_attr_id`" — that is, it turns a provider's
// reference field into a real CMDB relation rather than a string.
//
// THE TRIPLE IS THE IDENTITY. cantor's upsert keyed on all three, which is why
// the same ad_key may map to several peers and why a row cannot be identified by
// its key alone.
type TargetRelation struct {
	cmdbmodel.Base

	// AdTypeID is the model the target discovers — NOT the target's own id.
	// cantor named it after the CI type and keyed the table by it, so two
	// targets of the same model share one relation set. That is worth keeping:
	// the relations describe the model, and a model with two providers would
	// otherwise need the same rows twice.
	AdTypeID cmdbmodel.ID `gorm:"column:ad_type_id;not null;index:idx_c_ad_ci_type_relations_ad_type_id"`

	// AdKey is the provider's field name — a key of the target's Attributes.
	AdKey *string `gorm:"column:ad_key;size:128"`

	PeerTypeID cmdbmodel.ID `gorm:"column:peer_type_id;not null;index:idx_c_ad_ci_type_relations_peer_type_id"`

	// PeerAttrID is the attribute of the peer that the collected value matches.
	PeerAttrID cmdbmodel.ID `gorm:"column:peer_attr_id;not null;index:idx_c_ad_ci_type_relations_peer_attr_id"`
}

func (TargetRelation) TableName() string { return "c_ad_ci_type_relations" }

// RuleSyncHistory is one record of rules being handed to an agent
// (c_ad_rule_sync_histories).
//
// IT IS APPEND-ONLY, cantor's `Model2` base: a handover happened, and an audit
// row that could be soft-deleted is not an audit row. The one exception is the
// handover's own bookkeeping — cantor DELETES these rows when a target's agent
// binding changes, because they describe a configuration that no longer exists,
// and the port does the same inside the update's transaction.
type RuleSyncHistory struct {
	cmdbmodel.AppendOnlyBase

	// AdtID is the target that was handed over.
	AdtID *cmdbmodel.ID `gorm:"column:adt_id;index:idx_c_ad_rule_sync_histories_adt_id"`

	// OneagentID and OneagentName identify the agent that asked. cantor indexed
	// neither, and the queries here are by target; they are kept unindexed so an
	// adopted database needs no new index.
	OneagentID   *string `gorm:"column:oneagent_id;size:64"`
	OneagentName *string `gorm:"column:oneagent_name;size:64"`

	// SyncAt is when the handover happened. It is its own column rather than
	// created_at, which cantor also kept.
	SyncAt *time.Time `gorm:"column:sync_at"`
}

func (RuleSyncHistory) TableName() string { return "c_ad_rule_sync_histories" }

// Account is one credential a rule uses (c_ad_accounts).
//
// IT IS PER RULE AND PER USER, which is cantor's shape and worth keeping: two
// operators sharing a cloud provider each hold their own keys, and a rule's
// targets run with whichever account their owner attached. The console's
// account list is therefore "my accounts for this provider".
//
// THE CONFIG IS A CREDENTIAL, not a document with one inside it: its `secret`
// and `password` entries are encrypted at rest, and the read paths do not
// return them at all. See the biz layer's account handling.
type Account struct {
	cmdbmodel.Base

	// UID is the account's owner. An account with a secret may only be changed
	// by its owner, which is the one ownership rule cantor enforced here.
	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_ad_accounts_uid"`

	Name string `gorm:"column:name;size:64"`

	// AdrID is the rule the credential belongs to.
	AdrID *cmdbmodel.ID `gorm:"column:adr_id;index:idx_c_ad_accounts_adr_id"`

	// Config holds the credential. Its `secret` and `password` entries are
	// ciphertext.
	Config map[string]any `gorm:"column:config;serializer:json"`
}

func (Account) TableName() string { return "c_ad_accounts" }
