// Package relationtype implements the CMDB relation-type dictionary: the
// "runs on / belongs to / connects to" vocabulary shared by model-level
// relations and CI instance relations.
//
// It is the smallest entity in the CMDB (a name and a colour), which makes it
// the module that proves the server -> biz -> data -> model path end to end
// before the larger modules are built on it.
package relationtype

import (
	"context"

	citypemodel "github.com/ongridio/ongrid/internal/cmdb/model/citype"
)

// Repo is the consumer-side persistence port for relation types.
type Repo interface {
	// List returns every live relation type ordered by id.
	List(ctx context.Context) ([]citypemodel.RelationType, error)

	// Get returns one by id, or nil when it does not exist.
	Get(ctx context.Context, id int32) (*citypemodel.RelationType, error)

	// FindByName returns the live relation type with this exact name, or nil.
	// Names are matched exactly, matching cantor's `get_by(name=...)`.
	FindByName(ctx context.Context, name string) (*citypemodel.RelationType, error)

	// Create inserts a relation type.
	Create(ctx context.Context, rt *citypemodel.RelationType) error

	// Update writes name and colour of an existing row.
	Update(ctx context.Context, rt *citypemodel.RelationType) error

	// SoftDelete marks a relation type deleted.
	SoftDelete(ctx context.Context, id int32) error
}
