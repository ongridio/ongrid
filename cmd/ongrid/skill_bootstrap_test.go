package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadBootstrapRegistriesSkillRoots(t *testing.T) {
	fixtures, err := filepath.Abs("../../internal/manager/biz/aiops/runtime/v3/chatruntime/testdata")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := filepath.Abs("../../agents")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	if err := os.CopyFS("skills", os.DirFS(filepath.Join(fixtures, "skill_parser", "minimal"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONGRID_BUILTIN_AGENTS_ROOT", agents)

	for _, tc := range []struct {
		name        string
		extraRoot   string
		marketplace string
		want        []string
	}{
		{"no packs", "", "missing", nil},
		{"marketplace only", "", filepath.Join(fixtures, "skill_registry", "multi", "always_skill"), []string{"always_skill"}},
		{"explicit extra root", "skills", filepath.Join(fixtures, "skill_registry", "multi", "always_skill"), []string{"always_skill", "hello_world"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ONGRID_BUILTIN_SKILLS_ROOT", tc.extraRoot)
			t.Setenv("ONGRID_SKILLS_ROOT", tc.marketplace)
			skills, personas := loadBootstrapRegistries(slog.Default())
			var names []string
			for _, skill := range skills.All() {
				names = append(names, skill.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.want) {
				t.Fatalf("loaded skills = %v, want %v", names, tc.want)
			}
			if len(personas.All()) == 0 {
				t.Fatal("agent personas were not loaded")
			}
		})
	}
}
