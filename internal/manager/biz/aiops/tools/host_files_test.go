package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// fakeHostFilesResolver is a stub hostFilesDeviceResolver. Tests
// preload either a deviceID→edgeID map (happy path) or an err to
// exercise the failure branches.
type fakeHostFilesResolver struct {
	mapping map[uint64]uint64
	err     error
}

func (f *fakeHostFilesResolver) LookupHostEdge(_ context.Context, deviceID uint64) (uint64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if f.mapping == nil {
		return 0, nil
	}
	return f.mapping[deviceID], nil
}

// newHostFilesToolsFor builds the three host_files BaseTools backed by
// a fake caller + fake resolver. Returns the (caller, find, du, stat)
// quad so each test can pick what it needs without re-doing wiring.
func newHostFilesToolsFor(t *testing.T, resolver hostFilesDeviceResolver, fc *fakeCaller) (*FindLargeFilesTool, *DuSummaryTool, *StatFileTool) {
	t.Helper()
	find := &FindLargeFilesTool{caller: fc, resolver: resolver}
	du := &DuSummaryTool{caller: fc, resolver: resolver}
	stat := &StatFileTool{caller: fc, resolver: resolver}
	return find, du, stat
}

// =====================================================================
// find_large_files
// =====================================================================

func TestFindLargeFilesTool_Info(t *testing.T) {
	tool, _, _ := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameFindLargeFiles {
		t.Errorf("Name = %q, want %q", info.Name, ToolNameFindLargeFiles)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q, want read", info.Class)
	}
	if info.Description == "" {
		t.Errorf("Description empty")
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty — requires it separated from Description")
	}
	if !strings.Contains(info.WhenToUse, "batch") && !strings.Contains(info.WhenToUse, "multiple") {
		t.Errorf("WhenToUse should nudge batch usage: %q", info.WhenToUse)
	}
	var schema map[string]any
	if err := json.Unmarshal(info.Parameters, &schema); err != nil {
		t.Fatalf("Parameters not valid JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("schema missing properties")
	}
	pathsProp, _ := props["paths"].(map[string]any)
	if pathsProp == nil {
		t.Fatalf("schema missing paths property — must be array now")
	}
	if pathsProp["type"] != "array" {
		t.Errorf("paths.type = %v, want array", pathsProp["type"])
	}
	if pathsProp["maxItems"].(float64) != 16 {
		t.Errorf("paths.maxItems = %v, want 16", pathsProp["maxItems"])
	}
	if pathsProp["minItems"].(float64) != 1 {
		t.Errorf("paths.minItems = %v, want 1", pathsProp["minItems"])
	}
}

func TestFindLargeFilesTool_BatchRoundTrip(t *testing.T) {
	mtime := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.FindLargeFilesResponse{
			Results: []tunnel.FindLargeFilesResultEntry{
				{
					Path:        "/var/log",
					ScannedPath: "/var/log",
					Files: []tunnel.HostFileInfo{
						{Path: "/var/log/messages.1.gz", SizeBytes: 524288000, SizeHuman: "500 MiB", Mtime: mtime, Owner: "root"},
					},
				},
				{Path: "/var/cache", Error: "sandbox: not in allow-list"},
			},
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var/log","/var/cache"],"top_n":5}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 7 {
		t.Errorf("caller invoked with edge id %d, want 7", fc.lastID)
	}
	if fc.lastName != tunnel.MethodFindLargeFiles {
		t.Errorf("method = %q, want %q", fc.lastName, tunnel.MethodFindLargeFiles)
	}
	var sentReq tunnel.FindLargeFilesRequest
	if err := json.Unmarshal(fc.lastBody, &sentReq); err != nil {
		t.Fatalf("decode lastBody: %v", err)
	}
	if len(sentReq.Paths) != 2 || sentReq.Paths[0] != "/var/log" || sentReq.Paths[1] != "/var/cache" {
		t.Errorf("sent paths = %v, want [/var/log /var/cache]", sentReq.Paths)
	}
	if sentReq.TopN != 5 {
		t.Errorf("sent top_n = %d, want 5", sentReq.TopN)
	}
	if sentReq.MinSizeBytes != (1 << 20) {
		t.Errorf("sent min_size_bytes = %d, want default 1 MiB", sentReq.MinSizeBytes)
	}
	if len(sentReq.ExcludePaths) == 0 {
		t.Errorf("exclude_paths should default to virtual fs")
	}

	var env findLargeFilesResultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if env.DeviceID != 1 {
		t.Errorf("env.DeviceID = %d, want 1", env.DeviceID)
	}
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 2 {
		t.Fatalf("Results len = %d, want 2", len(env.Results))
	}
	if env.Results[0].Error != "" || env.Results[1].Error == "" {
		t.Errorf("partial-success layout corrupted: %+v", env.Results)
	}
	if env.Results[0].ScannedPath != "/var/log" || len(env.Results[0].Files) != 1 {
		t.Errorf("entry 0 unexpected: %+v", env.Results[0])
	}
}

func TestFindLargeFilesTool_Defaults(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.FindLargeFilesResponse{Results: []tunnel.FindLargeFilesResultEntry{{Path: "/", ScannedPath: "/"}}}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, fc)

	if _, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/"]}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var sentReq tunnel.FindLargeFilesRequest
	if err := json.Unmarshal(fc.lastBody, &sentReq); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sentReq.TopN != 20 {
		t.Errorf("TopN default = %d, want 20", sentReq.TopN)
	}
	if sentReq.MinSizeBytes != (1 << 20) {
		t.Errorf("MinSize default = %d, want 1 MiB", sentReq.MinSizeBytes)
	}
}

