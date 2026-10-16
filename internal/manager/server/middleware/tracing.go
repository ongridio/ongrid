package middleware

import (
	"net/http"
	"strings"
)

func ShouldTraceHTTPRequest(r *http.Request) bool {
	path := r.URL.Path
	return path != "/healthz" &&
		path != "/readyz" &&
		path != "/api/v1/prometheus/auth" &&
		path != "/api/v1/traces" &&
		!strings.HasPrefix(path, "/api/v1/traces/") &&
		path != "/api/v1/agent-observability" &&
		!strings.HasPrefix(path, "/api/v1/agent-observability/")
}
