package discovery

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/cmdb/model"
	discoverymodel "github.com/ongridio/ongrid/internal/cmdb/model/discovery"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// The pool: what an agent reported, what a person accepted, and what an
// auto-accept target writes without asking.
//
// THE WRITER IS A DOUBLE HERE because the instance side is tested in its own
// package; what this file settles is the mapping, the state machine and the
// idempotency — none of which belong to the instance write.

type stubPoolRepo struct {
	entries   []discoverymodel.PoolEntry
	histories []discoverymodel.ExecHistory
	counters  PoolCounters
	nextID    model.ID

	lastWrite  PoolWrite
	lastAccept model.ID
	acceptBy   string
}

func newPoolStub() *stubPoolRepo { return &stubPoolRepo{nextID: 100} }

func (s *stubPoolRepo) ListPool(_ context.Context, f PoolFilter) ([]discoverymodel.PoolEntry, int64, error) {
	out := []discoverymodel.PoolEntry{}
	for _, row := range s.entries {
		if f.TypeID != 0 && (row.TypeID == nil || *row.TypeID != f.TypeID) {
			continue
		}
		if f.IsAccept != nil && row.IsAccept != *f.IsAccept {
			continue
		}
		out = append(out, row)
	}
	return out, int64(len(out)), nil
}

func (s *stubPoolRepo) LoadPool(_ context.Context, id model.ID) (*discoverymodel.PoolEntry, error) {
	for i := range s.entries {
		if s.entries[i].ID == id {
			return &s.entries[i], nil
		}
	}
	return nil, nil
}

func (s *stubPoolRepo) UpsertPool(_ context.Context, in PoolWrite) (*PoolUpsertResult, error) {
	s.lastWrite = in
	for i := range s.entries {
		row := &s.entries[i]
		if row.TypeID == nil || *row.TypeID != in.TypeID || derefString(row.UniqueValue) != in.UniqueValue {
			continue
		}
		changed := !reflect.DeepEqual(row.Instance, in.Instance)
		if changed {
			merged := map[string]any{}
			for k, v := range row.Instance {
				merged[k] = v
			}
			for k, v := range in.Instance {
				merged[k] = v
			}
			row.Instance = merged
			row.IsAccept = false
			row.CIID = nil
			row.AcceptBy = nil
			row.AcceptTime = nil
		}
		row.OneagentID = strp(in.OneagentID)
		row.OneagentName = strp(in.OneagentName)
		return &PoolUpsertResult{Row: row, Changed: changed}, nil
	}
	s.nextID++
	row := discoverymodel.PoolEntry{
		TypeID: idp(in.TypeID), AdtID: idp(in.AdtID),
		UniqueValue: strp(in.UniqueValue), Instance: in.Instance,
		OneagentID: strp(in.OneagentID), OneagentName: strp(in.OneagentName),
	}
	row.ID = s.nextID
	row.Instance = in.Instance
	s.entries = append(s.entries, row)
	return &PoolUpsertResult{Row: &s.entries[len(s.entries)-1], Created: true, Changed: true}, nil
}

func (s *stubPoolRepo) DeletePool(_ context.Context, id model.ID) error {
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *stubPoolRepo) MarkPoolAccepted(_ context.Context, id, ciID model.ID, acceptBy string, at time.Time) error {
	s.lastAccept = id
	s.acceptBy = acceptBy
	for i := range s.entries {
		if s.entries[i].ID != id {
			continue
		}
		s.entries[i].CIID = idp(ciID)
		s.entries[i].IsAccept = true
		s.entries[i].AcceptBy = strp(acceptBy)
		s.entries[i].AcceptTime = &at
	}
	return nil
}

func (s *stubPoolRepo) PoolCounters(context.Context, model.ID, time.Time) (PoolCounters, error) {
	return s.counters, nil
}

func (s *stubPoolRepo) ListExecHistories(_ context.Context, typeID model.ID, _, _ int) ([]discoverymodel.ExecHistory, int64, error) {
	out := []discoverymodel.ExecHistory{}
	for _, row := range s.histories {
		if row.TypeID != nil && *row.TypeID == typeID {
			out = append(out, row)
		}
	}
	return out, int64(len(out)), nil
}

func (s *stubPoolRepo) CreateExecHistory(_ context.Context, typeID model.ID, stdout string) error {
	row := discoverymodel.ExecHistory{TypeID: idp(typeID), Stdout: strp(stdout)}
	row.ID = model.ID(len(s.histories) + 1)
	s.histories = append(s.histories, row)
	return nil
}

