package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ongridio/ongrid/internal/pkg/autoapm"
)

type AutoAPMOptions struct {
	Environments []string `json:"environments"`
	Namespaces   []string `json:"namespaces"`
}

// AutoAPMOptions derives reusable values from persisted settings, including
// offline devices. No separate catalogue or browser-local history is needed.
func (uc *PluginConfigUC) AutoAPMOptions(ctx context.Context) (*AutoAPMOptions, error) {
	specs, err := uc.repo.ListAutoAPMSpecs(ctx)
	if err != nil {
		return nil, err
	}
	if uc.kubernetesAutoAPMSpecs != nil {
		clusterSpecs, err := uc.kubernetesAutoAPMSpecs(ctx)
		if err != nil {
			return nil, err
		}
		specs = append(specs, clusterSpecs...)
	}
	environments, namespaces := map[string]bool{}, map[string]bool{}
	for _, raw := range specs {
		var spec autoapm.Spec
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return nil, fmt.Errorf("read saved auto APM settings: %w", err)
		}
		environments[spec.Environment] = true
		if spec.Kubernetes != nil {
			// Kubernetes uses its own namespace inventory, not host service suggestions.
			continue
		}
		for _, target := range spec.Targets {
			environments[target.Environment] = true
			namespaces[target.ServiceNamespace] = true
		}
	}
	values := func(set map[string]bool) []string {
		out := make([]string, 0, len(set))
		for value := range set {
			if value != "" {
				out = append(out, value)
			}
		}
		sort.Strings(out)
		return out
	}
	return &AutoAPMOptions{Environments: values(environments), Namespaces: values(namespaces)}, nil
}
