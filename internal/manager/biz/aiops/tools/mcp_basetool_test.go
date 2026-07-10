package tools

import "testing"

func TestMCPToolClassElasticsearchReadTools(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"elasticsearch_search",
		"indices_stats",
		"cluster_health",
		"count_documents",
		"msearch",
		"get_mapping",
	} {
		if got := MCPToolClass(name); got != "read" {
			t.Fatalf("MCPToolClass(%q) = %s, want read", name, got)
		}
	}
}

func TestMCPToolClassMutationWinsOverReadToken(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"delete_by_query",
		"update_index_mapping",
		"restart_cluster",
	} {
		if got := MCPToolClass(name); got != "destructive" {
			t.Fatalf("MCPToolClass(%q) = %s, want destructive", name, got)
		}
	}
}