func (s *stubPoolRepo) DeleteExecHistories(_ context.Context, typeID model.ID) error {
	kept := []discoverymodel.ExecHistory{}
	for _, row := range s.histories {
		if row.TypeID == nil || *row.TypeID != typeID {
			kept = append(kept, row)
		}
	}
	s.histories = kept
	return nil
}

type stubWriter struct {
	calls   []DiscoveredInstance
	result  DiscoveredResult
	err     error
	missing map[model.ID]bool
}

func (s *stubWriter) WriteDiscovered(_ context.Context, in DiscoveredInstance) (*DiscoveredResult, error) {
	s.calls = append(s.calls, in)
	if s.err != nil {
		return nil, s.err
	}
	out := s.result
	return &out, nil
}

func (s *stubWriter) CIExists(_ context.Context, id model.ID) (bool, error) {
	return !s.missing[id], nil
}

type stubAttributes struct{ attrs []AttributeRef }

func (s *stubAttributes) ResolveAttributes(context.Context, model.ID) ([]AttributeRef, error) {
	return s.attrs, nil
}

func poolFixture(t *testing.T, autoAccept bool) (*Usecase, *stubRepo, *stubPoolRepo, *stubWriter) {
	t.Helper()
	repo := newStub()
	repo.rules = []discoverymodel.Rule{providerRule(1, "阿里云", discoverymodel.TypeHTTP, "aliyun")}
	target := targetOf(7, 1, nil)
	target.ID = 5
	target.Attributes = map[string]any{"instance_id": "hostname", "cpu_num": "cpu", "vpc_id": "vpc"}
	target.AutoAccept = autoAccept
	repo.targets = []discoverymodel.Target{target}
	repo.relations = []discoverymodel.TargetRelation{
		{AdTypeID: 7, AdKey: strp("vpc_id"), PeerTypeID: 8, PeerAttrID: 20},
	}
	pool := newPoolStub()
	writer := &stubWriter{result: DiscoveredResult{CIID: 77, Created: true}}
	uc := NewUsecase(repo).
		WithModels(modelsFor(7, "host", "hostname")).
		WithPool(pool).
		WithWriter(writer)
	return uc, repo, pool, writer
}

func poolEntry(id model.ID, instance map[string]any, accepted bool) discoverymodel.PoolEntry {
	row := discoverymodel.PoolEntry{
		TypeID: idp(7), AdtID: idp(5), UniqueValue: strp("i-1"),
		Instance: instance, OneagentID: strp("agent-1"), OneagentName: strp("agent one"),
		IsAccept: accepted,
	}
	row.ID = id
	if accepted {
		row.CIID = idp(70)
		row.AcceptBy = strp("ops@example.test")
	}
	return row
}

// A CHANGED INSTANCE LOSES ITS ACCEPTANCE, because what a person accepted is no
// longer what the provider reports. An unchanged report keeps it: the agent
// re-sends its whole inventory on every run, and resetting on every sync would
// make the pool permanently unacceptable.
func TestPoolUpsertMergesAndResetsAcceptanceOnlyOnChange(t *testing.T) {
	uc, _, pool, writer := poolFixture(t, false)
	pool.entries = []discoverymodel.PoolEntry{poolEntry(1, map[string]any{
		"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1",
	}, true)}
	ctx := context.Background()

	got, err := uc.UpsertPool(ctx, PoolUpsertInput{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 4, "vpc_id": "vpc-1"},
		OneagentID: "agent-2", OneagentName: "agent two",
	}, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("UpsertPool: %v", err)
	}
	if got.IsAccept || got.CIID != nil {
		t.Fatalf("a changed instance kept its acceptance: %+v", got)
	}
	// The merge keeps the fields this report did not mention, and the view
	// carries the model's names (`vpc`), not the provider's (`vpc_id`).
	if got.Instance["vpc"] != "vpc-1" || got.Instance["cpu"] != 4 {
		t.Fatalf("instance was replaced rather than merged: %+v", got.Instance)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("a non-auto-accept target wrote a CI: %+v", writer.calls)
	}
	if len(pool.histories) != 1 || pool.histories[0].Stdout == nil || *pool.histories[0].Stdout == "" {
		t.Fatalf("the update was not logged: %+v", pool.histories)
	}

	// The agent re-reports the same values: nothing changes, and the row the
	// operator accepts next stays acceptable.
	accepted, err := uc.AcceptPool(ctx, 1, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("AcceptPool: %v", err)
	}
	if !accepted.IsAccept || accepted.CIID == nil {
		t.Fatalf("accept did not mark the row: %+v", accepted)
	}
	writer.calls = nil

	same, err := uc.UpsertPool(ctx, PoolUpsertInput{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 4, "vpc_id": "vpc-1"},
		OneagentID: "agent-2", OneagentName: "agent two",
	}, Actor{UserID: 42})
	if err != nil {
		t.Fatalf("UpsertPool (unchanged): %v", err)
	}
	if !same.IsAccept || same.CIID == nil {
		t.Fatalf("an unchanged sync cleared the acceptance: %+v", same)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("an unchanged sync wrote a CI: %+v", writer.calls)
	}
}

