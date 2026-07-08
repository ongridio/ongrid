package tools

import "testing"

func TestClassifyToolCategoryGroupsKubernetesToolsIntoExistingCategories(t *testing.T) {
	tests := map[string]string{
		"query_k8s_snapshot":    "telemetry",
		"describe_k8s_resource": "telemetry",
		"query_k8s_logs":        "telemetry",
		"execute_k8s_action":    "other",
	}

	for name, want := range tests {
		if got := classifyToolCategory(name); got != want {
			t.Fatalf("classifyToolCategory(%q) = %q, want %q", name, got, want)
		}
	}
}
