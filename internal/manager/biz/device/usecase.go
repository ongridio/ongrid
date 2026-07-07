// Package device — biz Usecase facade. Wraps Repo + EdgeDeviceRepo so
// the HTTP handler doesn't have to thread two dependencies through.
package device

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase is the manager/device biz-layer facade.
type Usecase struct {
	repo     Repo
	links    EdgeDeviceRepo
	topology TopologyMirror
	log      *slog.Logger
}

// NewUsecase builds the usecase. links may be nil — junction-aware methods
// will return ErrNotWiredYet so callers degrade gracefully. log may be nil.
func NewUsecase(repo Repo, links EdgeDeviceRepo, log *slog.Logger) *Usecase {
	return &Usecase{repo: repo, links: links, log: log}
}

// TopologyMirror is the optional device → topology cleanup bridge. The
// topology usecase implements it; keeping the interface here avoids a
// device → topology package dependency.
type TopologyMirror interface {
	DeleteNodeForDevice(ctx context.Context, deviceID, nodeID uint64) error
}

func (u *Usecase) SetTopologyMirror(m TopologyMirror) { u.topology = m }

// Repo returns the underlying device Repo for callers that need direct
// access (e.g. the edge HTTP handler hydrating host_info on the listing
// response).
func (u *Usecase) Repo() Repo { return u.repo }

// Links returns the underlying junction repo. May be nil.
func (u *Usecase) Links() EdgeDeviceRepo { return u.links }

// ReconcilePresence flips orphan "ghost" devices (online=true with no
// online linked edge) back to offline and returns how many it healed.
// Called once at boot and then on a ticker so device presence converges
// even across manager restarts and hard edge deletes — the per-event
// MarkOnline/MarkOffline paths can't see an edge that no longer exists.
func (u *Usecase) ReconcilePresence(ctx context.Context) (int64, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	n, err := u.repo.ReconcileOfflineOrphans(ctx)
	if err != nil {
		return 0, err
	}
	if n > 0 && u.log != nil {
		u.log.Info("device presence reconcile: flipped orphan devices offline", "count", n)
	}
	return n, nil
}

// Get returns one device by id.
func (u *Usecase) Get(ctx context.Context, id uint64) (*model.Device, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.Get(ctx, id)
}

// List returns devices matching f.
func (u *Usecase) List(ctx context.Context, f ListFilter) ([]*model.Device, error) {
	if u.repo == nil {
		return nil, errs.ErrNotWiredYet
	}
	return u.repo.List(ctx, f)
}

// UpdateRoles assigns the device-roles bit set used for sidebar grouping
// and AI prompt routing. Names is the canonical wire shape ("server" /
// "storage" / "network" / "database"); the special "unknown" name (or
// an empty list) clears the bit set. Names outside the canonical enum
// are rejected so a silent typo can't park a device in a phantom bucket.
func (u *Usecase) UpdateRoles(ctx context.Context, id uint64, names []string) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !model.IsValidRoleName(n) {
			return fmt.Errorf("%w: invalid role %q", errs.ErrInvalid, n)
		}
	}
	roles := model.EncodeRoles(names)
	if !model.IsValidRoles(roles) {
		return fmt.Errorf("%w: invalid roles bit set", errs.ErrInvalid)
	}
	if err := u.repo.UpdateRoles(ctx, id, roles); err != nil {
		return err
	}
	if u.log != nil {
		u.log.Info("device roles updated", "id", id, "roles", roles, "names", model.DecodeRoles(roles))
	}
	return nil
}

// UpdateNameDescription updates operator-editable display fields.
func (u *Usecase) UpdateNameDescription(ctx context.Context, id uint64, name, description string) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	return u.repo.UpdateNameDescription(ctx, id, strings.TrimSpace(name), strings.TrimSpace(description))
}

// Delete soft-deletes a device and removes the device-owned topology node
// plus edge junction rows so deleted devices cannot leak into topology views.
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	d, err := u.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := u.deleteTopologyNode(ctx, d); err != nil {
		return err
	}
	if err := u.unlinkDeviceEdges(ctx, id); err != nil {
		return err
	}
	return u.repo.Delete(ctx, id)
}