// ACCEPT WRITES THE MAPPING AND THE RELATIONS IN ONE CALL, and is idempotent:
// a second click does not create a second instance or a second set of edges.
func TestAcceptWritesTheMappingAndStaysIdempotent(t *testing.T) {
	uc, _, pool, writer := poolFixture(t, false)
	pool.entries = []discoverymodel.PoolEntry{poolEntry(1, map[string]any{
		"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1",
	}, false)}
	ctx := context.Background()

	got, err := uc.AcceptPool(ctx, 1, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("AcceptPool: %v", err)
	}
	if !got.IsAccept || got.CIID == nil || *got.CIID != 77 {
		t.Fatalf("pool row after accept = %+v", got)
	}
	if pool.acceptBy != "ops@example.test" {
		t.Fatalf("accept_by = %q, want the caller's email", pool.acceptBy)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.calls))
	}
	call := writer.calls[0]
	if call.TypeID != 7 || call.Actor != 42 {
		t.Fatalf("write = %+v", call)
	}
	if call.Values["hostname"] != "i-1" || call.Values["cpu"] != 2 {
		t.Fatalf("mapped values = %+v", call.Values)
	}
	// The provider field that is not in the mapping is NOT written under its
	// raw name: a model does not have that attribute, and the write would be
	// refused or, worse, silently dropped.
	if _, ok := call.Values["instance_id"]; ok {
		t.Fatalf("the raw provider field leaked into the write: %+v", call.Values)
	}
	if len(call.Relations) != 1 || call.Relations[0].PeerTypeID != 8 ||
		call.Relations[0].PeerAttrID != 20 || len(call.Relations[0].Values) != 1 ||
		call.Relations[0].Values[0] != "vpc-1" {
		t.Fatalf("relations = %+v", call.Relations)
	}

	// A SECOND ACCEPT IS A READ, not a second write.
	again, err := uc.AcceptPool(ctx, 1, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("second AcceptPool: %v", err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("a second accept wrote the instance again: %+v", writer.calls)
	}
	if !again.IsAccept {
		t.Fatalf("second accept lost the acceptance: %+v", again)
	}
}

// AN AUTO-ACCEPT TARGET ACCEPTS A CHANGED INSTANCE AND DOES NOT EVEN CALL THE
// WRITER WHEN THE REPORT IS UNCHANGED.
func TestAutoAcceptTargetWritesWithoutAPerson(t *testing.T) {
	uc, _, pool, writer := poolFixture(t, true)
	ctx := context.Background()

	created, err := uc.UpsertPool(ctx, PoolUpsertInput{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1"},
		OneagentID: "agent-1", OneagentName: "agent one",
	}, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("UpsertPool: %v", err)
	}
	if !created.IsAccept || created.CIID == nil {
		t.Fatalf("auto-accept did not accept: %+v", created)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.calls))
	}
	if pool.acceptBy != "auto" {
		t.Fatalf("auto-accept recorded %q as the actor, want auto", pool.acceptBy)
	}

	// The same report again: still accepted, and no second write.
	writer.calls = nil
	same, err := uc.UpsertPool(ctx, PoolUpsertInput{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1"},
		OneagentID: "agent-1", OneagentName: "agent one",
	}, Actor{UserID: 42})
	if err != nil {
		t.Fatalf("UpsertPool (unchanged): %v", err)
	}
	if !same.IsAccept {
		t.Fatalf("an unchanged auto-accepted row lost its acceptance: %+v", same)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("an unchanged report was written again: %+v", writer.calls)
	}
}