func TestFindLargeFilesTool_TopNClamp(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.FindLargeFilesResponse{Results: []tunnel.FindLargeFilesResultEntry{{Path: "/"}}}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, fc)

	if _, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/"],"top_n":9999}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var sentReq tunnel.FindLargeFilesRequest
	_ = json.Unmarshal(fc.lastBody, &sentReq)
	if sentReq.TopN != 100 {
		t.Errorf("TopN clamp = %d, want 100", sentReq.TopN)
	}
}

func TestFindLargeFilesTool_MissingDeviceID(t *testing.T) {
	tool, _, _ := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"paths":["/"]}`)
	if err == nil {
		t.Fatalf("expected error for missing device_id")
	}
	if !strings.Contains(err.Error(), ToolNameFindLargeFiles) {
		t.Errorf("error should include tool name prefix, got: %v", err)
	}
	if !strings.Contains(err.Error(), "device_id") {
		t.Errorf("error should mention device_id, got: %v", err)
	}
}

func TestFindLargeFilesTool_MissingPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":1}`)
	if err == nil {
		t.Fatalf("expected error for missing paths")
	}
	if !strings.Contains(err.Error(), "paths") {
		t.Errorf("error should mention paths: %v", err)
	}
}

func TestFindLargeFilesTool_TooManyPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	// Build args with 17 paths.
	paths := make([]string, hostFilesMaxBatchPaths+1)
	for i := range paths {
		paths[i] = "/var"
	}
	args, _ := json.Marshal(map[string]any{"device_id": 1, "paths": paths})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil {
		t.Fatalf("expected error for too many paths")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("error should mention too many: %v", err)
	}
}

func TestFindLargeFilesTool_EmptyPathString(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var",""]}`)
	if err == nil {
		t.Fatalf("expected error for empty path string in paths")
	}
}

