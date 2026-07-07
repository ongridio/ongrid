package device

import (
	"context"
	"testing"

	devicemodel "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func TestDeleteCleansTopologyAndEdgeLinks(t *testing.T) {
	ctx := context.Background()
	nodeID := uint64(44)
	repo := &fakeRepo{byID: map[uint64]*devicemodel.Device{
		7: {ID: 7, Name: "node-a", NodeID: &nodeID},
	}}
	links := &fakeLinks{rows: []*devicemodel.EdgeDevice{
		{EdgeID: 11, DeviceID: 7, Type: devicemodel.EdgeDeviceRelationHost},
		{EdgeID: 12, DeviceID: 7, Type: devicemodel.EdgeDeviceRelationDiscovered},
	}}
	mirror := &fakeTopologyMirror{}
	uc := NewUsecase(repo, links, nil)
	uc.SetTopologyMirror(mirror)

	if err := uc.Delete(ctx, 7); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(mirror.deleted) != 1 || mirror.deleted[0] != [2]uint64{7, 44} {
		t.Fatalf("topology deletes = %#v, want device/node 7/44", mirror.deleted)
	}
	if len(links.unlinked) != 2 {
		t.Fatalf("unlinked = %#v, want 2 links", links.unlinked)
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

func (r *fakeRepo) FindOrCreateByFingerprint(context.Context, *devicemodel.Device) (*devicemodel.Device, error) {
	return nil, nil
}

func (r *fakeRepo) RebindFingerprint(context.Context, string, string) error { return nil }

func (r *fakeRepo) UpdateHostFacts(context.Context, uint64, HostFacts) error { return nil }

func (r *fakeRepo) UpdateUsage(context.Context, uint64, Usage) error { return nil }

func (r *fakeRepo) UpdateRoles(context.Context, uint64, uint8) error { return nil }

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
}

func (l *fakeLinks) Link(context.Context, uint64, uint64, devicemodel.EdgeDeviceRelationType) error {
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
