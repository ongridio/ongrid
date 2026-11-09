package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ongridio/ongrid/internal/cmdb/biz/discovery/catalog"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The target layer: where a rule lands.
//
// A TARGET IS THE JOIN BETWEEN A PROVIDER AND A MODEL, and everything here is
// about the two questions that join raises: WHICH fields the provider reports
// (so the operator can map them — see AdKeys), and WHICH attribute identifies an
// instance (so a second sighting of a machine is the same machine — see
// ErrTargetMappingNeedsUniqueKey).

// ModelResolver answers the model-side questions a target needs.
//
// THE PORT IS DECLARED HERE, by the consumer, and implemented by the model
// package's adapter — the same shape the instance side uses for its attribute
// sets, and for the same reason: the rule that an effective attribute set spans
// a model's whole inheritance chain must exist in exactly one place, and it is
// not this package.
//
// IT IS SMALL ON PURPOSE. A target needs a model's id and the name of its
// identifying attribute; it does not need the model, its attributes, or its
// relations.
type ModelResolver interface {
	// ResolveCIType finds a model by numeric id, exact name or alias, or returns
	// (nil, nil) when nothing matches.
	ResolveCIType(ctx context.Context, ref string) (*CITypeRef, error)
}

// CITypeRef is the model-side answer a target needs.
type CITypeRef struct {
	ID    model.ID
	Name  string
	Alias string
	Icon  string

	// UniqueKey is the NAME of the attribute that identifies an instance of this
	// model. Empty when the model declares none, which makes a target impossible
	// to configure — see ErrTargetMappingNeedsUniqueKey.
	UniqueKey string
}

