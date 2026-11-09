package ci

import (
	"context"
	"fmt"
	"strings"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// MaxFacetBuckets bounds how many values one facet returns.
//
// cantor returned every bucket, with no bound at all, over whatever attribute
// the caller named. On a model with a million instances and a distinct hostname
// per instance that is a million rows assembled in the request, which is a
// capacity problem rather than a search feature — and the console renders a
// filtered list of a few dozen at most. The cap is applied per facet.
//
// It is a documented deviation, not a silent one: the caller sees at most this
// many buckets and the service logs when the cap bites. The alternative —
// returning everything — cannot be defended for a request any authenticated
// caller can send.
const MaxFacetBuckets = 500

// SearchRequest is one search over the instance set.
type SearchRequest struct {
	// Query is the `q` expression. An empty expression matches every instance,
	// which is what the console sends when a filter is cleared.
	Query string

	// Sort is the `sort` parameter, e.g. "-_updated_at" or "cpu" or "-1".
	Sort string

	// Facets are the attributes to aggregate the matches by.
	Facets []string

	// Fields projects the returned values; empty returns every readable one.
	Fields []string

	// RetKey chooses how the returned value maps are keyed.
	RetKey RetKey

	// Page is 1-based.
	Page int

	// Count is the page size; zero takes the default.
	Count int
}

// SearchHit is one instance in a search result.
//
// It carries the identifying fields cantor's result rows carried, because the
// console reads them: `ci_type` names the model in a result set that spans
// several, and `unique` names the attribute that identifies an instance of it.
type SearchHit struct {
	Instance

	TypeName  string
	TypeAlias string

	UniqueAttr  string
	UniqueAlias string

	// UpdatedBy is the CI row's own column. cantor carried it as a built-in
	// result field (`_updated_by`) alongside the model's attributes, because
	// the console shows it as a column that no model declares.
	UpdatedBy string
}

// SearchPage is one page of search results.
type SearchPage struct {
	Hits []SearchHit

	// NumFound is the total number of matching instances, before paging.
	NumFound int64

	// Page is the 1-based page that was returned, after clamping.
	Page int

	// Facets is keyed by the attribute's ret_key, exactly as cantor keyed it,
	// and each list is ordered by descending count.
	Facets map[string][]FacetBucket

	// Counter is how many of THIS page's hits belong to each model, keyed by
	// the model's name. cantor sent it so the console could label a mixed
	// result set; it counts the page, not the whole match set.
	Counter map[string]int
}

// Search runs a query expression over the instance set.
//
// THE THREE PHASES ARE SEPARATE ON PURPOSE. Parsing is pure and knows nothing
// about the dictionary; resolving needs the dictionary and nothing about
// storage; executing needs the dialect and nothing about the language. That
// split is what lets the language be tested against cantor's own output
// without a database, and it is why the dictionary is loaded from the
// references the parse produced rather than from the whole attribute table —
// a query naming one attribute of a two-hundred-attribute model reads one row.
func (u *Usecase) Search(ctx context.Context, req SearchRequest) (*SearchPage, error) {
	return u.searchWith(ctx, req, nil)
}

// InstanceMatches reports whether an expression selects one instance.
//
// THE QUESTION IS MEMBERSHIP, NOT A LIST, and that is the whole point of having
// this method rather than reusing Search. cantor answered it by running the
// expression at `count=1000000` and then looking for the instance among the
// results — so "does this rule apply to this host" cost a full materialisation
// of the rule's entire match set, once per target, on every agent poll. The same
// question asked as a restriction answers with at most one row.
//
// The restriction is applied the way the hierarchy walk's is: an id set the
// caller cannot express in the query language, merged last so the expression
// cannot evade it.
func (u *Usecase) InstanceMatches(ctx context.Context, expr string, ciID model.ID) (bool, error) {
	if u.repo == nil || u.types == nil {
		return false, errs.ErrNotWiredYet
	}
	if ciID <= 0 {
		return false, errs.ErrInvalid
	}
	page, err := u.searchWithLimit(ctx, SearchRequest{Query: expr, Count: 1},
		&search.IDTerm{IDs: []model.ID{ciID}}, 1)
	if err != nil {
		return false, err
	}
	for _, hit := range page.Hits {
		if hit.ID == ciID {
			return true, nil
		}
	}
	return false, nil
}

// FindInstancesByAttribute returns every instance whose named attribute holds
// one of the given values, keyed by name.
//
// IT EXISTS FOR THE AGENT PLACEMENT LOOKUP, which is the one question the
// discovery module asks that is not a query expression: "which instance is the
// agent with this id running on". cantor asked it with `oneagent_id:<id>`
// through the same search bar an operator uses; here it is a method, because the
// value is not caller-supplied text and must not be spliced into an expression.
//
// A MODEL THAT DOES NOT CARRY THE ATTRIBUTE ANSWERS EMPTY rather than failing.
// `oneagent_id` is not a built-in attribute — a deployment has it only if it
// created one — and with no such attribute there is no placement to look up,
// which is an answer rather than an error.
func (u *Usecase) FindInstancesByAttribute(ctx context.Context, attr string, values []any, limit int) ([]SearchHit, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}
	attr = strings.TrimSpace(attr)
	if attr == "" || len(values) == 0 {
		return []SearchHit{}, nil
	}
	if limit <= 0 {
		limit = DefaultInstancePageSize
	}
	if limit > MaxInstancePageSize {
		limit = MaxInstancePageSize
	}
	node, err := u.equalAnyNode(ctx, attr, values)
	if err != nil {
		return nil, err
	}
	if node == nil {
		return []SearchHit{}, nil
	}
	page, err := u.searchWithLimit(ctx, SearchRequest{Count: limit, RetKey: RetKeyName}, node, limit)
	if err != nil {
		return nil, err
	}
	return page.Hits, nil
}

