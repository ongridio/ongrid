package citype

import (
	"bytes"
	"encoding/json"
	"fmt"

	cmdbmodel "github.com/ongridio/ongrid/internal/cmdb/model"
)

// IDList is cantor's JSON attribute-id set, as stored in
// c_ci_type_relations.parent_attr_ids / child_attr_ids and
// c_c_t_u_c.attr_ids.
//
// THESE COLUMNS ARE JSON ARRAYS, and the type has to say so. A field declared
// as map[string]any unmarshals `[1,2,3]` into an error — "cannot unmarshal
// array into Go value of type map[string]interface {}" — so every read of a row
// cantor wrote would fail, including the read that a delete cascade performs.
//
// The unmarshaller also accepts the two other shapes cantor's own helper
// tolerated, because the column has been through several migrations and an
// older deployment wrote a bare integer:
//
//	null      -> empty
//	7         -> [7]
//	[1,2,3]   -> [1,2,3]
//
// cantor copied the value before normalising it so its `list.remove` would not
// mutate the ORM object. Unmarshalling produces a fresh slice every time, which
// makes that hazard structurally impossible rather than merely avoided.
type IDList []cmdbmodel.ID

// UnmarshalJSON accepts an array, a single scalar id, or null.
func (a *IDList) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*a = nil
		return nil
	}

	if trimmed[0] == '[' {
		var ids []cmdbmodel.ID
		if err := json.Unmarshal(trimmed, &ids); err != nil {
			return fmt.Errorf("cmdb attribute id list: %w", err)
		}
		*a = ids
		return nil
	}

	// The legacy scalar form. A JSON string holding an id is accepted too:
	// some exports quote their numbers.
	var one cmdbmodel.ID
	if err := json.Unmarshal(trimmed, &one); err == nil {
		*a = IDList{one}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return fmt.Errorf("cmdb attribute id list: unexpected shape %s", trimmed)
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("cmdb attribute id list: unexpected scalar %q", s)
	}
	*a = IDList{cmdbmodel.ID(n)}
	return nil
}

// MarshalJSON always writes an array, so a value read in the legacy scalar form
// is normalised the first time it is written back.
func (a IDList) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("null"), nil
	}
	return json.Marshal([]cmdbmodel.ID(a))
}

// Contains reports whether the set holds an id.
func (a IDList) Contains(id cmdbmodel.ID) bool {
	for _, x := range a {
		if x == id {
			return true
		}
	}
	return false
}

// Without returns a copy of the set with an id removed. The receiver is not
// modified: the stored value is shared with whatever else read it.
func (a IDList) Without(id cmdbmodel.ID) IDList {
	out := make(IDList, 0, len(a))
	for _, x := range a {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}
