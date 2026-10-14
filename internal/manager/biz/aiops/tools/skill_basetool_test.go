package tools

import (
	"context"
	"encoding/json"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	skillsvc "github.com/ongridio/ongrid/internal/manager/biz/skill"
	skillcore "github.com/ongridio/ongrid/internal/skill"
	_ "github.com/ongridio/ongrid/internal/skill/builtin"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestGraphCatalogIncludesAndExecutesSafeSkills(t *testing.T) {
	runner := &fakeSkillRunner{result: json.RawMessage(`{"ok":true}`)}
	r := NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default())
	r.SetSkillRunner(runner)
	bag := r.BuildBaseTools()
	names := map[string]basetool.BaseTool{}
	for _, tool := range bag.AllTools() {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if names[info.Name] != nil {
			t.Fatalf("duplicate tool %s", info.Name)
		}
		names[info.Name] = tool
	}
	for _, key := range []string{"web_search", "host_probe_dns", "host_probe_tcp", "host_probe_http", "host_probe_ping", "host_netns_inspect", "host_read_journal", "host_tail_file"} {
		if names[key] == nil {
			t.Errorf("missing executable %s", key)
		}
	}
	tool := names["web_search"]
	if tool == nil {
		t.Fatal("missing search")
	}
	if _, err := tool.InvokableRun(context.Background(), `{"query":"test"}`); err == nil {
		t.Fatal("search must fail closed")
	}
	ctx := basetool.WithWebSearchAllowed(context.Background(), true)
	if _, err := tool.InvokableRun(ctx, `{"query":"test","device_id":42}`); err != nil {
		t.Fatal(err)
	}
	if runner.gotIn.Key != "web_search" || runner.gotIn.EdgeID != 0 || string(runner.gotIn.Params) != `{"query":"test"}` {
		t.Fatalf("wrong dispatch: %+v", runner.gotIn)
	}
	if names["capture_pcap"] != nil {
		t.Fatal("mutating skill exposed as safe")
	}
}

func TestGraphCatalogDoesNotReimportInventoryExports(t *testing.T) {
	const key = "test_catalog_export"
	skillcore.Register(&baseToolSkillExecutor{meta: skillcore.Metadata{Key: key, Name: key, Description: "inventory", Class: skillcore.ClassSafe, Scope: skillcore.ScopeManager}})
	t.Cleanup(func() { unregisterSkillForTest(key) })
	r := NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default())
	r.SetSkillRunner(&fakeSkillRunner{})
	for _, tool := range r.BuildBaseTools().AllTools() {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == key {
			t.Fatal("inventory export imported as executable skill")
		}
	}
}

func TestGraphCatalogAdaptsExternalSubprocessSkill(t *testing.T) {
	const key = "test_catalog_subprocess"
	// 使用真实 manifest loader 校验加载到调用之间的链路。
	dir := t.TempDir()
	writeCatalogFixture(t, dir)
	n, err := skillcore.LoadDirs(skillcore.LoaderConfig{Dirs: []string{dir}})
	if err != nil || n != 1 {
		t.Fatalf("load: %d %v", n, err)
	}
	t.Cleanup(func() { unregisterSkillForTest(key) })
	runner := skillsvc.New(nil, nil, slog.Default())
	r := NewRegistry(nil, nil, nil, nil, nil, nil, nil, slog.Default())
	r.SetSkillRunner(runner)
	for _, tool := range r.BuildBaseTools().AllTools() {
		info, err := tool.Info(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Name != key {
			continue
		}
		if info.Origin != basetool.OriginSkill {
			t.Fatal("missing dynamic origin")
		}
		result, err := tool.InvokableRun(context.Background(), `{"query":"external"}`)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Result struct {
				OK bool `json:"ok"`
			} `json:"result"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(result), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Result.OK || envelope.Error != "" {
			t.Fatalf("subprocess execution failed: %s", result)
		}
		return
	}
	t.Fatal("external skill missing")
}

func writeCatalogFixture(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\nprintf '{\"ok\":true}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.json"), []byte(`{"name":"test_catalog_subprocess","description":"test external tool","entry":"run.sh","class":"safe","schema":{"type":"object","properties":{"query":{"type":"string"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
}
