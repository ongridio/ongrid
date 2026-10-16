package skill

import (
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/runtime/v3/chatruntime"
	managerbizskill "github.com/ongridio/ongrid/internal/manager/biz/skill"
	skillcore "github.com/ongridio/ongrid/internal/skill"
)

// RuntimeCatalog projects SKILL.md metadata into the Manager skill inventory.
func RuntimeCatalog(registry *chatruntime.SkillRegistry) func() []managerbizskill.SkillSummary {
	return func() []managerbizskill.SkillSummary {
		if registry == nil {
			return nil
		}
		var out []managerbizskill.SkillSummary
		for _, sk := range registry.All() {
			if sk == nil || sk.Name == "" {
				continue
			}
			scope := skillcore.ScopeManager
			if sk.Metadata.Ongrid.Scope == string(skillcore.ScopeHost) {
				scope = skillcore.ScopeHost
			}
			source := sk.Provenance.Source
			if source == "" {
				source = "builtin"
			}
			out = append(out, managerbizskill.SkillSummary{
				Key:           sk.Name,
				Name:          sk.Name,
				Description:   sk.Description,
				Class:         skillcore.ClassSafe,
				Scope:         scope,
				Category:      "skill",
				Source:        source,
				InventoryOnly: true,
			})
		}
		return out

	}
}
