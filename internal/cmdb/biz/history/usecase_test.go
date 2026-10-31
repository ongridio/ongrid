package history

import (
	"context"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
)

// The history read paths: grouping a page of changes under their operation
// record, and naming what the ids refer to.
//
// THE STORE IS A DOUBLE HERE because its job is one joined query per table; what
// this file settles is the shape a console page consumes — one row per write,
// with the changes underneath it and the labels a person can read.

type stubRepo struct {
	attributes []AttributeRow
	relations  []RelationRow
	types      []TypeRow
	triggers   []TriggerRow

	lastAttributeFilter AttributeFilter
	lastRelationFilter  RelationFilter
	lastTypeFilter      TypeFilter
	lastTriggerFilter   TriggerFilter
	lastCIPage          [2]int
}

func (s *stubRepo) ListAttributeRecords(_ context.Context, f AttributeFilter) ([]AttributeRow, int64, error) {
	s.lastAttributeFilter = f
	return s.attributes, int64(len(s.attributes)), nil
}

func (s *stubRepo) ListRelationRecords(_ context.Context, f RelationFilter) ([]RelationRow, int64, error) {
	s.lastRelationFilter = f
	return s.relations, int64(len(s.relations)), nil
}

func (s *stubRepo) ListTypeRecords(_ context.Context, f TypeFilter) ([]TypeRow, int64, error) {
	s.lastTypeFilter = f
	return s.types, int64(len(s.types)), nil
}

func (s *stubRepo) ListTriggerRecords(_ context.Context, f TriggerFilter) ([]TriggerRow, int64, error) {
	s.lastTriggerFilter = f
	return s.triggers, int64(len(s.triggers)), nil
}

func (s *stubRepo) ListCIAttributeRecords(_ context.Context, _ model.ID, page, size int) ([]AttributeRow, int64, error) {
	s.lastCIPage = [2]int{page, size}
	return s.attributes, int64(len(s.attributes)), nil
}

func (s *stubRepo) ListCITriggerRecords(_ context.Context, _ model.ID, page, size int) ([]TriggerRow, int64, error) {
	s.lastCIPage = [2]int{page, size}
	return s.triggers, int64(len(s.triggers)), nil
}

type stubTypeNames map[model.ID]string

func (s stubTypeNames) TypeName(_ context.Context, id model.ID) (string, string, error) {
	name := s[id]
	return name, name + " alias", nil
}

type stubAttributes map[model.ID][]AttributeRef

func (s stubAttributes) ResolveAttributes(_ context.Context, typeID model.ID) ([]AttributeRef, error) {
	return s[typeID], nil
}

type stubCINames map[model.ID]string

