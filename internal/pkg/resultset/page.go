// Package resultset reads a selected collection and its upstream pagination metadata.
package resultset

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
)

type Selection struct {
	Rows  string `json:"rows_pointer"`
	Total string `json:"total_pointer,omitempty"`
	Next  string `json:"next_pointer,omitempty"`
}

type Page struct {
	Rows         []any
	Total        *int
	Next         string
	HasNextField bool
	HasMore      bool
}

func Decode(r io.Reader) (any, error) {
	d := json.NewDecoder(r)
	d.UseNumber()
	var root any
	if err := d.Decode(&root); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return root, nil
}

// Look for conventional metadata next to the array, then in its ancestors.
// Explicit paths are authoritative and must resolve.
func (s Selection) field(root any, path string, keys ...string) (any, bool, error) {
	if path != "" {
		v, ok := resultfile.Pointer(root, path)
		if !ok {
			return nil, false, fmt.Errorf("missing pagination field %q", path)
		}
		return v, true, nil
	}
	parent := s.Rows
	for {
		idx := strings.LastIndexByte(parent, '/')
		if idx < 0 {
			return nil, false, nil
		}
		parent = parent[:idx]
		for _, key := range keys {
			if v, ok := resultfile.Pointer(root, parent+"/"+key); ok {
				return v, true, nil
			}
		}
	}
}

func (s Selection) Page(root any) (Page, error) {
	var p Page
	rows, _ := resultfile.Pointer(root, s.Rows)
	p.Rows, _ = rows.([]any)
	if p.Rows == nil {
		return p, fmt.Errorf("no row array at %q", s.Rows)
	}
	v, ok, err := s.field(root, s.Total, "total", "totalCount", "total_count")
	if err != nil {
		return p, err
	}
	if ok {
		n, err := strconv.Atoi(fmt.Sprint(v))
		if err != nil || n < 0 {
			return p, fmt.Errorf("invalid upstream total")
		}
		p.Total = &n
	}
	v, ok, err = s.field(root, s.Next, "nextCursor", "next_cursor", "nextPageToken", "next_page_token")
	if err != nil {
		return p, err
	}
	p.HasNextField = ok
	if ok && v != nil {
		var isText bool
		p.Next, isText = v.(string)
		if !isText {
			return p, fmt.Errorf("invalid upstream cursor")
		}
	}
	v, ok, err = s.field(root, "", "hasMore", "has_more", "hasNextPage")
	if err != nil {
		return p, err
	}
	if ok {
		var valid bool
		p.HasMore, valid = v.(bool)
		if !valid {
			return p, fmt.Errorf("invalid upstream has-more flag")
		}
		p.HasNextField = true
	}
	return p, nil
}
