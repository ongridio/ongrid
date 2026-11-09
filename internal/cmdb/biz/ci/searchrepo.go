package ci

import (
	"context"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// QueryRepo is the port the search path needs.
//
// IT IS SEPARATE FROM Reader BECAUSE IT IS A DIFFERENT KIND OF QUESTION. Reader
// asks for a model's instances, which the caller has already chosen. QueryRepo
// hands the storage layer a PREDICATE and asks it to decide — and the decision
// is a SQL problem: which value table, which collation, whether the index
// serves the operator. Expressing that as "give me the ids you think match"
// keeps the whole language out of the data layer while leaving the execution
// plan where the dialect is known.
type QueryRepo interface {
	// QueryInstances returns one page of the instances matching a plan, in the
	// plan's order, with the total number of matches before paging.
	QueryInstances(ctx context.Context, plan search.Plan, limit, offset int) ([]cimodel.CI, int64, error)

	// QueryFacets returns, per attribute, the distinct values the matching
	// instances hold and how many instances hold each.
	QueryFacets(ctx context.Context, plan search.Plan, attrs []search.Attr, limit int) (map[model.ID][]FacetBucket, error)

	// QueryAttributes loads the attribute dictionary entries a query mentions.
	// References may be names, aliases or decimal ids; unknown ones are simply
	// absent from the result, and the caller reports them.
	QueryAttributes(ctx context.Context, refs []string) ([]search.Attr, error)

	// QueryTypes loads the models a query mentions, by the same three names.
	QueryTypes(ctx context.Context, refs []string) ([]search.Type, error)
}

// FacetBucket is one value and how many matching instances carry it.
type FacetBucket struct {
	Value any
	Count int64
}
