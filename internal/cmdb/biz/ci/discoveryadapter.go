package ci

import (
	"context"
	"fmt"
	"strings"

	discoverybiz "github.com/ongridio/ongrid/internal/cmdb/biz/discovery"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// DiscoveryQueries adapts the instance side to the discovery module's
// consumer-declared port.
//
// IT IS TWO QUESTIONS, and both are the instance module's to answer because both
// are about instances: WHERE an agent runs, and WHETHER an expression selects a
// given instance. The discovery module holds a target's query expression as
// text; deciding what it means needs the attribute dictionary and the compiler,
// which live here.
type DiscoveryQueries struct {
	uc *Usecase
}

// NewDiscoveryQueries builds the adapter.
func NewDiscoveryQueries(uc *Usecase) *DiscoveryQueries {
	return &DiscoveryQueries{uc: uc}
}

var _ discoverybiz.InstanceQueries = (*DiscoveryQueries)(nil)

// The attribute a deployment records an agent's id under.
//
// IT IS NOT A BUILT-IN ATTRIBUTE, which is why the lookup has to tolerate its
// absence: a deployment that does not model agent placement simply has no
// instance to find, and that is an answer.
const (
	agentIDAttribute   = "oneagent_id"
	agentNameAttribute = "oneagent_name"
)

// LocateAgentInstance returns the instance an agent identified itself as running
// on, or zero.
func (q *DiscoveryQueries) LocateAgentInstance(ctx context.Context, oneagentID, oneagentName string) (model.ID, error) {
	agentID := strings.TrimSpace(oneagentID)
	if agentID == "" {
		return 0, nil
	}
	hits, err := q.uc.FindInstancesByAttribute(ctx, agentIDAttribute, []any{agentID}, agentInstanceLookupLimit)
	if err != nil {
		return 0, err
	}
	name := strings.TrimSpace(oneagentName)
	for _, hit := range hits {
		if name == "" {
			// AN AGENT THAT DOES NOT GIVE ITS NAME gets the first instance that
			// claims its id. Identical to cantor's, which only compared names when
			// it had one.
			return hit.ID, nil
		}
		// THE INSTANCE'S OWN NAME, or its `oneagent_name` if it carries one —
		// either identifies it, and cantor accepted both.
		if valueMatchesName(hit, name) {
			return hit.ID, nil
		}
	}
	return 0, nil
}

// agentInstanceLookupLimit bounds the placement lookup.
//
// AN AGENT ID BELONGS TO ONE MACHINE, and a deployment whose ids are unique in
// practice will match on the first or second hit. The bound is there because the
// attribute is a deployment's own and nothing in the schema makes it unique — an
// operator who set the same id on every host would otherwise have this endpoint
// load the estate on every poll.
const agentInstanceLookupLimit = 200

// valueMatchesName reports whether a located instance carries the agent's name.
func valueMatchesName(hit SearchHit, name string) bool {
	if unique := strings.TrimSpace(hit.UniqueAttr); unique != "" {
		if strings.TrimSpace(hit.UniqueAlias) != "" && hit.Values[hit.UniqueAlias] != nil {
			if equalText(hit.Values[hit.UniqueAlias], name) {
				return true
			}
		}
		if equalText(hit.Values[unique], name) {
			return true
		}
	}
	return equalText(hit.Values[agentNameAttribute], name)
}

// MatchInstance reports whether an expression selects one instance.
func (q *DiscoveryQueries) MatchInstance(ctx context.Context, expr string, ciID model.ID) (bool, error) {
	return q.uc.InstanceMatches(ctx, expr, ciID)
}

// equalText reports whether a rendered value is the given name.
func equalText(raw any, want string) bool {
	switch v := raw.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(v) == want
	case []any:
		// A MULTI-VALUED ATTRIBUTE ARRIVES AS A LIST, and "does this instance
		// carry that name" is then a membership test rather than an equality —
		// which is the same reading that makes an `IN` match a value row rather
		// than a cell.
		for _, item := range v {
			if text, ok := item.(string); ok && strings.TrimSpace(text) == want {
				return true
			}
		}
		return false
	}
	return strings.TrimSpace(fmt.Sprint(raw)) == want
}

// DiscoveryWriter adapts the discovery accept path to the instance write.
//
// IT RESOLVES THE PEERS AND THE MODEL PAIR'S RULE, because both are instance-
// side questions: which instances an attribute value selects, and whether two
// models may be related at all. The discovery module holds the field values and
// the model ids; it should not learn how to search for a peer.
type DiscoveryWriter struct {
	uc *Usecase
}

// NewDiscoveryWriter builds the adapter.
func NewDiscoveryWriter(uc *Usecase) *DiscoveryWriter { return &DiscoveryWriter{uc: uc} }

var _ discoverybiz.InstanceWriter = (*DiscoveryWriter)(nil)