// Target is one discovery target as the API presents it.
//
// IT CARRIES NO CREDENTIAL. `extra_option` is returned without its `secret` and
// `password` entries, with flags saying whether each is set — the same posture
// the rule accounts and the password attributes take. The ONE path that needs
// the plaintext is the agent's poll, and it is the only one that decrypts.
type Target struct {
	ID     model.ID `json:"id"`
	TypeID model.ID `json:"type_id,omitempty"`
	AdrID  model.ID `json:"adr_id,omitempty"`

	// Attributes is the `ad_key → model attribute` mapping.
	Attributes map[string]any `json:"attributes"`

	AutoAccept bool `json:"auto_accept"`

	// AgentID and QueryExpr are the two ways of naming who runs this. Cantor's
	// console writes all three of these always, with empty strings for the modes
	// it is not using, so they are returned as strings rather than omitted.
	AgentID   string `json:"agent_id"`
	QueryExpr string `json:"query_expr"`
	Cron      string `json:"cron"`

	// Interval is the deprecated seconds-between-runs field. Returned when set,
	// so an adopted target's value is visible and can be cleared.
	Interval *model.Int32 `json:"interval,omitempty"`

	ExtraOption map[string]any `json:"extra_option"`

	HasSecret   bool `json:"has_secret"`
	HasPassword bool `json:"has_password"`

	UID     *model.ID `json:"uid,omitempty"`
	Enabled bool      `json:"enabled"`

	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`

	// The rule's own labels, attached so the console's row does not have to look
	// the rule up in a second list to draw itself.
	RuleName string `json:"adr_name,omitempty"`
	RuleType string `json:"adr_type,omitempty"`
	IsPlugin bool   `json:"is_plugin,omitempty"`

	// RuleMissing says the rule this target names is gone. The delete paths
	// cannot produce it — a rule a target uses is not removable — but an adopted
	// database can hold one, and a console row with no provider should say why
	// rather than render blank.
	RuleMissing bool `json:"adr_missing,omitempty"`
}

// TargetInput is one write of a target.
//
// THE OPTIONALS ARE POINTERS OR FLAGGED SETS: an omitted field leaves the stored
// value alone, a supplied empty one writes it. That distinction carries more
// weight here than anywhere else in the module, because the console sends
// `agent_id: ""` and `query_expr: ""` to mean "not this mode" — a write that
// read those as an absence would leave a target bound to an agent the operator
// had just unbound.
type TargetInput struct {
	AdrID *model.ID

	Attributes    map[string]any
	SetAttributes bool

	AutoAccept *bool
	AgentID    *string
	QueryExpr  *string
	Cron       *string
	Interval   *model.Int32

	ExtraOption    map[string]any
	SetExtraOption bool

	Enabled *bool
}

// TargetRelation is one discovered relation as the API presents it.
type TargetRelation struct {
	ID         model.ID `json:"id,omitempty"`
	AdKey      string   `json:"ad_key,omitempty"`
	PeerTypeID model.ID `json:"peer_type_id,omitempty"`
	PeerAttrID model.ID `json:"peer_attr_id,omitempty"`
}

// SyncHistory is one target-to-agent handover.
type SyncHistory struct {
	ID           model.ID `json:"id"`
	AdtID        model.ID `json:"adt_id,omitempty"`
	OneagentID   string   `json:"oneagent_id,omitempty"`
	OneagentName string   `json:"oneagent_name,omitempty"`

	SyncAt    string `json:"sync_at,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// SyncHistoryPage is one page of a target's handover log.
type SyncHistoryPage struct {
	Items    []SyncHistory `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// Bounds on the handover log.
//
// THE CONSOLE ASKS FOR TEN THOUSAND, and the honest ceiling is the number of
// agents: the log is one row per (target, agent) PAIR, because the write is an
// upsert rather than an append. Two thousand is headroom over any deployment's
// agent count, and the response carries the real total beside the page so a
// truncated read is visible rather than merely short — see MaxFacetBuckets for
// the same reasoning about a bound the caller cannot see.
const (
	DefaultSyncHistoryPageSize = 50
	MaxSyncHistoryPageSize     = 2000
)

// BuiltinRuleResult is what the built-in rule bootstrap did.
type BuiltinRuleResult struct {
	Created []string `json:"created"`
	// Existing names the built-in rules that were already there. They are
	// reported rather than silently skipped, because "the bootstrap found
	// everything already" and "the bootstrap found nothing to do" are different
	// answers to "why is my ADT page empty".
	Existing []string `json:"existing"`
}

// WithModels supplies the model-side resolver.
//
// IT IS A BUILDER RATHER THAN A CONSTRUCTOR PARAMETER so the composition root
// changes additively: NewUsecase keeps working, and a deployment that wires no
// resolver gets ErrNotWiredYet from the target routes rather than a panic.
func (u *Usecase) WithModels(resolver ModelResolver) *Usecase {
	u.models = resolver
	return u
}

// ListTargets returns one model's targets.
// TargetTypeCount is one model that has discovery targets.
type TargetTypeCount struct {
	TypeID      model.ID `json:"type_id"`
	Name        string   `json:"name,omitempty"`
	Alias       string   `json:"alias,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	TargetCount int      `json:"target_count"`
}

// ListTargetTypes returns the models that have at least one target, with the
// model label and icon the console needs for the pool tree.
//
// THE COUNT IS DERIVED FROM THE TARGET ROWS rather than a counter table: a
// deployment has tens of targets, not millions, and cantor's c_ad_counter was a
// cache that could disagree with the rows it described.
func (u *Usecase) ListTargetTypes(ctx context.Context) ([]TargetTypeCount, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	targets, err := u.repo.ListAllTargets(ctx)
	if err != nil {
		return nil, err
	}
	counts := map[model.ID]int{}
	for _, target := range targets {
		if target.TypeID != nil {
			counts[*target.TypeID]++
		}
	}
	ids := make([]model.ID, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	out := make([]TargetTypeCount, 0, len(ids))
	for _, id := range ids {
		item := TargetTypeCount{TypeID: id, TargetCount: counts[id]}
		if u.models != nil {
			if ref, err := u.models.ResolveCIType(ctx, fmt.Sprint(id)); err != nil {
				return nil, err
			} else if ref != nil {
				item.Name, item.Alias, item.Icon = ref.Name, ref.Alias, ref.Icon
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func (u *Usecase) ListTargets(ctx context.Context, typeRef string) ([]Target, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	typeID, _, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	rows, err := u.repo.ListTargets(ctx, typeID)
	if err != nil {
		return nil, err
	}
	return u.renderTargets(ctx, rows)
}

// GetTarget returns one target by id.
func (u *Usecase) GetTarget(ctx context.Context, id model.ID) (*Target, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	row, err := u.repo.LoadTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("%w: %d", ErrTargetNotFound, id)
	}
	views, err := u.renderTargets(ctx, []discoverymodel.Target{*row})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// CreateTarget binds a rule to a model.
func (u *Usecase) CreateTarget(ctx context.Context, typeRef string, in TargetInput, actor Actor) (*Target, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	typeID, ref, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	_ = typeID
	write, err := u.prepareTarget(ctx, ref, in, actor, nil)
	if err != nil {
		return nil, err
	}
	write.TypeID = ref.ID
	write.UID = actor.UserID

	row, err := u.repo.CreateTarget(ctx, write)
	if err != nil {
		return nil, err
	}
	views, err := u.renderTargets(ctx, []discoverymodel.Target{*row})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// UpdateTarget writes a target's fields.
func (u *Usecase) UpdateTarget(ctx context.Context, id model.ID, in TargetInput, actor Actor) (*Target, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %d", ErrTargetNotFound, id)
	}
	ref, err := u.refOf(ctx, *existing)
	if err != nil {
		return nil, err
	}
	write, err := u.prepareTarget(ctx, ref, in, actor, existing)
	if err != nil {
		return nil, err
	}

	row, err := u.repo.UpdateTarget(ctx, id, write)
	if err != nil {
		return nil, err
	}
	views, err := u.renderTargets(ctx, []discoverymodel.Target{*row})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// DeleteTarget removes a target, its handover log, and the relations of its
// model that no other target can produce.
func (u *Usecase) DeleteTarget(ctx context.Context, id model.ID, actor Actor) (*TargetDeleteResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	existing, err := u.repo.LoadTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("%w: %d", ErrTargetNotFound, id)
	}
	// DELETING A CREDENTIAL IS AN OWNERSHIP ACT, which cantor allowed anyone to
	// do: its delete path checked nothing, so a second operator could remove the
	// target holding the first operator's cloud key. A target that carries no
	// credential is shared configuration and anybody may remove it, which keeps
	// the common case exactly as cantor had it.
	if targetHasCredential(*existing) && !ownsTarget(*existing, actor) {
		return nil, fmt.Errorf("%w: target %d holds a credential", ErrAccountSecretNotYours, id)
	}
	return u.repo.DeleteTarget(ctx, id)
}

// ListTargetRelations returns one model's discovered relations.
func (u *Usecase) ListTargetRelations(ctx context.Context, typeRef string) ([]TargetRelation, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	typeID, _, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}
	rows, err := u.repo.ListTargetRelations(ctx, typeID)
	if err != nil {
		return nil, err
	}
	return renderRelations(rows), nil
}

// ReplaceTargetRelations makes a model's relation set exactly the ones given.
//
// A FULL REPLACE, which is what cantor's upsert was and what the console sends:
// one form whose save is the whole set. The alternative — per-relation calls —
// makes removing one a request the console has to remember to make.
func (u *Usecase) ReplaceTargetRelations(ctx context.Context, typeRef string, in []TargetRelationWrite, actor Actor) (*RelationReplaceResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	typeID, _, err := u.resolveType(ctx, typeRef)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	writes := make([]TargetRelationWrite, 0, len(in))
	for i, item := range in {
		write := TargetRelationWrite{
			AdKey:      strings.TrimSpace(item.AdKey),
			PeerTypeID: item.PeerTypeID,
			PeerAttrID: item.PeerAttrID,
		}
		// ALL THREE PARTS OR NONE. cantor tested `len(list(filter(lambda x: x, k))) == 3`
		// and silently skipped anything short of that — so a half-filled row
		// vanished from a save that reported success.
		if write.AdKey == "" || write.PeerTypeID == 0 || write.PeerAttrID == 0 {
			return nil, fmt.Errorf("%w: relation %d", ErrTargetRelationIncomplete, i+1)
		}
		key := relationKey(write)
		if seen[key] {
			return nil, fmt.Errorf("%w: %s appears twice", ErrTargetRelationIncomplete, write.AdKey)
		}
		seen[key] = true
		writes = append(writes, write)
	}
	return u.repo.ReplaceTargetRelations(ctx, typeID, writes)
}

// ListSyncHistories returns one target's handover log, newest first.
func (u *Usecase) ListSyncHistories(ctx context.Context, id model.ID, page, size int) (*SyncHistoryPage, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = DefaultSyncHistoryPageSize
	}
	if size > MaxSyncHistoryPageSize {
		size = MaxSyncHistoryPageSize
	}
	if _, err := u.GetTarget(ctx, id); err != nil {
		return nil, err
	}
	rows, total, err := u.repo.ListSyncHistories(ctx, id, size, (page-1)*size)
	if err != nil {
		return nil, err
	}
	out := &SyncHistoryPage{
		Items:    make([]SyncHistory, 0, len(rows)),
		Total:    total,
		Page:     page,
		PageSize: size,
	}
	for _, row := range rows {
		out.Items = append(out.Items, renderSyncHistory(row))
	}
	return out, nil
}

// EnsureBuiltinRules creates the discovery rules the product ships with.
//
// ⚠️ THIS IS THE ONE THING THAT MAKES A FRESH DEPLOYMENT USABLE, and it is by
// no means obvious: cantor has no bootstrap for these rows at all. Its rule-list
// endpoint SELF-HEALS — `GET /adr` inserts any built-in provider that is missing
// before answering — so a deployment's provider list appears the first time
// somebody opens the page, and a fresh database has no rules at all until then.
//
// A READ THAT WRITES IS THE WRONG PLACE FOR IT. Two concurrent list calls race
// to insert the same rows, a GET is retried by proxies and prefetched by
// browsers, and an endpoint whose effect is a schema-level seed should be
// something an operator chose to run. This is that: EXPLICIT, IDEMPOTENT, and it
// ONLY ADDS — a rule that exists is reported and left alone, so an operator's
// edit to a built-in provider is never reverted. Same posture as the built-in
// models bootstrap for the data centre and address modules.
func (u *Usecase) EnsureBuiltinRules(ctx context.Context, actor Actor) (*BuiltinRuleResult, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	providers, err := catalog.Providers()
	if err != nil {
		return nil, err
	}
	existing, err := u.repo.ListRules(ctx)
	if err != nil {
		return nil, err
	}
	have := make(map[string]bool, len(existing))
	for _, row := range existing {
		have[strings.ToLower(strings.TrimSpace(row.Name))] = true
	}

	out := &BuiltinRuleResult{Created: []string{}, Existing: []string{}}
	for _, provider := range providers {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			continue
		}
		if have[strings.ToLower(name)] {
			out.Existing = append(out.Existing, name)
			continue
		}
		kind := provider.Type
		plugin := provider.IsPlugin
		inner := true
		option := map[string]any{}
		for k, v := range provider.Option {
			option[k] = v
		}
		// cantor's self-heal writes `en` into `option` and drops it from the
		// top level, because the rule table has no such column. A rule's provider
		// key is read back from `option.en` first for exactly this reason.
		if provider.EN != "" {
			option["en"] = provider.EN
		}
		write, err := u.ruleWrite(RuleInput{
			Name: name, Type: &kind, IsPlugin: &plugin,
			Option: option, SetOption: true,
		}, actor, false)
		if err != nil {
			return nil, err
		}
		write.Owner = actor.UserID
		write.IsInner = &inner
		if _, err := u.repo.CreateRule(ctx, write); err != nil {
			return nil, err
		}
		out.Created = append(out.Created, name)
	}
	return out, nil
}

// prepareTarget validates one write into a store write.
func (u *Usecase) prepareTarget(ctx context.Context, ref *CITypeRef, in TargetInput, actor Actor, existing *discoverymodel.Target) (TargetWrite, error) {
	write := TargetWrite{
		AutoAccept: in.AutoAccept,
		AgentID:    in.AgentID,
		QueryExpr:  in.QueryExpr,
		Cron:       in.Cron,
		Interval:   in.Interval,
		Enabled:    in.Enabled,
	}

	// --- the rule ---
	// THE RULE DECIDES TWO THINGS: whether the target is usable at all, and how
	// `extra_option` is derived below. Both need it loaded, and an update that
	// does not mention the binding still needs the one it already has.
	var rule *discoverymodel.Rule
	if in.AdrID != nil {
		write.AdrID = in.AdrID
		if *in.AdrID != 0 {
			row, err := u.repo.LoadRule(ctx, *in.AdrID)
			if err != nil {
				return TargetWrite{}, err
			}
			if row == nil {
				return TargetWrite{}, fmt.Errorf("%w: rule %d", ErrRuleNotFound, *in.AdrID)
			}
			rule = row
		}
	} else if existing != nil && existing.AdrID != nil && *existing.AdrID != 0 {
		row, err := u.repo.LoadRule(ctx, *existing.AdrID)
		if err != nil {
			return TargetWrite{}, err
		}
		rule = row
	}

	// --- the mode ---
	// THE RESULTING STATE, not the request: a write that mentions only the
	// mapping must not be able to leave a target that runs everywhere without the
	// administrator check that creating one requires.
	agentID := derefString(in.AgentID)
	queryExpr := derefString(in.QueryExpr)
	if in.AgentID == nil && existing != nil {
		agentID = derefString(existing.AgentID)
	}
	if in.QueryExpr == nil && existing != nil {
		queryExpr = derefString(existing.QueryExpr)
	}
	if strings.TrimSpace(agentID) == "" && strings.TrimSpace(queryExpr) == "" && !actor.IsAdmin {
		return TargetWrite{}, ErrTargetCatchAllNeedsAdmin
	}

	// --- the mapping ---
	if in.SetAttributes || existing == nil {
		write.Attributes, write.SetAttributes = in.Attributes, true
		if err := checkUniqueMapped(ref, in.Attributes); err != nil {
			return TargetWrite{}, err
		}
	}

	// --- extra_option, which is where the credential and the collector live ---
	if in.SetExtraOption {
		prior := map[string]any(nil)
		if existing != nil {
			prior = existing.ExtraOption
		}
		if existing != nil && !mayWriteCredentialOf(*existing, in.ExtraOption, actor) {
			return TargetWrite{}, fmt.Errorf("%w: target %d", ErrAccountSecretNotYours, existing.ID)
		}
		sealed, err := sealAccountConfig(in.ExtraOption, prior)
		if err != nil {
			return TargetWrite{}, err
		}
		if rule != nil {
			if err := applyProviderOptions(*rule, sealed); err != nil {
				return TargetWrite{}, err
			}
		}
		write.ExtraOption, write.SetExtraOption = sealed, true
	}

	if existing != nil && existing.UID != nil {
		write.UID = *existing.UID
	}
	return write, nil
}

// checkUniqueMapped refuses a mapping that does not say which attribute
// identifies an instance.
//
// IT IS THE WHOLE POINT OF A MAPPING. Without the model's identifying attribute
// among the mapped values, nothing decides whether a reported row is a machine
// this deployment has never seen or a second sighting of one it knows, so the
// target could only ever create duplicates — or overwrite whichever instance
// happened to sort first.
func checkUniqueMapped(ref *CITypeRef, attributes map[string]any) error {
	if ref == nil || ref.UniqueKey == "" {
		// A model with no identifying attribute cannot be discovered at all;
		// cantor skipped the check in the same case rather than refusing, and a
		// model in that state has larger problems than this endpoint.
		return nil
	}
	for _, mapped := range attributes {
		if strings.TrimSpace(fmt.Sprint(mapped)) == ref.UniqueKey {
			return nil
		}
	}
	return fmt.Errorf("%w: %s must appear among the mapped attributes of %s",
		ErrTargetMappingNeedsUniqueKey, ref.UniqueKey, ref.Name)
}

// applyProviderOptions fills in the keys an agent needs in order to collect.
//
// THE AGENT IS TOLD WHICH COLLECTOR TO RUN, not left to infer it from a
// provider's display name: a cloud target carries `collect_key` (`ali.ecs`) and
// `provider` (`aliyun`), a component target carries its component's collect key.
// cantor derived them from the catalogue on every write, and so does this —
// which is also why the derivation lives here and not in the console.
func applyProviderOptions(rule discoverymodel.Rule, option map[string]any) error {
	if option == nil {
		return nil
	}
	provider, found, err := catalog.ProviderByName(rule.Name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	switch derefString(rule.Type) {
	case discoverymodel.TypeHTTP:
		resource := ""
		if raw, ok := option["category"].(string); ok {
			resource = strings.TrimSpace(raw)
		}
		if resource == "" {
			return nil
		}
		key, err := catalog.CollectKey(provider.ProviderEN(), resource)
		if err != nil {
			return err
		}
		// ONLY WHEN THE PROVIDER OFFERS THAT RESOURCE. An unknown category leaves
		// both keys alone rather than writing half a pair.
		if key != "" {
			option["collect_key"] = key
			option["provider"] = provider.ProviderEN()
		}
	case discoverymodel.TypeComponents:
		if key, ok := provider.Option["collect_key"].(string); ok && strings.TrimSpace(key) != "" {
			option["collect_key"] = strings.TrimSpace(key)
		}
	}
	return nil
}

// resolveType resolves a model reference, reporting what the model side said.
func (u *Usecase) resolveType(ctx context.Context, ref string) (model.ID, *CITypeRef, error) {
	if u.models == nil {
		return 0, nil, errs.ErrNotWiredYet
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, nil, fmt.Errorf("%w: ci_type", errs.ErrInvalid)
	}
	found, err := u.models.ResolveCIType(ctx, ref)
	if err != nil {
		return 0, nil, err
	}
	if found == nil {
		return 0, nil, fmt.Errorf("%w: model %s", errs.ErrNotFound, ref)
	}
	return found.ID, found, nil
}

// refOf resolves the model a stored target belongs to.
func (u *Usecase) refOf(ctx context.Context, row discoverymodel.Target) (*CITypeRef, error) {
	if row.TypeID == nil {
		return nil, fmt.Errorf("%w: target %d names no model", ErrTargetIncomplete, row.ID)
	}
	_, ref, err := u.resolveType(ctx, fmt.Sprint(*row.TypeID))
	return ref, err
}

// renderTargets turns stored rows into the API's shape, loading the rules once.
func (u *Usecase) renderTargets(ctx context.Context, rows []discoverymodel.Target) ([]Target, error) {
	if len(rows) == 0 {
		return []Target{}, nil
	}
	rules, err := u.rulesByID(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(rows))
	for _, row := range rows {
		view := renderTarget(row)
		if rule, ok := rules[derefID(row.AdrID)]; ok {
			view.RuleName = rule.Name
			view.RuleType = derefString(rule.Type)
			view.IsPlugin = rule.IsPlugin
		} else if row.AdrID != nil && *row.AdrID != 0 {
			view.RuleMissing = true
		}
		out = append(out, view)
	}
	return out, nil
}

// renderTarget strips the credentials and reports whether they were there.
//
// THE STRIPPING LIVES IN ONE PLACE, like the account renderer's, so that no
// endpoint can forget it.
func renderTarget(row discoverymodel.Target) Target {
	view := Target{
		ID:          row.ID,
		TypeID:      derefID(row.TypeID),
		AdrID:       derefID(row.AdrID),
		Attributes:  map[string]any{},
		AutoAccept:  row.AutoAccept,
		AgentID:     derefString(row.AgentID),
		QueryExpr:   derefString(row.QueryExpr),
		Cron:        derefString(row.Cron),
		Interval:    row.Interval,
		ExtraOption: map[string]any{},
		UID:         row.UID,
		Enabled:     row.Enabled,
		CreatedAt:   cmdbTime(row.CreatedAt),
		UpdatedAt:   cmdbTime(row.UpdatedAt),
	}
	for k, v := range row.Attributes {
		view.Attributes[k] = v
	}
	for k, v := range row.ExtraOption {
		switch k {
		case AccountSecretKey:
			view.HasSecret = isSet(v)
		case AccountPasswordKey:
			view.HasPassword = isSet(v)
		default:
			view.ExtraOption[k] = v
		}
	}
	return view
}

// renderRelations turns stored relation rows into the API's shape.
func renderRelations(rows []discoverymodel.TargetRelation) []TargetRelation {
	out := make([]TargetRelation, 0, len(rows))
	for _, row := range rows {
		out = append(out, TargetRelation{
			ID: row.ID, AdKey: derefString(row.AdKey),
			PeerTypeID: row.PeerTypeID, PeerAttrID: row.PeerAttrID,
		})
	}
	return out
}

// renderSyncHistory turns one stored handover record into the API's shape.
func renderSyncHistory(row discoverymodel.RuleSyncHistory) SyncHistory {
	view := SyncHistory{
		ID:           row.ID,
		AdtID:        derefID(row.AdtID),
		OneagentID:   derefString(row.OneagentID),
		OneagentName: derefString(row.OneagentName),
		CreatedAt:    cmdbTime(row.CreatedAt),
	}
	if row.SyncAt != nil {
		view.SyncAt = cmdbTime(*row.SyncAt)
	}
	return view
}

// relationKey is a stored relation's identity, which is the whole triple.
func relationKey(in TargetRelationWrite) string {
	return fmt.Sprintf("%s\x00%d\x00%d", in.AdKey, in.PeerTypeID, in.PeerAttrID)
}

// targetHasCredential reports whether a target holds a sealed secret.
func targetHasCredential(row discoverymodel.Target) bool {
	for _, key := range credentialKeys {
		if isSet(row.ExtraOption[key]) {
			return true
		}
	}
	return false
}

// mayWriteCredentialOf reports whether a caller may replace a target's
// credential — the same rule the accounts take, so the module has one answer to
// "whose secret is this".
func mayWriteCredentialOf(existing discoverymodel.Target, incoming map[string]any, actor Actor) bool {
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
	return ownsTarget(existing, actor)
}

// ownsTarget reports whether the caller owns a target. A target with no owner is
// nobody's and is an administrative object, matching the account rule.
func ownsTarget(row discoverymodel.Target, actor Actor) bool {
	if row.UID == nil {
		return actor.IsAdmin
	}
	return *row.UID == actor.UserID || actor.IsAdmin
}

// cmdbTime renders a timestamp the way cantor's JSON encoder did.
//
// THE FORMAT IS PART OF THE CONTRACT rather than a display choice: an agent's
// `last_update_at` is COMPARED AS A STRING on its next poll, so a value in
// another format would either look newer than everything forever or never look
// newer at all.
func cmdbTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}
