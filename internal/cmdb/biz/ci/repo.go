package ci

import (
	"context"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// Repo is the whole consumer-side persistence port for CI instances: the
// value-table writes, the transactional write path, the reads, the search
// query, the relation graph, and the graph shapes the data-centre module draws.
// It is assembled from six slices so each can be documented and tested on its
// own; splitting the declaration does not change injection, because the store
// implements all six and the use case holds one Repo.
type Repo interface {
	WriteRepo
	Reader
	ValueRepo
	QueryRepo
	RelationRepo
	DCIMRepo
	IPAMRepo
	ImportRepo
}

// ValueRepo is the value-table write path: writing a CI's attributes, and
// removing the CI itself.
//
// Every method that mutates is a COMPLETE transaction. Ongrid has no
// UnitOfWork abstraction — its data packages own GORM and its biz packages
// never see a *gorm.DB — so atomicity is expressed by making the repo method
// the unit of work rather than by threading a transaction handle through the
// usecase. This matters here more than elsewhere: one logical CI write spans
// c_cis, up to six value tables and two history tables, and a partial write
// would leave values that no longer match the document or the audit trail.
type ValueRepo interface {
	// CreateCIWithValues inserts the CI row, its value rows and its audit
	// rows in one transaction and returns the CI with its assigned id.
	CreateCIWithValues(ctx context.Context, in CreateInput) (*cimodel.CI, error)

	// UpsertCIValues applies value changes to an existing CI in one
	// transaction and returns the attribute-level changes it made.
	// A plan that changes nothing still returns nil changes.
	UpsertCIValues(ctx context.Context, in UpdateInput) ([]Change, error)

	// LoadValueRows returns every value row for a CI, across all six value
	// tables, each row tagged with the table it came from.
	LoadValueRows(ctx context.Context, ciID model.ID) ([]ValueRow, error)

	// LoadCI returns a CI by id, or nil when it does not exist.
	LoadCI(ctx context.Context, ciID model.ID) (*cimodel.CI, error)

	// LoadCIs returns the CIs with the given ids, keyed by id. Missing ids are
	// absent. Batch reads exist so a page of history does not issue one query
	// per instance.
	LoadCIs(ctx context.Context, ciIDs []model.ID) (map[model.ID]*cimodel.CI, error)

	// DeleteCI removes a CI together with its value rows and the relations it
	// participates in, in one transaction. Deletion is soft for the CI itself
	// (Ongrid's delete_marker convention); value rows are removed outright
	// because they carry no identity of their own.
	DeleteCI(ctx context.Context, ciID model.ID) error
}

// CreateInput is everything one CI creation needs.
type CreateInput struct {
	// CI carries type_id and the non-attribute columns. Its ID is ignored.
	CI *cimodel.CI

	// Specs are the attributes bound to the CI's type, keyed by attribute id.
	Specs map[model.ID]AttributeSpec

	// Values are the incoming attribute values, keyed by attribute id.
	Values map[model.ID]any

	// Audit describes the operation record written alongside the CI. Actor is
	// an Ongrid user id — cantor's user table is not migrated, so its own uid
	// space is never reused.
	Audit AuditInfo
}

// ParentLink relates a new instance to the instance that contains it.
//
// The child end is not a field: it is the instance being created, whose id does
// not exist until the insert runs. Everything the relation needs to be written
// is here, INCLUDING the models and the cardinality rule, so the store does not
// have to resolve the model graph inside a write transaction.
type ParentLink struct {
	ParentID model.ID

	ParentTypeID model.ID
	ChildTypeID  model.ID

	RelationTypeID model.ID

	// Constraint is the pair's cardinality rule. Zero means the write does not
	// enforce one, which is what a repair path passes.
	Constraint model.Constraint

	Actor model.ID
}

// UpdateInput is everything one CI value update needs.
type UpdateInput struct {
	CIID   model.ID
	TypeID model.ID
	Specs  map[model.ID]AttributeSpec
	Values map[model.ID]any
	Audit  AuditInfo
}

// AuditInfo describes the operation record for a write.
type AuditInfo struct {
	// Actor is the Ongrid user id performing the write.
	Actor model.ID
	// Origin names the subsystem, e.g. "ui", "import", "discovery".
	Origin string
	// TicketID is an optional change-management reference.
	TicketID string
	// Reason is an optional free-text justification.
	Reason string
}

// BuildOperationRecord renders the audit row for a write. It is pure so both
// the create and update paths — and the data layer that persists it — build it
// identically.
func BuildOperationRecord(typeID model.ID, in AuditInfo) *cimodel.OperationRecord {
	rec := &cimodel.OperationRecord{UID: in.Actor, TypeID: &typeID}
	if in.Origin != "" {
		origin := in.Origin
		rec.Origin = &origin
	}
	if in.TicketID != "" {
		ticket := in.TicketID
		rec.TicketID = &ticket
	}
	if in.Reason != "" {
		reason := in.Reason
		rec.Reason = &reason
	}
	return rec
}
