package tools

import (
	"context"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"sort"
	"strings"
	"testing"
)

type hostInventoryFixture struct {
	devicebiz.Repo
	hosts map[uint64]*model.Device
}

func (f *hostInventoryFixture) Get(_ context.Context, id uint64) (*model.Device, error) {
	if h := f.hosts[id]; h != nil {
		return h, nil
	}
	return nil, errs.ErrNotFound
}
func (f *hostInventoryFixture) List(_ context.Context, filter devicebiz.ListFilter) ([]*model.Device, error) {
	out := make([]*model.Device, 0, len(f.hosts))
	for _, h := range f.hosts {
		if (filter.Name == "" || strings.Contains(h.Name, filter.Name)) && (filter.Hostname == "" || strings.Contains(h.Hostname, filter.Hostname)) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

type hostLinksFixture struct {
	devicebiz.EdgeDeviceRepo
	hosts   map[uint64]*model.Device
	reverse map[uint64]uint64
}

func (f *hostLinksFixture) LookupHostDevice(_ context.Context, id uint64) (uint64, error) {
	if host := f.reverse[id]; host != 0 {
		return host, nil
	}
	return 0, errs.ErrNotFound
}
func (f *hostLinksFixture) LookupEdgeForDevice(_ context.Context, id uint64, _ model.EdgeDeviceRelationType) (uint64, error) {
	if h := f.hosts[id]; h != nil && h.CurrentEdgeID != nil {
		return *h.CurrentEdgeID, nil
	}
	return 0, errs.ErrNotFound
}

// Existing fixtures explicitly seed both identities. Different-ID regressions set DeviceID.
func testHostInventory(t *testing.T, edges *edgebiz.Usecase) *devicebiz.Usecase {
	t.Helper()
	rows, err := edges.List(context.Background(), edgebiz.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[uint64]*model.Device{}
	reverse := map[uint64]uint64{}
	for _, e := range rows {
		id := e.ID
		if e.DeviceID != nil {
			id = *e.DeviceID
		} else {
			e.DeviceID = &id
		}
		reverse[e.ID] = id
		if old := hosts[id]; old != nil && *old.CurrentEdgeID > e.ID {
			continue
		}
		edgeID := e.ID
		hosts[id] = &model.Device{ID: id, Name: e.Name, Hostname: e.Name, CurrentEdgeID: &edgeID, Online: e.Status == "online"}
	}
	return devicebiz.NewUsecase(&hostInventoryFixture{hosts: hosts}, &hostLinksFixture{hosts: hosts, reverse: reverse}, nil)
}
