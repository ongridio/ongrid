package citype

import (
	"bytes"
	"encoding/json"
	"fmt"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// TypePair is one model-to-model step in a service-tree definition.
type TypePair struct {
	ParentID cmdbmodel.ID `json:"parent_id"`
	ChildID  cmdbmodel.ID `json:"child_id"`
}

// TypePairList is cantor's c_prv.cr_ids: a JSON array of parent/child model
// pairs, in the order the user built the tree.
//
// The ORDER IS THE DEFINITION. A service tree is a sequence of steps down
// through the model graph, and cantor's renderer reads the list positionally —
// it pairs element i with element i+1 to look up the cardinality between them.
// A set would lose the tree.
//
// The declaration is a slice rather than a map because these columns hold JSON
// ARRAYS; see IDList for the same lesson learned on c_c_t_u_c.attr_ids.
type TypePairList []TypePair

// UnmarshalJSON tolerates the column being NULL or empty.
//
// cr_ids is nullable, and cantor's renderer iterated it without a guard, so a
// row written without one raised TypeError on read and answered 500. The port
// reads it as an empty list, and the definition then renders as an empty tree
// rather than taking the endpoint down.
func (t *TypePairList) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*t = nil
		return nil
	}
	var pairs []TypePair
	if err := json.Unmarshal(trimmed, &pairs); err != nil {
		return fmt.Errorf("cmdb service tree: cr_ids: %w", err)
	}
	*t = pairs
	return nil
}

// MarshalJSON always writes an array, so a definition read from a NULL column
// is normalised the first time it is written back.
func (t TypePairList) MarshalJSON() ([]byte, error) {
	if t == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]TypePair(t))
}

// PreferenceRelationView is a named service-tree definition (c_prv).
//
// A definition is a chain of model pairs the user assembled: "start at IDC, go
// down to server room, then to rack, then to host". The resource-management
// service tree READS these and expands the real configuration items along the
// chain; the model-management page writes them. That read/write split is why
// the table is shared between two menus.
//
// Rows are PER-USER (uid), and IsPublic shares a definition with everyone.
type PreferenceRelationView struct {
	cmdbmodel.Base

	UID  cmdbmodel.ID `gorm:"column:uid;not null;index:idx_c_prv_uid"`
	Name string       `gorm:"column:name;size:64;not null;index:idx_c_prv_name"`

	// CRIDs is the chain, in order. See TypePairList.
	CRIDs TypePairList `gorm:"column:cr_ids;serializer:json"`

	IsPublic bool `gorm:"column:is_public;not null;default:false"`

	// Option is cantor's open-ended per-view configuration. It is kept as a map
	// so a definition carrying keys a later cantor added survives an edit here.
	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (PreferenceRelationView) TableName() string { return "c_prv" }

// PreferenceSearchOption is one saved search (c_pso).
//
// A saved search can be scoped to a service-tree definition (PRVID), a tree
// view (PTVID) or a single model (TypeID), and cantor's three magic names are
// the console's own lists rather than user searches:
//
//	__recent__            the last twenty searches the user ran
//	__favor__             the user's favourites
//	__relation_favor__    favourites within a service tree
//
// The port keeps those names verbatim: they are persisted data, and a console
// that renamed them would orphan every row already stored.
type PreferenceSearchOption struct {
	cmdbmodel.Base

	Name *string `gorm:"column:name;size:64"`

	PRVID  *cmdbmodel.ID `gorm:"column:prv_id;index:idx_c_pso_prv_id"`
	PTVID  *cmdbmodel.ID `gorm:"column:ptv_id;index:idx_c_pso_ptv_id"`
	TypeID *cmdbmodel.ID `gorm:"column:type_id;index:idx_c_pso_type_id"`

	UID *cmdbmodel.ID `gorm:"column:uid;index:idx_c_pso_uid"`

	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (PreferenceSearchOption) TableName() string { return "c_pso" }

// The three reserved saved-search names.
const (
	// SearchOptionRecent is the rolling list of searches the user ran. Only the
	// newest entries are kept.
	SearchOptionRecent = "__recent__"

	// SearchOptionFavourite is the user's saved favourites.
	SearchOptionFavourite = "__favor__"

	// SearchOptionRelationFavourite is favourites scoped to a service tree.
	SearchOptionRelationFavourite = "__relation_favor__"
)

// SearchOptionRecentKeep is how many recent searches survive a write.
const SearchOptionRecentKeep = 20

// PreferenceTreeView is a user's choice of which attributes a model's
// hierarchy displays at each level (c_ptv).
//
// CANTOR'S NAME, KEPT. cantor calls this `PreferenceTreeView` and the console's
// 树视图 page is what writes it; it is NOT the same thing as
// PreferenceRelationView (c_prv) above, which defines the SHAPE of a service
// tree as a chain of models. This one answers a narrower question — for one
// model, which column does level 1 show, which does level 2 show — and it is
// what the resource-hierarchy page renders its rows with.
//
// Levels is a JSON ARRAY OF ATTRIBUTE IDS, in display order, one entry per
// depth of the hierarchy. It is an IDList and not a []model.ID for the reason
// that type documents: cantor has written this column as an array, as a bare
// scalar and as null over its lifetime, and an unmarshaller that accepted only
// one of those shapes would make every row it wrote unreadable.
//
// An empty rows set means the user has not customised the model, and the page
// falls back to the model's own defaults — which is why a write of an empty
// level list DELETES the row rather than storing an empty array.
type PreferenceTreeView struct {
	cmdbmodel.Base

	UID cmdbmodel.ID `gorm:"column:uid;not null;index:idx_c_ptv_uid"`

	TypeID cmdbmodel.ID `gorm:"column:type_id;not null;index:idx_c_ptv_type_id"`

	Levels IDList `gorm:"column:levels;serializer:json"`
}

func (PreferenceTreeView) TableName() string { return "c_ptv" }
