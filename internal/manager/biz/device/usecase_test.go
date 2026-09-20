package device

import (
	"context"
	"errors"
	"strings"
	"testing"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestDeleteCleansTopologyAndLinkedEdges(t *testing.T) {
	ctx := context.Background()
	nodeID := uint64(44)
	repo := &fakeRepo{byID: map[uint64]*devicemodel.Device{
		7: {ID: 7, Name: "node-a", NodeID: &nodeID},
	}}
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, nil, nil)
	uc.SetTopologyMirror(mirror)

	if err := uc.Delete(ctx, 7); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(mirror.deleted) != 1 || mirror.deleted[0] != [2]uint64{7, 44} {
		t.Fatalf("topology deletes = %#v, want device/node 7/44", mirror.deleted)
	}
	if !repo.deleted[7] {
		t.Fatalf("device was not deleted")
	}
}

func TestReconcileDeletedTopologyCleansHistoricalRows(t *testing.T) {
	ctx := context.Background()
	nodeID := uint64(88)
	repo := &fakeRepo{
		byID: map[uint64]*devicemodel.Device{},
		deletedWithNode: []*devicemodel.Device{
			{ID: 5, NodeID: &nodeID},
		},
	}
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, nil, nil)
	uc.SetTopologyMirror(mirror)

	cleaned, err := uc.ReconcileDeletedTopology(ctx)
	if err != nil {
		t.Fatalf("ReconcileDeletedTopology() error = %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1", cleaned)
	}
	if len(mirror.deleted) != 1 || mirror.deleted[0] != [2]uint64{5, 88} {
		t.Fatalf("topology deletes = %#v, want device/node 5/88", mirror.deleted)
	}
}

func TestReconcileOrphanDevicesDeletesRowsWithoutLiveEdges(t *testing.T) {
	ctx := context.Background()
	nodeID := uint64(99)
	repo := &fakeRepo{
		byID: map[uint64]*devicemodel.Device{
			9: {ID: 9, NodeID: &nodeID},
		},
		orphanDevices: []*devicemodel.Device{
			{ID: 9, NodeID: &nodeID},
		},
	}
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, nil, nil)
	uc.SetTopologyMirror(mirror)

	cleaned, err := uc.ReconcileOrphanDevices(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphanDevices() error = %v", err)
	}
	if cleaned != 1 {
		t.Fatalf("cleaned = %d, want 1", cleaned)
	}
	if !repo.deleted[9] {
		t.Fatalf("orphan device was not deleted")
	}
	if len(mirror.deleted) != 1 || mirror.deleted[0] != [2]uint64{9, 99} {
		t.Fatalf("topology deletes = %#v, want device/node 9/99", mirror.deleted)
	}
}

type fakeRepo struct {
	byID            map[uint64]*devicemodel.Device
	deleted         map[uint64]bool
	deletedWithNode []*devicemodel.Device
	orphanDevices   []*devicemodel.Device
}

func (r *fakeRepo) FindOrCreateByFingerprint(_ context.Context, seed *devicemodel.Device) (*devicemodel.Device, error) {
	if seed.ID == 0 {
		seed.ID = 42
	}
	r.byID[seed.ID] = seed
	return seed, nil
}

func (r *fakeRepo) RebindFingerprint(context.Context, string, string) error { return nil }

func (r *fakeRepo) UpdateHostFacts(context.Context, uint64, HostFacts) error { return nil }

func (r *fakeRepo) UpdateUsage(context.Context, uint64, Usage) error { return nil }

func (r *fakeRepo) UpdateRoles(context.Context, uint64, uint8) error { return nil }

func (r *fakeRepo) UpdateEnvironment(_ context.Context, id uint64, value string) error {
	dev, ok := r.byID[id]
	if !ok {
		return errs.ErrNotFound
	}
	dev.Environment = &value
	return nil
}

func (r *fakeRepo) UpdateNameDescription(context.Context, uint64, string, string) error {
	return nil
}

func (r *fakeRepo) SetNodeID(context.Context, uint64, uint64) error { return nil }

func (r *fakeRepo) MarkOnline(context.Context, uint64) error { return nil }

func (r *fakeRepo) MarkOffline(context.Context, uint64) error { return nil }

func (r *fakeRepo) Get(_ context.Context, id uint64) (*devicemodel.Device, error) {
	d, ok := r.byID[id]
	if !ok {
		return nil, errs.ErrNotFound
	}
	return d, nil
}

func (r *fakeRepo) GetMany(context.Context, []uint64) (map[uint64]*devicemodel.Device, error) {
	return map[uint64]*devicemodel.Device{}, nil
}

func (r *fakeRepo) List(context.Context, ListFilter) ([]*devicemodel.Device, error) {
	return nil, nil
}

func (r *fakeRepo) Count(context.Context) (int64, error) { return 0, nil }

func (r *fakeRepo) Delete(_ context.Context, id uint64) error {
	if r.deleted == nil {
		r.deleted = map[uint64]bool{}
	}
	r.deleted[id] = true
	delete(r.byID, id)
	return nil
}

