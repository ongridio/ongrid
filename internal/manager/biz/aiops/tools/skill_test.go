package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	skillsvc "github.com/ongridio/ongrid/internal/manager/biz/skill"
	skillcore "github.com/ongridio/ongrid/internal/skill"
	_ "github.com/ongridio/ongrid/internal/skill/builtin"
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

func TestHostSkillSchemaPreservesRequiredParameters(t *testing.T) {
	for _, executor := range skillcore.AllByClass(skillcore.ClassSafe) {
		if executor.Metadata().EffectiveScope() != skillcore.ScopeHost {
			continue
		}
		t.Run(executor.Metadata().Key, func(t *testing.T) {
			original, err := skillcore.BuildSchema(executor)
			if err != nil {
				t.Fatal(err)
			}
			adapted, err := buildSkillToolSchema(executor)
			if err != nil {
				t.Fatal(err)
			}
			var before, after struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(original, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(adapted, &after); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, field := range after.Required {
				counts[field]++
			}
			for _, field := range append(before.Required, "device_id") {
				if counts[field] != 1 {
					t.Errorf("required %s count=%d, schema=%s", field, counts[field], adapted)
				}
			}
		})
	}
}

// fakeSkillRunner records what the bridge dispatched. We use it to
// verify the bridge:
//   - strips edge_id from manager-scope args (ScopeManager skills)
//   - keeps params verbatim
//   - returns the runner's result envelope
type fakeSkillRunner struct {
	mu     sync.Mutex
	gotIn  skillsvc.ExecuteInput
	result json.RawMessage
	errStr string
}

func (f *fakeSkillRunner) Execute(_ context.Context, _ skillsvc.Caller, in skillsvc.ExecuteInput) (*skillsvc.ExecuteOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotIn = in
	return &skillsvc.ExecuteOutput{Result: f.result, Error: f.errStr}, nil
}

// stubManagerSkill is a minimal Executor that the test registers in a
// fresh registry, then we build its BaseTool through the registry.
// We hijack the global registry via a sentinel key + cleanup.
type stubManagerSkill struct {
	key string
}

func (s stubManagerSkill) Metadata() skillcore.Metadata {
	return skillcore.Metadata{
		Key:         s.key,
		Name:        s.key,
		Description: "stub for bridge test",
		Class:       skillcore.ClassSafe,
		Scope:       skillcore.ScopeManager,
		Params: skillcore.ParamSchema{
			{Name: "query", Param: skillcore.Param{Type: "string", Required: true, Desc: "q"}},
		},
		ResultPreview: "{ok}",
	}
}

func (stubManagerSkill) Execute(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{"ok":true}`), nil
}

func TestBridge_ManagerScopeSkill_NoEdgeIDRequired(t *testing.T) {
	const key = "stub_bridge_manager_scope"
	skillcore.Register(stubManagerSkill{key: key})
	defer unregisterSkillForTest(key)

	r := &Registry{log: slog.Default()}
	runner := &fakeSkillRunner{result: json.RawMessage(`{"ok":true}`)}
	r.SetSkillRunner(runner)

	tool := baseToolByName(t, r.BuildBaseTools(), key)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Schema should NOT contain edge_id for ScopeManager.
	if strings.Contains(string(info.Parameters), `"edge_id"`) {
		t.Errorf("manager-scope tool schema should not include edge_id; got %s", string(info.Parameters))
	}
	// Required list (if present) must not include edge_id either.
	var schemaMap map[string]any
	if err := json.Unmarshal(info.Parameters, &schemaMap); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if req, ok := schemaMap["required"].([]any); ok {
		for _, n := range req {
			if n == "edge_id" {
				t.Errorf("required list still has edge_id")
			}
		}
	}

	// Invoke the tool and verify the runner saw the params verbatim
	// (no edge_id) with EdgeID==0.
	res, err := tool.InvokableRun(context.Background(), `{"query":"hi"}`)
	if err != nil {
		t.Fatalf("tool.InvokableRun: %v", err)
	}
	if runner.gotIn.EdgeID != 0 {
		t.Errorf("EdgeID forwarded to runner = %d, want 0 for manager scope", runner.gotIn.EdgeID)
	}
	if got := string(runner.gotIn.Params); !strings.Contains(got, `"query":"hi"`) {
		t.Errorf("params not forwarded: %s", got)
	}
	if !strings.Contains(res, `"result"`) {
		t.Errorf("ResultJSON envelope unexpected: %s", res)
	}
}

func TestBridge_ManagerScope_StripsAccidentalEdgeID(t *testing.T) {
	const key = "stub_bridge_manager_strip"
	skillcore.Register(stubManagerSkill{key: key})
	defer unregisterSkillForTest(key)

	r := &Registry{log: slog.Default()}
	runner := &fakeSkillRunner{result: json.RawMessage(`{}`)}
	r.SetSkillRunner(runner)

	tool := baseToolByName(t, r.BuildBaseTools(), key)
	// LLM mistakenly sends edge_id even though it's not in the schema.
	if _, err := tool.InvokableRun(context.Background(), `{"query":"hi","edge_id":99}`); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if runner.gotIn.EdgeID != 0 {
		t.Errorf("manager-scope skill must ignore stray edge_id; runner saw %d", runner.gotIn.EdgeID)
	}
	if strings.Contains(string(runner.gotIn.Params), "edge_id") {
		t.Errorf("edge_id should be stripped from params; got %s", string(runner.gotIn.Params))
	}
}

// unregisterSkillForTest is a tiny escape hatch — the production
// registry doesn't expose Unregister but tests need to clean up.
func unregisterSkillForTest(key string) {
	// We can't reach into skillcore.globalRegistry from here (different
	// package) without an exported helper, and we don't want to ship
	// one for production. Instead, the test relies on each subtest
	// using a unique key — the duplicate-key panic would only fire if
	// two tests in the same process used the same key. Leave this as
	// a no-op; the registered skill stays in the global registry for
	// the lifetime of the test binary, which is fine.
	_ = key
}