// equalAnyNode builds an `attr IN (values)` restriction, or reports that the
// attribute is not something this deployment models.
//
// THE VALUES GO THROUGH THE PARSER'S OWN COERCION, not through the expression
// text: an agent id is a value, and one containing the language's punctuation
// would otherwise change the query rather than the value.
func (u *Usecase) equalAnyNode(ctx context.Context, attr string, values []any) (search.Node, error) {
	catalog, err := u.loadCatalog(ctx, search.Refs{Attributes: []string{attr}})
	if err != nil {
		return nil, err
	}
	resolved, ok := catalog.Attr(attr)
	if !ok {
		// NOT MODELLED IS NOT AN ERROR. `oneagent_id` is not a built-in
		// attribute: a deployment has one only because it made one, and a
		// deployment without it does not model where agents run — an answer, not
		// a failure.
		return nil, nil
	}
	raw := make([]string, 0, len(values))
	for _, value := range values {
		raw = append(raw, fmt.Sprint(value))
	}
	match, err := search.InValues(resolved, raw)
	if err != nil {
		return nil, err
	}
	return &search.AttrTerm{Attr: resolved, Match: match}, nil
}

// searchWith is Search with an extra predicate the caller cannot express in the
// query language.
//
// The hierarchy walk needs it: its result is a set of instance ids it computed
// itself, and the CI query has to be restricted to them. Expressing that as an
// `_id:` term would work but would also let the caller send one, and the ids
// are not the caller's to choose.
func (u *Usecase) searchWith(ctx context.Context, req SearchRequest, restrict search.Node) (*SearchPage, error) {
	return u.searchWithLimit(ctx, req, restrict, MaxInstancePageSize)
}