func (s stubCINames) ResolveCINames(_ context.Context, ids []model.ID) (map[model.ID]string, error) {
	out := map[model.ID]string{}
	for _, id := range ids {
		if name, ok := s[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

func timeAt(hour int) time.Time {
	return time.Date(2026, 10, 26, hour, 0, 0, 0, time.UTC)
}

func fixtureUseCase(repo *stubRepo) *Usecase {
	return NewUsecase(repo).
		WithTypeNames(stubTypeNames{1: "host", 2: "switch"}).
		WithAttributes(stubAttributes{
			1: []AttributeRef{
				{ID: 10, Name: "hostname", Alias: "主机名", ValueType: "2"},
				{ID: 11, Name: "cpu", Alias: "CPU", ValueType: "3"},
			},
		}).
		WithCINames(stubCINames{100: "web-01", 101: "sw-01"})
}

func TestAttributeHistoryGroupsChangesUnderTheirRecord(t *testing.T) {
	repo := &stubRepo{attributes: []AttributeRow{
		{
			Record: Record{ID: 2, UID: 7, TypeID: 1, Origin: "ui", CreatedAt: timeAt(11)},
			ID:     20, OperateType: "u", CIID: 100, AttrID: 10,
			Old: ptr("web-00"), New: ptr("web-01"),
		},
		{
			Record: Record{ID: 2, UID: 7, TypeID: 1, Origin: "ui", CreatedAt: timeAt(11)},
			ID:     19, OperateType: "u", CIID: 100, AttrID: 11,
			Old: ptr("4"), New: ptr("8"),
		},
		{
			Record: Record{ID: 1, UID: 8, TypeID: 1, Origin: "import", CreatedAt: timeAt(10)},
			ID:     18, OperateType: "a", CIID: 100, AttrID: 10,
			New: ptr("web-00"),
		},
	}}
	got, err := fixtureUseCase(repo).ListAttributes(context.Background(), AttributeFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListAttributes: %v", err)
	}
	if len(got.Items) != 2 || got.Total != 3 {
		t.Fatalf("page = %+v, want 2 groups over 3 changes", got)
	}
	first := got.Items[0]
	if first.RecordID != 2 || first.UID != 7 || first.TypeName != "host" || first.Origin != "ui" {
		t.Fatalf("first record = %+v", first)
	}
	// A timestamp carries its zone. The console sends RFC 3339 ranges and
	// renders the result in the browser's zone, so a naive server-local string
	// would shift both the filter and the display for every non-UTC operator.
	if _, err := time.Parse(time.RFC3339, first.CreatedAt); err != nil {
		t.Fatalf("created_at = %q, want RFC3339: %v", first.CreatedAt, err)
	}
	if len(first.Changes) != 2 {
		t.Fatalf("first record changes = %+v, want 2", first.Changes)
	}
	if first.Changes[0].AttrName != "hostname" || first.Changes[0].AttrAlias != "主机名" ||
		first.Changes[0].CIName != "web-01" || first.Changes[0].ValueType != "2" {
		t.Fatalf("first change = %+v", first.Changes[0])
	}
	if first.Changes[0].Old != "web-00" || first.Changes[0].New != "web-01" {
		t.Fatalf("old/new = %q/%q", first.Changes[0].Old, first.Changes[0].New)
	}
	if first.CreatedAt != "2026-10-26T11:00:00Z" {
		t.Fatalf("created_at = %q", first.CreatedAt)
	}
}

func TestHistoryFiltersReachTheStoreAndPagesAreClamped(t *testing.T) {
	repo := &stubRepo{}
	uc := fixtureUseCase(repo)
	start := timeAt(9)

	_, err := uc.ListAttributes(context.Background(), AttributeFilter{
		Start: start, End: timeAt(12), UID: 7, TypeID: 1, CIID: 100, AttrID: 10,
		OperateType: "u", Page: 0, PageSize: 9999,
	})
	if err != nil {
		t.Fatalf("ListAttributes: %v", err)
	}
	got := repo.lastAttributeFilter
	if got.Page != 1 || got.PageSize != MaxPageSize {
		t.Fatalf("paging = %d/%d, want 1/%d", got.Page, got.PageSize, MaxPageSize)
	}
	if !got.Start.Equal(start) || !got.End.Equal(timeAt(12)) || got.UID != 7 ||
		got.TypeID != 1 || got.CIID != 100 || got.AttrID != 10 || got.OperateType != "u" {
		t.Fatalf("filter = %+v", got)
	}
}

func TestRelationHistoryNamesBothEnds(t *testing.T) {
	repo := &stubRepo{relations: []RelationRow{{
		Record: Record{ID: 5, UID: 7, TypeID: 1, CreatedAt: timeAt(12)},
		ID:     30, OperateType: "a", FirstCIID: ptr(model.ID(100)), SecondCIID: ptr(model.ID(101)),
		RelationTypeID: ptr(model.ID(3)), RelationID: 9,
	}}}
	got, err := fixtureUseCase(repo).ListRelations(context.Background(), RelationFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	if len(got.Items) != 1 || len(got.Items[0].Changes) != 1 {
		t.Fatalf("relation page = %+v", got)
	}
	change := got.Items[0].Changes[0]
	if change.FirstCIName != "web-01" || change.SecondCIName != "sw-01" || change.RelationTypeID != 3 {
		t.Fatalf("relation change = %+v", change)
	}
}

func TestCITypeHistoryNamesTheModelAndAttribute(t *testing.T) {
	repo := &stubRepo{types: []TypeRow{{
		ID: 40, OperateType: "10", TypeID: 1, AttrID: ptr(model.ID(10)),
		UID: ptr(model.ID(7)), Change: map[string]any{"alias": "主机名"}, CreatedAt: timeAt(9),
	}}}
	got, err := fixtureUseCase(repo).ListTypes(context.Background(), TypeFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].TypeName != "host" || got.Items[0].AttrName != "hostname" {
		t.Fatalf("type item = %+v", got.Items)
	}
}

func TestTriggerHistoryNamesTheModelAndCI(t *testing.T) {
	repo := &stubRepo{triggers: []TriggerRow{{
		ID: 50, OperateType: "a", CIID: 100, TriggerID: ptr(model.ID(4)),
		TriggerName: "notify", IsOK: true, UID: 7, TypeID: 1, CreatedAt: timeAt(8),
	}}}
	got, err := fixtureUseCase(repo).ListTriggers(context.Background(), TriggerFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].CIName != "web-01" || got.Items[0].TypeName != "host" ||
		got.Items[0].TriggerName != "notify" || !got.Items[0].IsOK {
		t.Fatalf("trigger item = %+v", got.Items)
	}
}

func TestPerCIHistoryPagesAndNamesTheAttribute(t *testing.T) {
	repo := &stubRepo{attributes: []AttributeRow{{
		Record: Record{ID: 2, UID: 7, TypeID: 1, CreatedAt: timeAt(11)},
		ID:     20, OperateType: "u", CIID: 100, AttrID: 10, Old: ptr("a"), New: ptr("b"),
	}}}
	got, err := fixtureUseCase(repo).ListCIAttributes(context.Background(), 100, 0, 9999)
	if err != nil {
		t.Fatalf("ListCIAttributes: %v", err)
	}
	if got.Page != 1 || got.PageSize != MaxPageSize {
		t.Fatalf("paging = %d/%d", got.Page, got.PageSize)
	}
	if len(got.Items) != 1 || got.Items[0].AttrName != "hostname" || got.Items[0].CIName != "web-01" {
		t.Fatalf("ci history = %+v", got.Items)
	}
}

// A password attribute's stored old/new values never reach a history response,
// on either the global page or the per-instance page. The attribute's name may
// still be shown: what changed is useful, the value is not.
func TestHistoryMasksSensitiveAttributeValues(t *testing.T) {
	const masked = "******"
	repo := &stubRepo{attributes: []AttributeRow{
		{
			Record: Record{ID: 3, UID: 7, TypeID: 1, Origin: "ui", CreatedAt: timeAt(11)},
			ID:     30, OperateType: "u", CIID: 100, AttrID: 12,
			Old: ptr("hunter2"), New: ptr("correct-horse"),
		},
		{
			Record: Record{ID: 3, UID: 7, TypeID: 1, Origin: "ui", CreatedAt: timeAt(11)},
			ID:     31, OperateType: "u", CIID: 100, AttrID: 13,
			New: ptr("plain"),
		},
	}}
	uc := NewUsecase(repo).
		WithTypeNames(stubTypeNames{1: "host"}).
		WithAttributes(stubAttributes{1: []AttributeRef{
			{ID: 12, Name: "password", Alias: "密码", ValueType: "2", Sensitive: true},
			{ID: 13, Name: "note", Alias: "备注", ValueType: "2"},
		}}).
		WithCINames(stubCINames{100: "web-01"})

	page, err := uc.ListAttributes(context.Background(), AttributeFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListAttributes: %v", err)
	}
	if len(page.Items) != 1 || len(page.Items[0].Changes) != 2 {
		t.Fatalf("page = %+v", page.Items)
	}
	secret, note := page.Items[0].Changes[0], page.Items[0].Changes[1]
	if secret.Old != masked || secret.New != masked {
		t.Fatalf("password values = %q → %q, want masked", secret.Old, secret.New)
	}
	if secret.AttrName != "password" || secret.AttrAlias != "密码" {
		t.Fatalf("password label = %+v, want the name kept", secret)
	}
	if note.Old != "" || note.New != "plain" {
		t.Fatalf("note values = %q → %q, want untouched", note.Old, note.New)
	}

	perCI, err := uc.ListCIAttributes(context.Background(), 100, 1, 20)
	if err != nil {
		t.Fatalf("ListCIAttributes: %v", err)
	}
	for _, change := range perCI.Items {
		if change.AttrID == 12 && (change.Old != masked || change.New != masked) {
			t.Fatalf("per-CI password values = %q → %q, want masked", change.Old, change.New)
		}
	}
}

// A SENSITIVE ATTRIBUTE MAY BE GONE FROM THE MODEL'S CURRENT SET. The history
// row still names it, and a soft-deleted password attribute must not leak its
// old/new values just because the attribute resolver can no longer see the
// password flag. The store carries the flag on the row itself for this case.
func TestHistoryMasksDeletedSensitiveAttributeValues(t *testing.T) {
	repo := &stubRepo{attributes: []AttributeRow{{
		Record: Record{ID: 5, UID: 7, TypeID: 1, CreatedAt: timeAt(12)},
		ID:     40, OperateType: "u", CIID: 100, AttrID: 99,
		Old: ptr("legacy-secret"), New: ptr("new-secret"), Sensitive: true,
	}}}
	uc := NewUsecase(repo).
		WithTypeNames(stubTypeNames{1: "host"}).
		WithAttributes(stubAttributes{1: nil}).
		WithCINames(stubCINames{100: "web-01"})

	page, err := uc.ListAttributes(context.Background(), AttributeFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("ListAttributes: %v", err)
	}
	if len(page.Items) != 1 || len(page.Items[0].Changes) != 1 {
		t.Fatalf("page = %+v", page.Items)
	}
	if got := page.Items[0].Changes[0]; got.Old != "******" || got.New != "******" {
		t.Fatalf("deleted password values = %q → %q, want masked", got.Old, got.New)
	}
}