func TestFindLargeFilesTool_UnlinkedDevice(t *testing.T) {
	tool, _, _ := newHostFilesToolsFor(t, &fakeHostFilesResolver{mapping: map[uint64]uint64{}}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":42,"paths":["/var"]}`)
	if err == nil {
		t.Fatalf("expected error for unlinked device_id")
	}
	if !strings.Contains(err.Error(), "no host-edge link") {
		t.Errorf("error should mention missing junction: %v", err)
	}
}

func TestFindLargeFilesTool_DispatchError(t *testing.T) {
	fc := &fakeCaller{respErr: errs.ErrEdgeOffline}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	tool, _, _ := newHostFilesToolsFor(t, resolver, fc)

	_, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var"]}`)
	if err == nil {
		t.Fatalf("expected dispatch error")
	}
	if !errors.Is(err, errs.ErrEdgeOffline) {
		t.Errorf("error should wrap ErrEdgeOffline: %v", err)
	}
}

func TestFindLargeFilesTool_NilCaller(t *testing.T) {
	tool := &FindLargeFilesTool{caller: nil, resolver: &fakeHostFilesResolver{}}
	_, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var"]}`)
	if err == nil || !strings.Contains(err.Error(), "caller") {
		t.Errorf("expected caller-not-configured error, got %v", err)
	}
}

// =====================================================================
// du_summary
// =====================================================================

func TestDuSummaryTool_Info(t *testing.T) {
	_, tool, _ := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameDuSummary {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q", info.Class)
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty")
	}
	if !strings.Contains(info.WhenToUse, "depth") {
		t.Errorf("WhenToUse should explain depth: %q", info.WhenToUse)
	}
	if !strings.Contains(info.WhenToUse, "anti-pattern") && !strings.Contains(info.WhenToUse, "multiple") {
		t.Errorf("WhenToUse should warn against single-path: %q", info.WhenToUse)
	}
	var schema map[string]any
	_ = json.Unmarshal(info.Parameters, &schema)
	props, _ := schema["properties"].(map[string]any)
	pathsProp, _ := props["paths"].(map[string]any)
	if pathsProp == nil || pathsProp["type"] != "array" {
		t.Errorf("paths must be array in schema: %+v", pathsProp)
	}
}

func TestDuSummaryTool_BatchRoundTrip(t *testing.T) {
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.DuSummaryResponse{
			Results: []tunnel.DuSummaryResultEntry{
				{
					Path:           "/var",
					Subpaths:       []tunnel.HostDuEntry{{Subpath: "/var/log", SizeBytes: 1073741824, SizeHuman: "1 GiB"}},
					TotalSizeBytes: 1073741824,
					TotalSizeHuman: "1 GiB",
				},
				{Path: "/opt", Error: "du exited 1"},
				{
					Path:           "/home",
					Subpaths:       []tunnel.HostDuEntry{{Subpath: "/home/user", SizeBytes: 100000, SizeHuman: "97.7 KiB"}},
					TotalSizeBytes: 100000,
					TotalSizeHuman: "97.7 KiB",
				},
			},
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var","/opt","/home"],"depth":1}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 7 || fc.lastName != tunnel.MethodDuSummary {
		t.Errorf("dispatch wrong: id=%d method=%q", fc.lastID, fc.lastName)
	}
	var sentReq tunnel.DuSummaryRequest
	_ = json.Unmarshal(fc.lastBody, &sentReq)
	if len(sentReq.Paths) != 3 {
		t.Errorf("sent paths len = %d, want 3", len(sentReq.Paths))
	}
	if sentReq.Depth != 1 {
		t.Errorf("sent depth = %d, want 1", sentReq.Depth)
	}

	var env duSummaryResultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.DeviceID != 1 {
		t.Errorf("DeviceID = %d", env.DeviceID)
	}
	if env.SuccessCount != 2 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 2/1", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 3 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	if env.Results[0].Path != "/var" || env.Results[1].Path != "/opt" || env.Results[2].Path != "/home" {
		t.Errorf("order corrupted: %v", []string{env.Results[0].Path, env.Results[1].Path, env.Results[2].Path})
	}
	if env.Results[1].Error == "" {
		t.Errorf("entry 1 should carry error")
	}
}

func TestDuSummaryTool_DepthClamp(t *testing.T) {
	fc := &fakeCaller{respBody: mustMarshal(tunnel.DuSummaryResponse{Results: []tunnel.DuSummaryResultEntry{{Path: "/var"}}})}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, fc)

	if _, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var"],"depth":99}`); err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var sentReq tunnel.DuSummaryRequest
	_ = json.Unmarshal(fc.lastBody, &sentReq)
	if sentReq.Depth != 5 {
		t.Errorf("Depth clamp = %d, want 5", sentReq.Depth)
	}
}

func TestDuSummaryTool_MissingDeviceID(t *testing.T) {
	_, tool, _ := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"paths":["/var"]}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), ToolNameDuSummary) {
		t.Errorf("error should be tool-prefixed: %v", err)
	}
}

func TestDuSummaryTool_MissingPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":1}`)
	if err == nil {
		t.Fatalf("expected error for missing paths")
	}
	if !strings.Contains(err.Error(), "paths") {
		t.Errorf("error should mention paths: %v", err)
	}
}

func TestDuSummaryTool_TooManyPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	paths := make([]string, hostFilesMaxBatchPaths+1)
	for i := range paths {
		paths[i] = "/var"
	}
	args, _ := json.Marshal(map[string]any{"device_id": 1, "paths": paths})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-paths error, got %v", err)
	}
}

func TestDuSummaryTool_UnlinkedDevice(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":42,"paths":["/var"]}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "no host-edge link") {
		t.Errorf("error should mention missing junction: %v", err)
	}
}

func TestDuSummaryTool_DispatchError(t *testing.T) {
	fc := &fakeCaller{respErr: errs.ErrEdgeOffline}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, tool, _ := newHostFilesToolsFor(t, resolver, fc)

	_, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/var"]}`)
	if err == nil {
		t.Fatalf("expected dispatch error")
	}
	if !errors.Is(err, errs.ErrEdgeOffline) {
		t.Errorf("err should wrap ErrEdgeOffline: %v", err)
	}
}

// =====================================================================
// stat_file
// =====================================================================

func TestStatFileTool_Info(t *testing.T) {
	_, _, tool := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameStatFile {
		t.Errorf("Name = %q", info.Name)
	}
	if info.Class != "read" {
		t.Errorf("Class = %q", info.Class)
	}
	if info.WhenToUse == "" {
		t.Errorf("WhenToUse empty")
	}
	if !strings.Contains(info.WhenToUse, "anti-pattern") && !strings.Contains(info.WhenToUse, "multiple") {
		t.Errorf("WhenToUse should warn against single-path: %q", info.WhenToUse)
	}
}

