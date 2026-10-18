package basetool

import "testing"

func TestHostToolAliasesPreservePolicyNames(t *testing.T) {
	for old, current := range map[string]string{"rank_edges": "rank_hosts", "get_edge_summary": "get_host_summary", "find_outlier_edges": "find_outlier_hosts"} {
		if CanonicalName(old) != current || CanonicalName(current) != current {
			t.Fatalf("alias %s -> %s", old, current)
		}
		names := PolicyNames(current)
		if len(names) != 2 || names[0] != current || names[1] != old {
			t.Fatalf("policy alias lost: %v", names)
		}
	}
}