// AN EMPTY OR UNMAPPED INSTANCE CANNOT BE ACCEPTED: there is no value for the
// attribute that identifies an instance, so the write would create a nameless
// row — or overwrite whichever one sorted first.
func TestAcceptRefusesAnUnmappedInstance(t *testing.T) {
	uc, _, pool, writer := poolFixture(t, false)
	pool.entries = []discoverymodel.PoolEntry{poolEntry(1, map[string]any{"unknown": "i-1"}, false)}
	ctx := context.Background()

	_, err := uc.AcceptPool(ctx, 1, Actor{UserID: 42})
	if err == nil {
		t.Fatal("an instance whose fields are all unmapped was accepted")
	}
	if !errors.Is(err, ErrPoolUnmapped) {
		t.Fatalf("err = %v, want ErrPoolUnmapped", err)
	}
	if len(writer.calls) != 0 {
		t.Fatalf("writer was called: %+v", writer.calls)
	}
}

// THE ATTRIBUTE LIST IS THE MAPPED SUBSET, in the model's own order. A column
// for every attribute of the model would show the pool's raw provider keys as
// permanently empty columns.
func TestPoolAttributesKeepsOnlyTheMappedSubset(t *testing.T) {
	uc, _, _, _ := poolFixture(t, false)
	uc = uc.WithAttributes(&stubAttributes{attrs: []AttributeRef{
		{ID: 10, Name: "hostname", Alias: "主机名"},
		{ID: 11, Name: "cpu", Alias: "CPU"},
		{ID: 12, Name: "note", Alias: "备注"},
	}})

	got, err := uc.ListPoolAttributes(context.Background(), "7")
	if err != nil {
		t.Fatalf("ListPoolAttributes: %v", err)
	}
	if len(got) != 2 || got[0].Name != "hostname" || got[1].Name != "cpu" {
		t.Fatalf("attributes = %+v, want hostname and cpu only", got)
	}
}

// A POOL ENTRY WITH NO TARGET IS A BROKEN ROW, and accepting it must say so
// rather than write a CI with no mapping.
func TestAcceptRefusesAMissingTarget(t *testing.T) {
	uc, repo, pool, _ := poolFixture(t, false)
	repo.targets = nil
	pool.entries = []discoverymodel.PoolEntry{poolEntry(1, map[string]any{"instance_id": "i-1"}, false)}

	_, err := uc.AcceptPool(context.Background(), 1, Actor{UserID: 42})
	if err == nil {
		t.Fatal("an entry whose target is gone was accepted")
	}
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err = %v, want ErrTargetNotFound", err)
	}
}

// A POOL ENTRY ID THAT DOES NOT EXIST IS A 404, not a silent success.
func TestAcceptUnknownPoolEntry(t *testing.T) {
	uc, _, _, _ := poolFixture(t, false)

	_, err := uc.AcceptPool(context.Background(), 999, Actor{UserID: 42})
	if err == nil {
		t.Fatal("accepting a missing pool entry succeeded")
	}
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("err = %v, want a not-found", err)
	}
}

// A DELETED INSTANCE MUST NOT LEAVE ITS SIGHTING PERMANENTLY ACCEPTED. The
// instance can be deleted through the normal CI endpoints, which do not touch
// the pool; the next unchanged report must notice the dangling ci_id and write
// the instance again, or an auto-accept target would never recreate it.
func TestAutoAcceptRecreatesADeletedInstanceFromAnUnchangedReport(t *testing.T) {
	uc, _, _, writer := poolFixture(t, true)
	ctx := context.Background()
	report := PoolUpsertInput{
		TypeID: 7, AdtID: 5, UniqueValue: "i-1",
		Instance:   map[string]any{"instance_id": "i-1", "cpu_num": 2, "vpc_id": "vpc-1"},
		OneagentID: "agent-1", OneagentName: "agent one",
	}

	created, err := uc.UpsertPool(ctx, report, Actor{UserID: 42, Email: "ops@example.test"})
	if err != nil {
		t.Fatalf("UpsertPool: %v", err)
	}
	if !created.IsAccept || created.CIID == nil || *created.CIID != 77 {
		t.Fatalf("first accept = %+v", created)
	}

	// The instance was deleted after the accept; the pool row still points at
	// 77. The agent reports the same values, so the row itself does not change.
	writer.missing = map[model.ID]bool{77: true}
	writer.result = DiscoveredResult{CIID: 88, Created: true}
	writer.calls = nil

	got, err := uc.UpsertPool(ctx, report, Actor{UserID: 42})
	if err != nil {
		t.Fatalf("UpsertPool (unchanged, deleted instance): %v", err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("writer calls = %d, want the instance recreated", len(writer.calls))
	}
	if got.CIID == nil || *got.CIID != 88 || !got.IsAccept {
		t.Fatalf("recreated pool row = %+v, want ci 88 accepted", got)
	}
}
