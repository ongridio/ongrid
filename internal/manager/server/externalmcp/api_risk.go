package externalmcp

import (
	"slices"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
)

// Only reviewed, non-destructive actions bypass confirmation. New actions fail closed.
func (op apiOperation) lowRisk(a biz.Arguments) bool {
	switch op.ID {
	case "post_topology_nodes", "post_topology_relations", "post_chat_sessions":
		return true
	case "patch_topology_nodes_id":
		return metadataOnly(a, "name")
	case "patch_chat_sessions_id":
		return metadataOnly(a, "title")
	default:
		return false
	}
}

func metadataOnly(a biz.Arguments, fields ...string) bool {
	body, ok := a["body"].(map[string]any)
	if !ok || len(body) == 0 {
		return false
	}
	for key := range body {
		if !slices.Contains(fields, key) {
			return false
		}
	}
	return true
}
