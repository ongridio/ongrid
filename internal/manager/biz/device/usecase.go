// Package device — biz Usecase facade. Wraps Repo + EdgeDeviceRepo so
// the HTTP handler doesn't have to thread two dependencies through.
package device

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	model "github.com/ongridio/ongrid/internal/manager/model/device"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Usecase is the manager/device biz-layer facade.
type Usecase struct {
	repo  Repo
	links EdgeDeviceRepo
	log   *slog.Logger
}

// NewUsecase builds the usecase. links may be nil — junction-aware methods
// will return ErrNotWiredYet so callers degrade gracefully. log may be nil.
func NewUsecase(repo Repo, links EdgeDeviceRepo, log *slog.Logger) *Usecase {
	return &Usecase{repo: repo, links: links, log: log}
}

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

// Delete removes an offline device plus its linked Edge identities. Online
// devices are rejected so a live host cannot lose its access key while it is
// still connected. The repository owns the transaction because it touches
// devices, edge_devices, and edges together.
func (u *Usecase) Delete(ctx context.Context, id uint64) error {
	if u.repo == nil {
		return errs.ErrNotWiredYet
	}
	return u.repo.DeleteOfflineWithLinkedEdges(ctx, id)
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
