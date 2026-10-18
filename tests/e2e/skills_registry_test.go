//go:build e2e

// Catalog: L1 — 加载内置 skill registry。证明 GET /v1/skills 在一台干净
// 启动的 manager 上回出非空清单:
//
//   - 内置 Go skills 在 internal/skill/builtin 的 init() 里登记
//     (host_tail_file / host_netns_inspect / host_probe_dns /
//     host_probe_http / host_probe_tcp / host_read_journal /
//     host_restart_service / web_search),manager 一进程就有这 8 条。
//   - inventory_bridge.RegisterBaseToolsAsSkills 把 AIOps ToolBag 里的
//     ~18 个 BaseTool(query_promql / query_logql / query_traceql /
//     host_bash / get_host_summary / ...)也桥接进同一个 registry,所以
//     线上数量 = 8 + bridged ≈ 20+ 条。
//   - testenv 不加载额外的 SKILL.md；内置工具目录由 Go 注册提供。
//
// 我们用 >= 10 这个阈值,既贴近 catalog "~18" 的预期,又留余量,
// 哪怕未来某次重命名 / 删一个 BaseTool 也不会撞红。
//
// 路由:GET /api/v1/skills (skillHandler.Register: r.Get("/v1/skills",
// h.list))。需要 JWT (callerFromRequest -> tenantctx),用 admin token。
// list handler 对所有已登录角色一视同仁返全集,不按 role 过滤 — 过滤
// 在 Execute 时按 Class 走。
package e2e

import (
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestSkills_RegistryListed_L1(t *testing.T) {
	env := testenv.Start(t, testenv.WithEnv("ONGRID_SKILLS_ROOT", t.TempDir()))
	pair := env.LoginAdmin()

	// ─── unauth: bearer-less GET must 401 ─────────────────────────────────
	// callerFromRequest pulls tenantctx; without a JWT the middleware
	// short-circuits before list ever runs.
	status, _, err := env.DoJSON("GET", "/api/v1/skills", nil, "")
	if err != nil {
		t.Fatalf("GET /v1/skills (no token): transport: %v", err)
	}
	if status != 401 {
		t.Fatalf("GET /v1/skills without bearer: status=%d want 401", status)
	}

	// ─── authed list: admin pulls full registry ───────────────────────────
	status, body, err := env.DoJSON("GET", "/api/v1/skills", nil, pair.AccessToken)
	if err != nil {
		t.Fatalf("GET /v1/skills: transport: %v", err)
	}
	if status != 200 {
		t.Fatalf("GET /v1/skills: status=%d body=%v", status, body)
	}

	itemsAny, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("GET /v1/skills: response missing items[] (body=%v)", body)
	}
	// total is len(items) by construction in the handler; cross-check
	// just so a future shape change (paginated total != len(items))
	// surfaces here rather than as a silent drift.
	if total, ok := body["total"].(float64); ok {
		if int(total) != len(itemsAny) {
			t.Fatalf("GET /v1/skills: total=%d but items has %d (body=%v)",
				int(total), len(itemsAny), body)
		}
	}

	const minSkills = 10
	if len(itemsAny) < minSkills {
		t.Fatalf("GET /v1/skills: got %d skills, want >= %d (catalog L1 says ~18). items=%v",
			len(itemsAny), minSkills, itemsAny)
	}

	// ─── spot-check: at least 2 known builtin keys present ────────────────
	// These are the most-stable ones — host_probe_* live in
	// internal/skill/builtin/*.go with hardcoded init() Register; web_search
	// is the canonical ScopeManager builtin; host_restart_service has its
	// own subpackage. Even if BaseTool inventory_bridge churns we expect
	// these to keep showing up.
	wantAny := []string{
		"host_probe_http",
		"host_probe_tcp",
		"host_probe_dns",
		"host_read_journal",
		"host_tail_file",
		"host_restart_service",
		"web_search",
		"query_promql", // bridged from BaseTool
	}
	seen := map[string]map[string]any{}
	for _, raw := range itemsAny {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("GET /v1/skills: item not an object: %v", raw)
		}
		key, _ := item["key"].(string)
		if key == "" {
			t.Fatalf("GET /v1/skills: item missing 'key' field: %v", item)
		}
		seen[key] = item
	}
	for _, key := range []string{"host_bash", "host_find_large_files", "host_du_summary", "host_stat_file", "host_restart_service"} {
		if _, ok := seen[key]; !ok {
			t.Errorf("builtin tool %q missing from catalog without SKILL.md prompts", key)
		}
	}
	matched := 0
	matchedKeys := []string{}
	for _, k := range wantAny {
		if _, ok := seen[k]; ok {
			matched++
			matchedKeys = append(matchedKeys, k)
		}
	}
	if matched < 2 {
		t.Fatalf("GET /v1/skills: expected at least 2 of %v in the registry, matched %d (%v); have keys=%v",
			wantAny, matched, matchedKeys, keysOf(seen))
	}

	// ─── shape-check: pick one matched item and verify the DTO fields ────
	// SkillSummary in biz/skill/service.go promises (at minimum) key +
	// description + class. Params is `[]SkillParamDef` and gets emitted
	// even when empty (no `omitempty`), so an absent params is also a red
	// flag.
	probeKey := matchedKeys[0]
	probe := seen[probeKey]
	if desc, _ := probe["description"].(string); desc == "" {
		t.Fatalf("skill %q has empty description (item=%v)", probeKey, probe)
	}
	if class, _ := probe["class"].(string); class == "" {
		t.Fatalf("skill %q missing 'class' (item=%v)", probeKey, probe)
	}
	if _, ok := probe["params"]; !ok {
		t.Fatalf("skill %q missing 'params' key (item=%v)", probeKey, probe)
	}
}

// keysOf flattens a seen-map into a slice for diagnostics. Inline so the
// test stays single-file.
func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
