package tools

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
)

func TestBuildBaseTools_NilGatingMatchesNewRegistry(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())

	// Minimal — only edges + caller. NewRegistry registers exactly:
	// get_host_load, get_process_list, query_devices, get_topology,
	// get_host_summary → 5 tools.
	regMin := NewRegistry(&fakeCaller{}, uc, nil, nil, nil, nil, nil, slog.Default())
	closureNames := schemaNames(regMin.Schemas())

	bag := regMin.BuildBaseTools()
	baseNames := toolInfoNames(t, bag.AllTools())

	for _, n := range closureNames {
		if !containsName(baseNames, n) {
			t.Errorf("BuildBaseTools is missing closure-side tool %q (closure=%v base=%v)", n, closureNames, baseNames)
		}
	}
}

func TestBuildBaseTools_HostRankingDoesNotRequireEdge(t *testing.T) {
	devices := devicebiz.NewUsecase(nil, nil, slog.Default())
	reg := NewRegistry(nil, nil, devices, &fakePromQuerier{}, nil, nil, nil, slog.Default())
	for _, names := range [][]string{schemaNames(reg.Schemas()), toolInfoNames(t, reg.BuildBaseTools().AllTools())} {
		if countToolName(names, ToolNameRankHosts) != 1 {
			t.Fatalf("host ranking missing without an Edge dependency: %v", names)
		}
	}
}

func TestBuildBaseTools_FullSetWithAllDeps(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	pq := &fakePromQuerier{}
	lq := &fakeLogQuerier{}
	tq := &fakeTraceQuerier{}
	au := &fakeAlertUC{}

	reg := NewRegistry(&fakeCaller{}, uc, nil, pq, lq, tq, au, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{})
	closureNames := schemaNames(reg.Schemas())

	bag := reg.BuildBaseTools()
	// Filter out ToolSearch — it's registered unconditionally by
	// BuildBaseTools as an extras-slot tool and has no
	// counterpart in the closure-style Schemas() output.
	baseNames := filterOutTool(toolInfoNames(t, bag.AllTools()), ToolSearchToolName)

	for _, n := range closureNames {
		if !containsName(baseNames, n) {
			t.Errorf("BuildBaseTools missing %q (closure=%v base=%v)", n, closureNames, baseNames)
		}
	}
	for _, n := range baseNames {
		if !containsName(closureNames, n) {
			t.Errorf("BuildBaseTools has extra %q not in closure path (base=%v closure=%v)", n, baseNames, closureNames)
		}
	}
	if containsName(baseNames, "search_logs") || containsName(closureNames, "search_logs") {
		t.Fatalf("legacy search_logs tool is still exposed (base=%v closure=%v)", baseNames, closureNames)
	}
	if countToolName(baseNames, ToolNameQueryLogQL) != 1 || countToolName(closureNames, ToolNameQueryLogQL) != 1 {
		t.Fatalf("query_logql must be the single log-query tool (base=%v closure=%v)", baseNames, closureNames)
	}
}

// TestBuildBaseTools_GraphKernelToolBagCount mirrors the production
// wiring path that ONGRID_AGENT_KERNEL=graph follows in main.go:
//
//  1. NewRegistry with all four signal sources wired (mirrors a
//     fully-configured Prom + Loki + Tempo + alert deployment).
//  2. BuildBaseTools to materialise the BaseTool slice.
//  3. AppendHostFilesTools to add find_large_files / du_summary /
//     stat_file (PR-8). devices is required for host_files; we
//     construct a minimal usecase with nil repo so the helper's
//     non-nil check passes.
//
// The point of the test is twofold: it gates regression on the
// total tool count when adding/removing tools AND it gives the
// PR-9 reviewer a visible Logf line so they can see exactly what
// the LLM is exposed to in production.
func TestBuildBaseTools_GraphKernelToolBagCount(t *testing.T) {
	uc := edgebiz.NewUsecase(newFakeEdgeRepo(), nil, nil, slog.Default())
	dc := &fakeDevicesForToolBag{}
	pq := &fakePromQuerier{}
	lq := &fakeLogQuerier{}
	tq := &fakeTraceQuerier{}
	au := &fakeAlertUC{}

	reg := NewRegistry(&fakeCaller{}, uc, dc.usecase(), pq, lq, tq, au, slog.Default())
	reg.SetPluginConfigLister(fakePluginConfigLister{})
	bag := reg.BuildBaseTools()
	bag = AppendHostFilesTools(bag, &fakeCaller{}, uc, dc.usecase(), slog.Default())

	names := toolInfoNames(t, bag.AllTools())
	t.Logf("graph kernel toolBag (count=%d): %v", len(names), names)

	// Sanity: host_files trio MUST be present once AppendHostFilesTools
	// has been called with non-nil deps.
	for _, want := range []string{ToolNameFindLargeFiles, ToolNameDuSummary, ToolNameStatFile} {
		if !containsName(names, want) {
			t.Errorf("toolBag missing %q after AppendHostFilesTools", want)
		}
	}
}

// fakeDevicesForToolBag stands in for a *devicebiz.Usecase so
// AppendHostFilesTools' non-nil check passes. The host_files tool
// only needs a non-nil reference at construction; the actual
// resolver path is exercised by host_files_basetool_test.go.
type fakeDevicesForToolBag struct{}

func (fakeDevicesForToolBag) usecase() *devicebiz.Usecase {
	// devicebiz.NewUsecase accepts (Repo, EdgeDeviceRepo, Logger).
	// Passing nil triplet is fine for AppendHostFilesTools'
	// non-nil-pointer check; the resolver will return 0 for any
	// lookup but we never invoke a tool here.
	return devicebiz.NewUsecase(nil, nil, slog.Default())
}

func toolInfoNames(t *testing.T, tools []basetool.BaseTool) []string {
	t.Helper()
	out := make([]string, 0, len(tools))
	for _, x := range tools {
		info, err := x.Info(context.Background())
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		out = append(out, info.Name)
	}
	return out
}

// filterOutTool removes a single name from the slice, preserving order.
// Used by tests that compare BuildBaseTools' output against the closure
// path — the new ToolSearch entry has no closure-side counterpart so we
// strip it before the diff.
func filterOutTool(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func countToolName(names []string, want string) int {
	count := 0
	for _, name := range names {
		if name == want {
			count++
		}
	}
	return count
}