// searchWithLimit is searchWith under a caller-chosen page ceiling.
//
// THE CEILING IS THE CALLER'S BECAUSE A TREE'S CHILD LIST IS NOT A PAGE. The
// console reads a service tree by asking for one node's children — a query it
// sends with `count=10000`, because the children of a node ARE its contents and
// a tree that renders five hundred of three thousand branches is missing
// branches with nothing to say so. A hierarchy read is already bounded by the
// walk that produced its instances (MaxHierarchyInstances), so letting its page
// carry the same bound means the page can never truncate what the walk allowed
// — one bound governing both, rather than two that disagree.
func (u *Usecase) searchWithLimit(ctx context.Context, req SearchRequest, restrict search.Node, maxSize int) (*SearchPage, error) {
	if u.repo == nil || u.types == nil {
		return nil, errs.ErrNotWiredYet
	}

	page := req.Page
	if page < 1 {
		page = 1
	}
	size := req.Count
	if size <= 0 {
		size = DefaultInstancePageSize
	}
	if size > maxSize {
		size = maxSize
	}

	plan, err := u.compileQuery(ctx, req, restrict)
	if err != nil {
		return nil, err
	}

	rows, numFound, err := u.repo.QueryInstances(ctx, *plan, size, (page-1)*size)
	if err != nil {
		return nil, err
	}

	out := &SearchPage{
		Hits:     []SearchHit{},
		NumFound: numFound,
		Page:     page,
		Counter:  map[string]int{},
	}
	if len(rows) == 0 {
		return out, nil
	}

	hits, err := u.renderHits(ctx, rows, req.Fields, req.RetKey)
	if err != nil {
		return nil, err
	}
	out.Hits = hits
	for _, hit := range hits {
		out.Counter[hit.TypeName]++
	}

	if len(plan.Facets) > 0 && numFound > 0 {
		buckets, err := u.repo.QueryFacets(ctx, *plan, plan.Facets, MaxFacetBuckets)
		if err != nil {
			return nil, err
		}
		out.Facets = map[string][]FacetBucket{}
		for _, attr := range plan.Facets {
			key := renderKey(specOf(attr), req.RetKey)
			out.Facets[key] = buckets[attr.ID]
		}
	}

	return out, nil
}

// compileQuery parses an expression, loads the dictionary it mentions, resolves
// it, and applies a caller-invisible restriction.
//
// The three phases stay separate — see Search — and the restriction is merged
// last so it cannot be expressed, or evaded, by the expression itself.
func (u *Usecase) compileQuery(ctx context.Context, req SearchRequest, restrict search.Node) (*search.Plan, error) {
	parsed, err := search.Parse(req.Query)
	if err != nil {
		return nil, err
	}

	// The dictionary is loaded from the references the expression mentions —
	// once, batched — and an unknown reference is reported rather than
	// ignored, because a query that silently matched nothing would look like a
	// query that matched nothing.
	catalog, err := u.loadCatalog(ctx, search.CollectRefs(parsed, req.Sort, req.Facets))
	if err != nil {
		return nil, err
	}

	plan, err := search.Resolve(parsed, catalog, req.Sort, req.Facets)
	if err != nil {
		return nil, err
	}
	if restrict != nil {
		if plan.Root == nil {
			plan.Root = restrict
		} else {
			plan.Root = &search.And{Left: plan.Root, Right: restrict}
		}
	}
	return plan, nil
}