type deletedTopologyDeviceLister interface {
	ListDeletedWithNodeID(ctx context.Context, limit int) ([]*model.Device, error)
}

type orphanDeviceLister interface {
	ListWithoutLiveEdges(ctx context.Context, limit int) ([]*model.Device, error)
}

// ReconcileDeletedTopology removes topology nodes that belong to devices
// deleted before the topology cleanup hook existed.
func (u *Usecase) ReconcileDeletedTopology(ctx context.Context) (int, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	if u.topology == nil {
		return 0, nil
	}
	lister, ok := u.repo.(deletedTopologyDeviceLister)
	if !ok {
		return 0, nil
	}
	rows, err := lister.ListDeletedWithNodeID(ctx, 5000)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, d := range rows {
		if d == nil {
			continue
		}
		if err := u.deleteTopologyNode(ctx, d); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	if cleaned > 0 && u.log != nil {
		u.log.Info("device topology reconcile: removed deleted device nodes", "count", cleaned)
	}
	return cleaned, nil
}

// ReconcileOrphanDevices removes live device rows that no longer have any
// live edge bound to them. This heals devices left behind by the older
// DELETE /edges path used by the devices page.
func (u *Usecase) ReconcileOrphanDevices(ctx context.Context) (int, error) {
	if u.repo == nil {
		return 0, errs.ErrNotWiredYet
	}
	lister, ok := u.repo.(orphanDeviceLister)
	if !ok {
		return 0, nil
	}
	rows, err := lister.ListWithoutLiveEdges(ctx, 5000)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, d := range rows {
		if d == nil {
			continue
		}
		if err := u.Delete(ctx, d.ID); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	if cleaned > 0 && u.log != nil {
		u.log.Info("device reconcile: removed devices without live edges", "count", cleaned)
	}
	return cleaned, nil
}

func (u *Usecase) deleteTopologyNode(ctx context.Context, d *model.Device) error {
	if u.topology == nil || d == nil || d.NodeID == nil || *d.NodeID == 0 {
		return nil
	}
	if err := u.topology.DeleteNodeForDevice(ctx, d.ID, *d.NodeID); err != nil && !errors.Is(err, errs.ErrNotFound) {
		return fmt.Errorf("delete topology node for device %d: %w", d.ID, err)
	}
	return nil
}

func (u *Usecase) unlinkDeviceEdges(ctx context.Context, id uint64) error {
	if u.links == nil {
		return nil
	}
	rows, err := u.links.ListEdgesForDevice(ctx, id)
	if err != nil {
		return fmt.Errorf("list device edge links: %w", err)
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if err := u.links.Unlink(ctx, row.EdgeID, row.DeviceID, row.Type); err != nil {
			return fmt.Errorf("unlink device edge %d/%d: %w", row.EdgeID, row.DeviceID, err)
		}
	}
	return nil
}

// LookupHostDevice resolves edge → host device_id. Returns 0,
// ErrNotFound when the edge has no Type=Host junction yet (race during
// register).
func (u *Usecase) LookupHostDevice(ctx context.Context, edgeID uint64) (uint64, error) {
	if u.links == nil {
		return 0, errs.ErrNotWiredYet
	}
	return u.links.LookupHostDevice(ctx, edgeID)
}

// LookupEdgeForDevice resolves device → owning edge_id (type=host).
func (u *Usecase) LookupEdgeForDevice(ctx context.Context, deviceID uint64) (uint64, error) {
	if u.links == nil {
		return 0, errs.ErrNotWiredYet
	}
	return u.links.LookupEdgeForDevice(ctx, deviceID, model.EdgeDeviceRelationHost)
}

// LinkHost upserts the (edge, device, type=host) junction row. Called
// from the edge register flow.
func (u *Usecase) LinkHost(ctx context.Context, edgeID, deviceID uint64) error {
	if u.links == nil {
		return errs.ErrNotWiredYet
	}
	return u.links.Link(ctx, edgeID, deviceID, model.EdgeDeviceRelationHost)
}
