package edge

import "testing"

func TestIsKnownPluginNameIncludesMetricChildren(t *testing.T) {
	for _, name := range []string{
		PluginNameMetrics,
		PluginNameHostMetrics,
		PluginNameProcMetrics,
		PluginNameCustomMetrics,
		PluginNameDatabaseMetrics,
	} {
		if !IsKnownPluginName(name) {
			t.Fatalf("IsKnownPluginName(%q) = false, want true", name)
		}
	}
}