// renderHits turns CI rows into result rows.
//
// It is the only place a search result row is built, so the secret filter — see
// renderInstance — and the shape of a hit are decided once. The reads that
// fetch instances without paging (the hierarchy walks and the path search, both
// of which compute their own instance set) go through it too, so a row built
// there cannot differ from a row built by a search.
func (u *Usecase) renderHits(ctx context.Context, rows []cimodel.CI, fields []string, key RetKey) ([]SearchHit, error) {
	// The fallback read, as in ListByType: only when a requested field is not
	// in ANY of the page's snapshots, because a field absent from one row's
	// document just means that instance has no value for it.
	//
	// AN UNPOPULATED SNAPSHOT ALWAYS FALLS BACK, and that case is not the same
	// question as the one above. "Is this field missing from every row" is
	// vacuous when the caller asked for EVERY field — there is no field to be
	// missing — so a page whose snapshots are empty rendered as a page of
	// instances with no values at all. An empty snapshot is a row the document
	// column has never been written for, which is every row of a database
	// adopted from cantor: it has the value tables and no snapshot column.
	wanted := fieldSet(fields)
	missing := false
	for _, row := range rows {
		if len(wanted) == 0 && snapshotEmpty(row) {
			missing = true
			break
		}
	}
	if !missing {
		for name := range wanted {
			missing = missing || !snapshotHasAny(rows, name)
		}
	}
	var fallback map[model.ID][]ValueRow
	if missing {
		ids := make([]model.ID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		var err error
		fallback, err = u.repo.ValueRowsForCIs(ctx, ids)
		if err != nil {
			return nil, err
		}
	}

	out := make([]SearchHit, 0, len(rows))
	for _, row := range rows {
		set, err := u.typeSetFor(ctx, row.TypeID)
		if err != nil {
			return nil, err
		}
		instance, err := u.renderInstance(set, row, fallback[row.ID], wanted, key)
		if err != nil {
			return nil, err
		}
		hit := SearchHit{
			Instance:  instance,
			TypeName:  set.Type.Name,
			TypeAlias: set.Type.Alias,
			UpdatedBy: updatedBy(row),
		}
		if set.UniqueAttr.ID != 0 {
			hit.UniqueAttr = set.UniqueAttr.Name
			hit.UniqueAlias = set.UniqueAttr.Alias
		}
		out = append(out, hit)
	}
	return out, nil
}

// instanceIDs runs an expression and returns the matching instance ids.
//
// IT DOES NOT GO THROUGH Search, because its bound is the caller's — the
// hierarchy and path reads compute their own instance sets and are bounded by
// their own walk, not by a page size — and because building result rows for ids
// only to read the ids back off them is work with nothing behind it.
func (u *Usecase) instanceIDs(ctx context.Context, query string, restrict search.Node, limit int) ([]model.ID, error) {
	if limit <= 0 {
		return nil, nil
	}
	plan, err := u.compileQuery(ctx, SearchRequest{Query: query}, restrict)
	if err != nil {
		return nil, err
	}
	rows, _, err := u.repo.QueryInstances(ctx, *plan, limit, 0)
	if err != nil {
		return nil, err
	}
	out := make([]model.ID, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out, nil
}

// updatedBy reads the CI row's audit column, which is nullable because cantor
// left it unset for anything the system wrote.
func updatedBy(ci cimodel.CI) string {
	if ci.UpdatedBy == nil {
		return ""
	}
	return *ci.UpdatedBy
}

// loadCatalog batches the dictionary reads a parsed query needs.
func (u *Usecase) loadCatalog(ctx context.Context, refs search.Refs) (*search.Catalog, error) {
	var attrs []search.Attr
	if len(refs.Attributes) > 0 {
		got, err := u.repo.QueryAttributes(ctx, refs.Attributes)
		if err != nil {
			return nil, err
		}
		attrs = got
	}
	var types []search.Type
	if len(refs.Types) > 0 {
		got, err := u.repo.QueryTypes(ctx, refs.Types)
		if err != nil {
			return nil, err
		}
		types = got
	}
	return search.NewCatalog(attrs, types), nil
}

// typeSetFor resolves a row's model during rendering.
//
// A search result may span several models, so each row's own definition is
// needed to know which values are secrets, how a value is keyed, and what the
// model calls itself. The resolver is the same one the write path uses, so a
// row can never be rendered through a different reading of the model than the
// one it was written under.
func (u *Usecase) typeSetFor(ctx context.Context, typeID model.ID) (*AttributeSet, error) {
	set, err := u.types.ResolveForCI(ctx, fmt.Sprint(typeID))
	if err != nil {
		return nil, err
	}
	if set == nil {
		// An instance whose model is gone. cantor dereferenced the type row
		// and crashed on the row it could not find; reporting which model is
		// missing leaves the console something actionable.
		return nil, fmt.Errorf("%w: instance's model %d", ErrTypeNotFound, typeID)
	}
	return set, nil
}

// specOf projects a resolved query attribute back into the shape renderKey
// expects. A facet is keyed the same way a value is, so the two agree.
func specOf(a search.Attr) AttributeSpec {
	return AttributeSpec{ID: a.ID, Name: a.Name, Alias: a.Alias, ValueType: a.ValueType}
}