func (r *fakeRepo) DeleteOfflineWithLinkedEdges(ctx context.Context, id uint64) error {
	return r.Delete(ctx, id)
}

func (r *fakeRepo) ReconcileOfflineOrphans(context.Context) (int64, error) { return 0, nil }

func (r *fakeRepo) ListDeletedWithNodeID(context.Context, int) ([]*devicemodel.Device, error) {
	return r.deletedWithNode, nil
}

func (r *fakeRepo) ListWithoutLiveEdges(context.Context, int) ([]*devicemodel.Device, error) {
	return r.orphanDevices, nil
}

type fakeLinks struct {
	rows     []*devicemodel.EdgeDevice
	unlinked [][3]uint64
	linked   [][2]uint64
}

func (l *fakeLinks) Link(_ context.Context, edgeID, deviceID uint64, _ devicemodel.EdgeDeviceRelationType) error {
	l.linked = append(l.linked, [2]uint64{edgeID, deviceID})
	return nil
}

func (l *fakeLinks) Unlink(_ context.Context, edgeID, deviceID uint64, typ devicemodel.EdgeDeviceRelationType) error {
	l.unlinked = append(l.unlinked, [3]uint64{edgeID, deviceID, uint64(typ)})
	return nil
}

func (l *fakeLinks) LookupHostDevice(context.Context, uint64) (uint64, error) { return 0, nil }

func (l *fakeLinks) LookupEdgeForDevice(context.Context, uint64, devicemodel.EdgeDeviceRelationType) (uint64, error) {
	return 0, nil
}

func (l *fakeLinks) ListDevicesForEdge(context.Context, uint64) ([]*devicemodel.EdgeDevice, error) {
	return nil, nil
}

func (l *fakeLinks) ListEdgesForDevice(context.Context, uint64) ([]*devicemodel.EdgeDevice, error) {
	return l.rows, nil
}

type fakeTopologyMirror struct {
	deleted [][2]uint64
}

func (m *fakeTopologyMirror) DeleteNodeForDevice(_ context.Context, deviceID, nodeID uint64) error {
	m.deleted = append(m.deleted, [2]uint64{deviceID, nodeID})
	return nil
}

func TestDeviceEnvironmentInheritanceAndOverride(t *testing.T) {
	ctx := context.Background()
	nodeID := uint64(9)
	repo := &fakeRepo{byID: map[uint64]*devicemodel.Device{1: {ID: 1}, 2: {ID: 2, NodeID: &nodeID}}}
	uc := NewUsecase(repo, nil, nil)
	clusterEnv := "production"
	uc.SetEnvironmentProvider(func(context.Context, uint64) (string, string, error) { return clusterEnv, "cluster-a", nil }, nil)
	check := func(id uint64, value, effective, source string) {
		t.Helper()
		got, err := uc.SetEnvironment(ctx, id, value)
		if err != nil || got.Environment != strings.TrimSpace(value) || got.EffectiveEnvironment != effective || got.Source != source {
			t.Fatalf("environment: %+v, %v", got, err)
		}
		got, err = uc.ResolveEnvironment(ctx, id)
		if err != nil || got.EffectiveEnvironment != effective || got.Source != source {
			t.Fatalf("read environment: %+v, %v", got, err)
		}
	}
	check(1, " staging ", "staging", "device")
	check(1, "", "", "unset")
	check(2, "", "production", "cluster")
	check(2, "test", "test", "device")
	clusterEnv = "development"
	got, err := uc.ResolveEnvironment(ctx, 2)
	if err != nil || got.EffectiveEnvironment != "test" || got.InheritedEnvironment != "development" {
		t.Fatalf("override lost: %+v %v", got, err)
	}
	check(2, "", "development", "cluster")
	for _, invalid := range []string{"prod\n", "a\x00b", "${ENV}", strings.Repeat("x", 257)} {
		if _, err := uc.SetEnvironment(ctx, 2, invalid); !errors.Is(err, errs.ErrInvalid) {
			t.Fatalf("accepted invalid %q: %v", invalid, err)
		}
	}
	if *repo.byID[2].Environment != "" {
		t.Fatal("invalid input changed environment")
	}
	if _, err := uc.SetEnvironment(ctx, 999, "test"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("missing device: %v", err)
	}
}

func TestDeviceEnvironmentNotifiesOnlyHostCollectors(t *testing.T) {
	repo := &fakeRepo{byID: map[uint64]*devicemodel.Device{1: {ID: 1}}}
	links := &fakeLinks{rows: []*devicemodel.EdgeDevice{{EdgeID: 67, Type: devicemodel.EdgeDeviceRelationHost}, {EdgeID: 68, Type: devicemodel.EdgeDeviceRelationDiscovered}}}
	uc := NewUsecase(repo, links, nil)
	var notified []uint64
	uc.SetEnvironmentProvider(nil, func(_ context.Context, id uint64) { notified = append(notified, id) })
	if _, err := uc.SetEnvironment(context.Background(), 1, "production"); err != nil {
		t.Fatal(err)
	}
	if len(notified) != 1 || notified[0] != 67 {
		t.Fatalf("notified collectors: %v", notified)
	}
}