func TestStatFileTool_BatchRoundTrip(t *testing.T) {
	mtime := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	fc := &fakeCaller{
		respBody: mustMarshal(tunnel.StatFileResponse{
			Results: []tunnel.StatFileResultEntry{
				{Path: "/etc/passwd", Type: "file", SizeBytes: 2745, SizeHuman: "2.7 KiB", Mtime: mtime, Atime: mtime, Mode: "0644", Owner: "root", Group: "root"},
				{Path: "/var/log/messages", Error: "no such file"},
			},
		}),
	}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, _, tool := newHostFilesToolsFor(t, resolver, fc)

	out, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/etc/passwd","/var/log/messages"]}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if fc.lastID != 7 || fc.lastName != tunnel.MethodStatFile {
		t.Errorf("dispatch wrong: id=%d method=%q", fc.lastID, fc.lastName)
	}
	var sentReq tunnel.StatFileRequest
	_ = json.Unmarshal(fc.lastBody, &sentReq)
	if len(sentReq.Paths) != 2 {
		t.Errorf("sent paths len = %d, want 2", len(sentReq.Paths))
	}

	var env statFileResultEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.DeviceID != 1 {
		t.Errorf("DeviceID = %d", env.DeviceID)
	}
	if env.SuccessCount != 1 || env.ErrorCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", env.SuccessCount, env.ErrorCount)
	}
	if len(env.Results) != 2 {
		t.Fatalf("Results len = %d", len(env.Results))
	}
	if env.Results[0].Mode != "0644" || env.Results[0].Owner != "root" {
		t.Errorf("entry 0 fields wrong: %+v", env.Results[0])
	}
	if env.Results[1].Error == "" {
		t.Errorf("entry 1 should carry error")
	}
}

func TestStatFileTool_MissingDeviceID(t *testing.T) {
	_, _, tool := newHostFilesToolsFor(t, &fakeHostFilesResolver{}, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"paths":["/etc/passwd"]}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), ToolNameStatFile) {
		t.Errorf("error should be tool-prefixed: %v", err)
	}
}

func TestStatFileTool_MissingPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, _, tool := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":1}`)
	if err == nil {
		t.Fatalf("expected error for missing paths")
	}
	if !strings.Contains(err.Error(), "paths") {
		t.Errorf("error should mention paths: %v", err)
	}
}

func TestStatFileTool_TooManyPaths(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, _, tool := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	paths := make([]string, hostFilesMaxBatchPaths+1)
	for i := range paths {
		paths[i] = "/var"
	}
	args, _ := json.Marshal(map[string]any{"device_id": 1, "paths": paths})
	_, err := tool.InvokableRun(context.Background(), string(args))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected too-many-paths error, got %v", err)
	}
}

func TestStatFileTool_UnlinkedDevice(t *testing.T) {
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{}}
	_, _, tool := newHostFilesToolsFor(t, resolver, &fakeCaller{})
	_, err := tool.InvokableRun(context.Background(), `{"device_id":42,"paths":["/etc/passwd"]}`)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "no host-edge link") {
		t.Errorf("error should mention missing junction: %v", err)
	}
}

func TestStatFileTool_DispatchError(t *testing.T) {
	fc := &fakeCaller{respErr: errs.ErrEdgeOffline}
	resolver := &fakeHostFilesResolver{mapping: map[uint64]uint64{1: 7}}
	_, _, tool := newHostFilesToolsFor(t, resolver, fc)

	_, err := tool.InvokableRun(context.Background(), `{"device_id":1,"paths":["/etc/passwd"]}`)
	if err == nil {
		t.Fatalf("expected dispatch error")
	}
	if !errors.Is(err, errs.ErrEdgeOffline) {
		t.Errorf("err should wrap ErrEdgeOffline: %v", err)
	}
}

// =====================================================================
// AppendHostFilesTools
// =====================================================================

func TestAppendHostFilesTools_NilDepsReturnsUnchanged(t *testing.T) {
	// nil bag — early return.
	got := AppendHostFilesTools(nil, nil, nil, nil, nil)
	if got != nil {
		t.Errorf("expected nil bag to return nil, got %v", got)
	}
	// non-nil bag, nil deps — bag unchanged (no host_files appended).
	bag := NewToolBag(nil, 30)
	got = AppendHostFilesTools(bag, nil, nil, nil, nil)
	if got != bag {
		t.Errorf("expected same bag back, got different ref")
	}
	if n := len(bag.AllTools()); n != 0 {
		t.Errorf("expected 0 tools after nil-deps append, got %d", n)
	}
}