// CIExists reports whether an accepted sighting's instance still exists.
func (w *DiscoveryWriter) CIExists(ctx context.Context, id model.ID) (bool, error) {
	if id <= 0 {
		return false, nil
	}
	ci, err := w.uc.repo.LoadCI(ctx, id)
	if err != nil {
		return false, fmt.Errorf("cmdb ci: check discovered instance %d: %w", id, err)
	}
	return ci != nil, nil
}

// WriteDiscovered writes one accepted sighting.
//
// THE INSTANCE AND ITS EDGES ARE ONE CALL. The edges are resolved here, not in
// the store, and handed to WriteWithRelations so the store can write both in
// the transaction it already holds.
func (w *DiscoveryWriter) WriteDiscovered(ctx context.Context, in discoverybiz.DiscoveredInstance) (*discoverybiz.DiscoveredResult, error) {
	if w == nil || w.uc == nil {
		return nil, errs.ErrNotWiredYet
	}
	if in.TypeID <= 0 || len(in.Values) == 0 {
		return nil, errs.ErrInvalid
	}
	relations, err := w.resolveRelations(ctx, in)
	if err != nil {
		return nil, err
	}
	res, err := w.uc.WriteWithRelations(ctx, WriteRequest{
		TypeRef:     fmt.Sprint(in.TypeID),
		Values:      in.Values,
		ExistPolicy: ExistReplace,
		// A FIELD MAPPED TO AN ATTRIBUTE THAT NO LONGER EXISTS IS DROPPED, not
		// a reason to refuse the machine: a target can outlive a model edit,
		// and the accept's job is the fields the model still has.
		NoAttributePolicy: ExistIgnore,
		Audit:             AuditInfo{Actor: in.Actor, Origin: "discovery"},
	}, relations, true)
	if err != nil {
		return nil, err
	}
	return &discoverybiz.DiscoveredResult{CIID: res.CIID, Created: res.Created}, nil
}

// resolveRelations turns each declared relation into the edges its values
// select.
//
// A PAIR WITH NO DECLARED RULE IS SKIPPED, as is a value that matches no
// instance: a relation is a hint about the estate, and refusing the whole
// accept over one missing switch would lose a machine that is otherwise fine.
// cantor swallowed the same failures, one at a time.
func (w *DiscoveryWriter) resolveRelations(ctx context.Context, in discoverybiz.DiscoveredInstance) ([]RelationSpec, error) {
	if len(in.Relations) == 0 {
		return nil, nil
	}
	out := make([]RelationSpec, 0, len(in.Relations))
	seen := map[string]bool{}
	for _, relation := range in.Relations {
		if relation.PeerTypeID == 0 || relation.PeerAttrID == 0 || len(relation.Values) == 0 {
			continue
		}
		peerSet, err := w.uc.types.ResolveForCI(ctx, fmt.Sprint(relation.PeerTypeID))
		if err != nil {
			return nil, err
		}
		if peerSet == nil {
			continue
		}
		peerAttr, ok := peerSet.Specs[relation.PeerAttrID]
		if !ok {
			continue
		}

		// THE RULE IS RESOLVED ONCE PER DECLARATION, not once per peer: the
		// direction belongs to the model pair, and a field that selects a
		// hundred instances has one answer.
		rule, ok, err := w.uc.types.RelationBetween(ctx, in.TypeID, relation.PeerTypeID)
		if err != nil {
			return nil, err
		}
		discoveredIsFirst := ok
		if !ok {
			rule, ok, err = w.uc.types.RelationBetween(ctx, relation.PeerTypeID, in.TypeID)
			if err != nil {
				return nil, err
			}
			discoveredIsFirst = false
		}
		if !ok {
			continue
		}

		// THE PEER LOOKUP IS BOUNDED by the CMDB's maximum page size. A field
		// that selects more instances than one page loses the tail of its
		// edges — a relation is a convenience beside the accepted instance, and
		// loading an unbounded result inside an accept is worse than a missing
		// edge. cantor asked for a million; this asks for one page.
		hits, err := w.uc.FindInstancesByAttribute(ctx, peerAttr.Name, relation.Values, MaxInstancePageSize)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			// THE LOOKUP IS CROSS-MODEL: `hostname` exists on several models, and
			// only the declared peer model's instances are endpoints of this
			// edge.
			if hit.TypeID != relation.PeerTypeID {
				continue
			}
			key := fmt.Sprintf("%d:%t", hit.ID, discoveredIsFirst)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, RelationSpec{
				PeerCIID:          hit.ID,
				PeerTypeID:        relation.PeerTypeID,
				DiscoveredIsFirst: discoveredIsFirst,
				DiscoveredTypeID:  in.TypeID,
				RelationTypeID:    rule.RelationTypeID,
				Constraint:        rule.Constraint,
			})
		}
	}
	return out, nil
}
