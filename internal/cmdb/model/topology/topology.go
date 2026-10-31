// Package topology holds the saved topology views.
//
// A VIEW IS A SAVED WALK, not a saved graph: it names a model, a query that
// selects its roots, and the relation levels to follow below (+) and above (-)
// them. The graph is assembled from the current relations every time it is
// opened, so a view does not go stale when an instance or an edge changes.
package topology

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// ViewGroup is a sidebar folder (c_topology_view_groups).
type ViewGroup struct {
	cmdbmodel.Base

	Name  string `gorm:"column:name;size:64;index:idx_c_topology_view_groups_name"`
	Order int    `gorm:"column:order;not null;default:0"`
}

func (ViewGroup) TableName() string { return "c_topology_view_groups" }

// View is one saved topology view (c_topology_views).
type View struct {
	cmdbmodel.Base

	Name string `gorm:"column:name;size:64;index:idx_c_topology_views_name"`

	GroupID *cmdbmodel.ID `gorm:"column:group_id;index:idx_c_topology_views_group_id"`

	Category string `gorm:"column:category;size:32"`

	// CentralNodeType is the model the roots belong to.
	CentralNodeType *cmdbmodel.ID `gorm:"column:central_node_type"`

	// CentralNodeInstances is the root query. cantor stored a query-language
	// expression here, optionally prefixed with `q=`.
	CentralNodeInstances string `gorm:"column:central_node_instances;type:text"`

	// Path maps a level to the model ids allowed at it. Positive levels walk
	// down from the roots, negative levels walk up.
	Path Path `gorm:"column:path;serializer:json"`

	Order int `gorm:"column:order;not null;default:0"`

	// Option is the console's layout data.
	Option map[string]any `gorm:"column:option;serializer:json"`
}

func (View) TableName() string { return "c_topology_views" }

// Path is the stored level map.
//
// CANTOR WROTE THE MODEL IDS AS STRINGS. An adopted row therefore contains
// `{"1": ["2"]}`, which encoding/json refuses to put into []int32. Decoding
// both forms keeps one legacy row from making every topology read fail.
type Path map[string][]cmdbmodel.ID

func (p *Path) UnmarshalJSON(data []byte) error {
	var raw map[string][]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Path, len(raw))
	for level, items := range raw {
		ids := make([]cmdbmodel.ID, 0, len(items))
		for _, item := range items {
			var id cmdbmodel.ID
			if err := json.Unmarshal(item, &id); err == nil {
				ids = append(ids, id)
				continue
			}
			var text string
			if err := json.Unmarshal(item, &text); err != nil {
				return fmt.Errorf("topology path %q: %w", level, err)
			}
			n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
			if err != nil {
				return fmt.Errorf("topology path %q: %w", level, err)
			}
			ids = append(ids, cmdbmodel.ID(n))
		}
		out[level] = ids
	}
	*p = out
	return nil
}
