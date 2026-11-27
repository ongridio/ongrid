package mcpclient

import "strings"

// ToolClass infers an MCP tool's risk class from its name. MCP servers
// rarely set the readOnlyHint annotation, so we read the verb: pure-read
// queries (k8s list/get/log/top/stats/view/...) are "read" and can be
// single-node test-run; anything naming a mutating verb — or unknown — stays
// "destructive" so it is gated to full-flow runs / approval. Decoupled from the
// server's `trusted` flag, which governs only the sync-vs-approval run path.
func ToolClass(bareName string) string {
	n := strings.ToLower(bareName)
	for _, v := range []string{"delete", "remove", "create", "apply", "exec", "scale", "restart", "patch", "update", "drain", "cordon", "rollout", "evict", "kill", "destroy", "stop", "start", "attach", "write"} {
		if strings.Contains(n, v) {
			return "destructive"
		}
	}
	for _, v := range []string{"list", "get", "read", "view", "describe", "log", "top", "stat", "status", "summary", "event", "watch", "search", "info", "query", "show", "fetch", "inspect", "config", "cat", "tail", "head"} {
		if strings.Contains(n, v) {
			return "read"
		}
	}
	return "destructive"
}
