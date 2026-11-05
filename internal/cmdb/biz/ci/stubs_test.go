package ci

import (
	"context"
	"errors"

	"github.com/ongridio/ongrid/internal/cmdb/biz/search"
	model "github.com/ongridio/ongrid/internal/cmdb/model"
	cimodel "github.com/ongridio/ongrid/internal/cmdb/model/ci"
)

// valueRepoStub is the do-nothing write half, embedded by doubles that
// exercise a read or a query path. A test that accidentally reaches a write
// gets an error rather than a silent zero value that looks like a real empty
// result.
type valueRepoStub struct{}

func (valueRepoStub) WriteCI(context.Context, WriteSpec) (*WriteResult, error) {
	return nil, errors.New("not used")
}

func (valueRepoStub) CreateCIWithValues(context.Context, CreateInput) (*cimodel.CI, error) {
	return nil, errors.New("not used")
}

func (valueRepoStub) UpsertCIValues(context.Context, UpdateInput) ([]Change, error) {
	return nil, errors.New("not used")
}

func (valueRepoStub) LoadValueRows(context.Context, model.ID) ([]ValueRow, error) {
	return nil, errors.New("not used")
}

func (valueRepoStub) DeleteCI(context.Context, model.ID) error { return errors.New("not used") }

// queryRepoStub is the do-nothing half of the query port, embedded by test
// doubles that exercise a different path.
//
// Repo is assembled from four slices, so a double that only needs the write
// path would otherwise have to grow a method every time the query port does.
// Embedding this gives it the shape without the behaviour: a test that
// accidentally calls one of these gets an error rather than a silent zero
// value that looks like a real empty result.
type queryRepoStub struct{}

func (queryRepoStub) QueryInstances(context.Context, search.Plan, int, int) ([]cimodel.CI, int64, error) {
	return nil, 0, errors.New("not used")
}

func (queryRepoStub) QueryFacets(context.Context, search.Plan, []search.Attr, int) (map[model.ID][]FacetBucket, error) {
	return nil, errors.New("not used")
}

func (queryRepoStub) QueryAttributes(context.Context, []string) ([]search.Attr, error) {
	return nil, errors.New("not used")
}

func (queryRepoStub) QueryTypes(context.Context, []string) ([]search.Type, error) {
	return nil, errors.New("not used")
}

// relationRepoStub is the do-nothing relation half, embedded by doubles that
// exercise a different path.
type relationRepoStub struct{}

func (relationRepoStub) AddRelation(context.Context, RelationAddSpec) (*cimodel.Relation, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) BatchAddRelations(context.Context, []RelationAddSpec) (int, error) {
	return 0, errors.New("not used")
}
func (relationRepoStub) LoadRelation(context.Context, model.ID) (*cimodel.Relation, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) FindRelation(context.Context, RelationRef) (*cimodel.Relation, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) DeleteRelation(context.Context, model.ID, model.ID) (bool, error) {
	return false, errors.New("not used")
}
func (relationRepoStub) RelationsFrom(context.Context, model.ID, model.ID, int, int) ([]cimodel.Relation, int64, error) {
	return nil, 0, errors.New("not used")
}
func (relationRepoStub) RelationsTo(context.Context, model.ID, model.ID, int, int) ([]cimodel.Relation, int64, error) {
	return nil, 0, errors.New("not used")
}
func (relationRepoStub) WalkChildren(context.Context, []WalkKey, bool) ([]WalkKey, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) WalkParents(context.Context, []model.ID) ([]WalkKey, []model.ID, error) {
	return nil, nil, errors.New("not used")
}
func (relationRepoStub) TypeIDsForInstances(context.Context, []model.ID) (map[model.ID]model.ID, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) GraphChildren(context.Context, []model.ID) ([]GraphEdge, error) {
	return nil, errors.New("not used")
}
func (relationRepoStub) GraphParents(context.Context, []model.ID) ([]GraphEdge, error) {
	return nil, errors.New("not used")
}

// The data-centre graph reads are unused by the instance tests; embedding this
// keeps the double from growing a method per addition to the port.
type dcimRepoStub struct{}

func (dcimRepoStub) InstancesOfModels(context.Context, []model.ID) ([]InstanceRef, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) RelationsWithin(context.Context, []model.ID) ([]cimodel.Relation, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) ChildrenOfModel(context.Context, []model.ID, model.ID) ([]model.ID, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) ChildCountsOfModel(context.Context, []model.ID, model.ID) (map[model.ID]int64, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) RackOccupancy(context.Context, []model.ID, []model.ID, []model.ID) (map[model.ID][]Slot, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) ApplyRackChange(context.Context, RackChange) (*RackChangeResult, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) ApplyRackResize(context.Context, RackResize) (*RackResizeResult, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) DeleteRack(context.Context, RackDelete) error {
	return errors.New("not used")
}

func (dcimRepoStub) RackHistory(context.Context, RackHistoryFilter) ([]cimodel.DCIMOperationHistory, int64, error) {
	return nil, 0, errors.New("not used")
}

// The address-management reads are unused by the instance tests.
func (dcimRepoStub) SubnetScans(context.Context, []model.ID) (map[model.ID]ScanRule, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) SaveSubnetScan(context.Context, ScanRuleSpec) error {
	return errors.New("not used")
}

func (dcimRepoStub) DeleteSubnetScan(context.Context, model.ID) error {
	return errors.New("not used")
}

func (dcimRepoStub) InsertScanHistory(context.Context, ScanHistorySpec) error {
	return errors.New("not used")
}

func (dcimRepoStub) ScanHistories(context.Context, ScanHistoryFilter) ([]cimodel.IPAMSubnetScanHistory, int64, error) {
	return nil, 0, errors.New("not used")
}

func (dcimRepoStub) IPAMHistory(context.Context, IPAMHistoryFilter) ([]cimodel.IPAMOperationHistory, int64, error) {
	return nil, 0, errors.New("not used")
}

func (dcimRepoStub) ApplyAddressChange(context.Context, AddressChange) (*AddressChangeResult, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) ApplySubnetWrite(context.Context, SubnetWrite) (*WriteResult, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) WriteCIBatch(context.Context, []WriteSpec) ([]WriteResult, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) FindCIsByUniqueValues(context.Context, model.ID, model.ID, map[model.ID]AttributeSpec, []any) ([]model.ID, error) {
	return nil, errors.New("not used")
}

func (dcimRepoStub) RecordIPAMOperation(context.Context, IPAMHistoryEntry, AuditInfo) error {
	return errors.New("not used")
}

func (dcimRepoStub) CheckSubnetRange(context.Context, SubnetWrite) error {
	return errors.New("not used")
}

func (dcimRepoStub) MoveSubnet(context.Context, SubnetMove) error {
	return errors.New("not used")
}

// The model-graph half of the resolver double. It reports "no rule" for every
// pair, so a test that reaches the relation path without setting it up gets a
// clear refusal rather than a half-configured graph.
func (s *resolverStub) RelationBetween(context.Context, model.ID, model.ID) (TypeRelationRule, bool, error) {
	return TypeRelationRule{}, false, errors.New("the resolver double declares no model rules")
}

func (s *resolverStub) HierarchyLevels(context.Context, model.ID, int, bool) (HierarchyLevels, error) {
	return HierarchyLevels{}, errors.New("the resolver double declares no model graph")
}
