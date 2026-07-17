package procmetrics

import (
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

func TestBuildArgsWithProcFS(t *testing.T) {
	args := buildArgs(plugins.PluginConfig{
		Spec: map[string]interface{}{
			"listen_address": ":19256",
			"procfs":         "/host/proc",
		},
	}, "/tmp/process-exporter.yaml")

	for _, want := range []string{
		"-web.listen-address=:19256",
		"-config.path=/tmp/process-exporter.yaml",
		"-procfs=/host/proc",
	} {
		if !contains(args, want) {
			t.Fatalf("args missing %q in %#v", want, args)
		}
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
