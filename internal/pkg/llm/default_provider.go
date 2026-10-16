package llm

import (
	"sort"
	"strings"
)

// PickProviderDefault mirrors MultiClient's catalog default:
// use the configured default when it names an available provider, otherwise
// pick the first configured provider by stable provider id. Background graph
// workers, including reports, rely on this to match /v1/aiops/models.
func PickProviderDefault(providers []ProviderConfig, preferred string) (string, string) {
	preferred = strings.TrimSpace(preferred)
	available := make([]ProviderConfig, 0, len(providers))
	for _, p := range providers {
		if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.APIKey) == "" {
			continue
		}
		available = append(available, p)
	}
	if preferred != "" {
		for _, p := range available {
			if p.ID == preferred {
				return p.ID, p.Model
			}
		}
	}
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	if len(available) > 0 {
		return available[0].ID, available[0].Model
	}
	return "", ""
}
