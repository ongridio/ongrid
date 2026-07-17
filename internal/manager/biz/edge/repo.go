package edge

import (
	"context"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/edge"
)

// ListFilter is the parameter object for Repo.List / Usecase.List.
//
// All filters are optional. Status is exact-match ("", "online", "offline").
// Name is a substring match (LIKE %name%). CreatedBy, when non-nil,
// restricts to edges created by that user id. Limit / Offset apply after
// filtering. Roles filtering moved to model/device.Device after the May
// 2026 split — query the device repo for that.
type ListFilter struct {
	Status    string
	Name      string
	CreatedBy *uint64
	Limit     int
	Offset    int
}

// Repo is the manager/edge persistence contract. Implemented in
// internal/manager/data/edge/store. Post-pivot there is no org_id
// parameter.
type Repo interface {
	Create(ctx context.Context, e *model.Edge) error
	GetByID(ctx context.Context, id uint64) (*model.Edge, error)
	GetByAccessKey(ctx context.Context, accessKey string) (*model.Edge, error)
	GetByName(ctx context.Context, name string) (*model.Edge, error)
	List(ctx context.Context, f ListFilter) ([]*model.Edge, error)
	UpdateSecretHash(ctx context.Context, id uint64, hash string) error
	UpdateStatus(ctx context.Context, id uint64, status string, lastSeen time.Time) error
	// UpdateName overwrites the operator-friendly display name. Used
	// by HandleRegister to back-fill empty names with the host's
	// reported hostname on first tunnel handshake.
	UpdateName(ctx context.Context, id uint64, name string) error
	// SetDeviceID links an edge row to its host Device after register.
	// Source of truth for the junction is the edge_devices table; this
	// field is kept in sync as a convenience pointer.
	SetDeviceID(ctx context.Context, edgeID, deviceID uint64) error
	// ClearDeviceID removes the convenience host Device pointer. Used
	// when a non-host edge role, such as Kubernetes controller, needs to
	// self-heal an old mistaken host registration.
	ClearDeviceID(ctx context.Context, edgeID uint64) error
	// SetAgentVersion records the agent's self-reported binary version
	// (semver-ish, e.g. "0.7.43"). Updated on register_edge whenever
	// the value changes — empty inputs are filtered upstream.
	SetAgentVersion(ctx context.Context, id uint64, version string) error
	Delete(ctx context.Context, id uint64) error // soft delete
	Count(ctx context.Context) (int64, error)
}
