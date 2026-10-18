package basetool

// CanonicalName keeps stored tool configuration compatible without exposing old names to the model.
func CanonicalName(name string) string {
	switch name {
	case "rank_edges":
		return "rank_hosts"
	case "find_outlier_edges":
		return "find_outlier_hosts"
	case "get_edge_summary":
		return "get_host_summary"
	default:
		return name
	}
}

// PolicyNames also preserves old blacklist patterns after a tool is renamed.
func PolicyNames(name string) []string {
	switch CanonicalName(name) {
	case "rank_hosts":
		return []string{"rank_hosts", "rank_edges"}
	case "find_outlier_hosts":
		return []string{"find_outlier_hosts", "find_outlier_edges"}
	case "get_host_summary":
		return []string{"get_host_summary", "get_edge_summary"}
	default:
		return []string{name}
	}
}
