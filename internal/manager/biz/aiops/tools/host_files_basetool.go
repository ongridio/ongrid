package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	devicebiz "github.com/ongridio/ongrid/internal/manager/biz/device"
	edgebiz "github.com/ongridio/ongrid/internal/manager/biz/edge"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// host_files_basetool.go is PR-8 of the manager-side BaseTool
// implementations of the three edge-scope filesystem inspection tools
// declared in skills/host-files/SKILL.md. Each tool unmarshals the LLM
// argsJSON, resolves device_id → host edge_id via the edge_devices
// junction (devicebiz.EdgeDeviceRepo + EdgeDeviceRelationHost), forwards
// the request through the frontier tunnel (Caller.Call), and returns the
// edge-side JSON verbatim.
//
// Batch protocol (2026-05-07): each schema accepts `paths: string[]`
// (1..hostFilesMaxBatchPaths). The LLM is strongly nudged to send 5..8
// related paths per call; the schema's maxItems=16 is the hard ceiling.
// Per-path failures from the edge surface as Results[i].Error in the
// envelope re-emitted to the LLM, so partial success is observable
// without an extra round-trip.
//
// Why a single file for three tools: they share identical wiring (same
// device→edge resolver, same tunnel.Caller seam, same error envelope) so
// putting them side-by-side makes the symmetry visible. The edge-side
// handlers in internal/edgeagent/host_files/ mirror this layout. The
// closure-style legacy registry path (registry.go::Tool) is NOT used —
// these tools are BaseTool-native from day one (改进点 #1).

// hostFilesCallTimeout caps a single tunnel round-trip. The edge runs
// up to 4 paths concurrently with a 30 s per-path budget and a 60 s
// whole-batch ceiling, so we mirror 60 s here. Stat is always cheap;
// find/du can saturate this budget on a large tree.
const hostFilesCallTimeout = 60 * time.Second

// hostFilesMaxBatchPaths is the manager-side hard upper bound on
// len(paths). Mirrored at the edge in internal/edgeagent/host_files
// for defense in depth. Keep in sync with the schema's maxItems below.
const hostFilesMaxBatchPaths = 16

// hostFilesDeviceResolver is the narrow interface the host_files
// BaseTools need to translate device_id → host edge_id. Both the
// shared DeviceResolver and a test fake satisfy it. Declared locally
// so the test fakes can keep using LookupHostEdge as the seam name
// while the production wiring goes through DeviceResolver.
type hostFilesDeviceResolver interface {
	// LookupHostEdge returns the host edge_id for deviceID, or 0 +
	// nil error when the device has no Type=Host junction row. Real
	// errors (DB outage etc.) propagate.
	LookupHostEdge(ctx context.Context, deviceID uint64) (uint64, error)
}

// deviceResolverAdapter bridges DeviceResolver to the
// hostFilesDeviceResolver interface used internally by the three
// host_files BaseTools. The adapter exists so test code can keep
// injecting a fakeHostFilesResolver that implements LookupHostEdge
// while production wiring goes through the shared DeviceResolver.
type deviceResolverAdapter struct {
	inner DeviceResolver
}

func (a deviceResolverAdapter) LookupHostEdge(ctx context.Context, deviceID uint64) (uint64, error) {
	if a.inner == nil {
		return 0, nil
	}
	return a.inner.ResolveEdgeID(ctx, deviceID)
}

// dispatchEdgeCall is the shared tunnel-call helper used by all three
// host_files BaseTools. It marshals req, applies the per-call timeout,
// fires through caller, and returns the raw response bytes (for the
// tool to re-emit verbatim or unmarshal as it sees fit). toolName is
// used in error messages so the LLM can route the failure cleanly.
func dispatchEdgeCall(ctx context.Context, caller Caller, edgeID uint64, method string, req any, toolName string) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("%s: marshal req: %w", toolName, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, hostFilesCallTimeout)
	defer cancel()
	respBody, err := caller.Call(callCtx, edgeID, method, body)
	if err != nil {
		return nil, fmt.Errorf("%s: dispatch: %w", toolName, err)
	}
	return respBody, nil
}

// validateBatchPaths enforces the 1..maxItems constraint at the BaseTool
// layer. The schema's minItems/maxItems already covers the LLM-supplied
// happy path; this is a belt-and-braces check for the rare case where
// the LLM emits an empty array or the schema validator is bypassed.
func validateBatchPaths(toolName string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("%s: paths required (1..%d)", toolName, hostFilesMaxBatchPaths)
	}
	if len(paths) > hostFilesMaxBatchPaths {
		return fmt.Errorf("%s: too many paths (%d > max %d)", toolName, len(paths), hostFilesMaxBatchPaths)
	}
	for i, p := range paths {
		if p == "" {
			return fmt.Errorf("%s: paths[%d] is empty", toolName, i)
		}
	}
	return nil
}

// =====================================================================
// find_large_files
// =====================================================================

// ToolNameFindLargeFiles is the stable wire name the LLM sees.
const ToolNameFindLargeFiles = "host_find_large_files"

// FindLargeFilesDescription is the one-line "what does this tool do"
// blurb the LLM reads when picking tools.
const FindLargeFilesDescription = "Return the top-N largest files under one or more paths on a specific device, sorted by size descending. Accepts a batch of paths in a single call."

// findLargeFilesWhenToUse is the routing hint shown in the system
// prompt under a "When to use" header. kept distinct
// from Description so skill manifests can override one without
// rewriting the other.
const findLargeFilesWhenToUse = "When the user asks 'find large files' / 'top N largest files' / " +
	"'which files take up disk'. ALWAYS pass multiple related paths in one call (paths is an array, " +
	"max 16) — sending paths one at a time wastes round-trips. Default scan starts from / with top_n=20. " +
	"NOT for log content (use search_logs) or metric trends (use query_promql); " +
	"those answer 'how full is the disk', this answers 'which files are filling it'."

// FindLargeFilesSchema is the JSON Schema of the tool's argument object.
// Mirrors skills/host-files/SKILL.md's 调用规则 section verbatim. The
// `paths` array is the batch knob — minItems=1, maxItems=16; the LLM
// is nudged via description to send 5..8 related paths per call.
var FindLargeFilesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "description": "Device id to scan (same id as the @-mention chip and the Prom device_id label)."},
    "paths": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 16,
      "description": "Directories to scan, 1..16. ALWAYS batch 5..8 related paths per call instead of calling once per path — saves LLM round-trips. Example: [\"/var/log\",\"/var/cache\",\"/opt\"]."
    },
    "top_n": {"type": "integer", "default": 20, "minimum": 1, "maximum": 100, "description": "How many files to return per path; sorted by size descending."},
    "min_size_bytes": {"type": "integer", "default": 1048576, "description": "Lower bound on file size in bytes; default 1 MiB."},
    "exclude_paths": {"type": "array", "items": {"type": "string"}, "default": ["/proc", "/sys", "/dev", "/run"], "description": "Path prefixes to skip; defaults to the virtual filesystems."}
  },
  "required": ["device_id", "paths"]
}`)

// findLargeFilesArgs is the typed form of FindLargeFilesSchema.
type findLargeFilesArgs struct {
	DeviceID     uint64   `json:"device_id"`
	Paths        []string `json:"paths"`
	TopN         int      `json:"top_n"`
	MinSizeBytes int64    `json:"min_size_bytes"`
	ExcludePaths []string `json:"exclude_paths"`
}

// findLargeFilesResultEnvelope wraps the edge response with the
// device_id the call resolved to, plus split success/error views.
// `Results` keeps the per-path order verbatim from the wire response.
// `SuccessCount`/`ErrorCount` give the LLM a quick summary so it can
// decide whether to retry the failed paths without scanning every entry.
type findLargeFilesResultEnvelope struct {
	DeviceID     uint64                             `json:"device_id"`
	SuccessCount int                                `json:"success_count"`
	ErrorCount   int                                `json:"error_count"`
	Results      []tunnel.FindLargeFilesResultEntry `json:"results"`
}

// FindLargeFilesTool is the BaseTool-shape implementation of
// find_large_files. Holds its dependencies on the struct (
// 改进点 #1) so it can be unit-tested without standing up the registry.
type FindLargeFilesTool struct {
	caller   Caller
	resolver hostFilesDeviceResolver
	log      *slog.Logger
}

// NewFindLargeFilesTool builds a new BaseTool. Pass nil log to default
// to slog.Default(). edges may be nil if devices is wired with a real
// junction; the fallback path is only triggered when the junction is
// missing rows (legacy deployment grace).
func NewFindLargeFilesTool(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *FindLargeFilesTool {
	if log == nil {
		log = slog.Default()
	}
	return &FindLargeFilesTool{
		caller:   c,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(d, e)},
		log:      log,
	}
}

// Info returns the tool metadata. Class="read" — find never mutates
// state; the sandbox only allows read of whitelisted paths anyway.
func (t *FindLargeFilesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameFindLargeFiles,
		Description: FindLargeFilesDescription,
		WhenToUse:   findLargeFilesWhenToUse,
		Parameters:  FindLargeFilesSchema,
		Class:       "read",
	}, nil
}

// InvokableRun parses argsJSON, resolves device_id → edge_id, dispatches
// the batched tunnel RPC, and re-emits the response wrapped in
// findLargeFilesResultEnvelope so device_id + success/error counts are
// echoed back to the LLM.
//
// opts are accepted but not consulted for routing — device_id comes
// from the LLM-supplied args, not from the per-call invoke context.
// The decorator chain still consumes opts upstream (audit / ratelimit).
func (t *FindLargeFilesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameFindLargeFiles)
	}
	var in findLargeFilesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameFindLargeFiles, err)
	}
	if in.DeviceID == 0 {
		return "", fmt.Errorf("%s: device_id required", ToolNameFindLargeFiles)
	}
	if err := validateBatchPaths(ToolNameFindLargeFiles, in.Paths); err != nil {
		return "", err
	}
	if in.TopN <= 0 {
		in.TopN = 20
	}
	if in.TopN > 100 {
		in.TopN = 100
	}
	if in.MinSizeBytes <= 0 {
		in.MinSizeBytes = 1 << 20 // 1 MiB
	}
	if in.ExcludePaths == nil {
		in.ExcludePaths = []string{"/proc", "/sys", "/dev", "/run"}
	}

	edgeID, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
	if err != nil {
		return "", fmt.Errorf("%s: resolve device %d: %w", ToolNameFindLargeFiles, in.DeviceID, err)
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: device_id=%d has no host-edge link (try query_devices to list available device ids)", ToolNameFindLargeFiles, in.DeviceID)
	}

	req := tunnel.FindLargeFilesRequest{
		Paths:        in.Paths,
		TopN:         in.TopN,
		MinSizeBytes: in.MinSizeBytes,
		ExcludePaths: in.ExcludePaths,
	}
	respBody, err := dispatchEdgeCall(ctx, t.caller, edgeID, tunnel.MethodFindLargeFiles, req, ToolNameFindLargeFiles)
	if err != nil {
		return "", err
	}
	var resp tunnel.FindLargeFilesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode resp: %w", ToolNameFindLargeFiles, err)
	}
	env := findLargeFilesResultEnvelope{
		DeviceID: in.DeviceID,
		Results:  resp.Results,
	}
	for i := range resp.Results {
		if resp.Results[i].Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameFindLargeFiles, err)
	}
	return string(out), nil
}

// =====================================================================
// du_summary
// =====================================================================

// ToolNameDuSummary is the stable wire name the LLM sees.
const ToolNameDuSummary = "host_du_summary"

// DuSummaryDescription is the one-line "what does this tool do" blurb.
const DuSummaryDescription = "Return per-subdirectory disk usage under one or more paths on a specific device, sorted by size descending. Accepts a batch of paths in a single call."

// duSummaryWhenToUse is the routing hint. The "drill down one level at
// a time" guidance is the most important bit — without it the LLM
// wastes tokens on wide trees.
const duSummaryWhenToUse = "When the user asks 'which directory is growing' / 'disk usage breakdown' / 'du'. " +
	"ALWAYS pass multiple related paths in one call (paths is an array, max 16) — calling once per path is " +
	"the canonical anti-pattern. Drill down one level at a time with depth=1 — do NOT use depth>3 to grab " +
	"the whole tree (slow and the LLM can't make sense of it). NEVER call against /proc /sys (the scan never finishes). " +
	"The response includes a `coverage` field showing what fraction of root-fs used capacity the scanned paths " +
	"explain; if it carries a `hint` saying INCOMPLETE, you MUST re-call with broader paths (typically [\"/\"] at " +
	"depth=1) before answering the user — do not finalize a recommendation while coverage is poor."

// DuSummarySchema is the JSON Schema of the tool's argument object.
var DuSummarySchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "description": "Device id to scan (same id as the @-mention chip and the Prom device_id label)."},
    "paths": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 16,
      "description": "Directories to summarise, 1..16. ALWAYS batch 5..8 related paths per call (e.g. [\"/\",\"/var\",\"/var/log\",\"/opt\",\"/home\",\"/tmp\"]). Calling once per path is the canonical anti-pattern."
    },
    "depth": {"type": "integer", "default": 1, "minimum": 1, "maximum": 5, "description": "How many levels of subdirectories to expand. Default 1; drill down by re-calling with the next paths."}
  },
  "required": ["device_id", "paths"]
}`)

// duSummaryArgs is the typed form of DuSummarySchema.
type duSummaryArgs struct {
	DeviceID uint64   `json:"device_id"`
	Paths    []string `json:"paths"`
	Depth    int      `json:"depth"`
}

// duSummaryResultEnvelope wraps the edge response with device_id +
// success/error counts. Filesystems echoes the edge's df snapshot;
// Coverage is the manager-computed sanity-check ("you scanned 1.2 GB,
// fs_used is 30 GB, you've explained 4%") that nudges weak models to
// re-call with broader top-level paths instead of stopping early.
type duSummaryResultEnvelope struct {
	DeviceID     uint64                        `json:"device_id"`
	SuccessCount int                           `json:"success_count"`
	ErrorCount   int                           `json:"error_count"`
	Results      []tunnel.DuSummaryResultEntry `json:"results"`
	Filesystems  []tunnel.HostFilesystem       `json:"filesystems,omitempty"`
	Coverage     *duCoverage                   `json:"coverage,omitempty"`
}

// duCoverage tells the LLM how much of root-fs used capacity the
// scanned paths actually explain. When ExplainedPct < 80 and the
// request didn't include "/", Hint is non-empty and explicitly tells
// the model to re-call with paths=["/"].
type duCoverage struct {
	RootMount    string  `json:"root_mount,omitempty"`
	FsUsedBytes  int64   `json:"fs_used_bytes,omitempty"`
	FsUsedHuman  string  `json:"fs_used_human,omitempty"`
	ScannedBytes int64   `json:"scanned_bytes,omitempty"`
	ScannedHuman string  `json:"scanned_human,omitempty"`
	ExplainedPct float64 `json:"explained_pct,omitempty"`
	Hint         string  `json:"hint,omitempty"`
}

// DuSummaryTool is the BaseTool-shape implementation of du_summary.
type DuSummaryTool struct {
	caller   Caller
	resolver hostFilesDeviceResolver
	log      *slog.Logger
}

// NewDuSummaryTool builds a new BaseTool. See NewFindLargeFilesTool.
func NewDuSummaryTool(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *DuSummaryTool {
	if log == nil {
		log = slog.Default()
	}
	return &DuSummaryTool{
		caller:   c,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(d, e)},
		log:      log,
	}
}

// Info returns the tool metadata. Class="read".
func (t *DuSummaryTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameDuSummary,
		Description: DuSummaryDescription,
		WhenToUse:   duSummaryWhenToUse,
		Parameters:  DuSummarySchema,
		Class:       "read",
	}, nil
}

// InvokableRun parses args, resolves device_id, dispatches the batch,
// returns the response wrapped with device_id + success/error counts.
func (t *DuSummaryTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameDuSummary)
	}
	var in duSummaryArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameDuSummary, err)
	}
	if in.DeviceID == 0 {
		return "", fmt.Errorf("%s: device_id required", ToolNameDuSummary)
	}
	if err := validateBatchPaths(ToolNameDuSummary, in.Paths); err != nil {
		return "", err
	}
	if in.Depth <= 0 {
		in.Depth = 1
	}
	if in.Depth > 5 {
		in.Depth = 5
	}

	edgeID, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
	if err != nil {
		return "", fmt.Errorf("%s: resolve device %d: %w", ToolNameDuSummary, in.DeviceID, err)
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: device_id=%d has no host-edge link (try query_devices to list available device ids)", ToolNameDuSummary, in.DeviceID)
	}

	req := tunnel.DuSummaryRequest{Paths: in.Paths, Depth: in.Depth}
	respBody, err := dispatchEdgeCall(ctx, t.caller, edgeID, tunnel.MethodDuSummary, req, ToolNameDuSummary)
	if err != nil {
		return "", err
	}
	var resp tunnel.DuSummaryResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode resp: %w", ToolNameDuSummary, err)
	}
	env := duSummaryResultEnvelope{
		DeviceID:    in.DeviceID,
		Results:     resp.Results,
		Filesystems: resp.Filesystems,
	}
	for i := range resp.Results {
		if resp.Results[i].Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	env.Coverage = computeDuCoverage(in.Paths, resp.Results, resp.Filesystems)
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameDuSummary, err)
	}
	return string(out), nil
}

// computeDuCoverage produces the LLM-facing "did your scan explain the
// problem?" sanity check. We sum the TotalSizeBytes of every requested
// path that sits under root, compare against root-fs used capacity, and
// when coverage is poor we synthesize an explicit hint telling the
// model to broaden the scan. Returns nil if we can't compute it (no
// root-fs row, or no successful results).
func computeDuCoverage(reqPaths []string, results []tunnel.DuSummaryResultEntry, fs []tunnel.HostFilesystem) *duCoverage {
	var root *tunnel.HostFilesystem
	for i := range fs {
		if fs[i].Mountpoint == "/" {
			root = &fs[i]
			break
		}
	}
	if root == nil || root.UsedBytes <= 0 {
		return nil
	}
	// Sum only the request paths that look like they're on the root
	// fs. We don't have per-result mount info, so the heuristic is
	// "absolute path that isn't itself a known non-root mount." Cheap
	// and good enough for the common case.
	nonRootMounts := map[string]struct{}{}
	for i := range fs {
		if fs[i].Mountpoint != "/" && fs[i].Mountpoint != "" {
			nonRootMounts[fs[i].Mountpoint] = struct{}{}
		}
	}
	var scanned int64
	scannedRoot := false
	for _, r := range results {
		if r.Error != "" || r.TotalSizeBytes <= 0 {
			continue
		}
		clean := filepath.Clean(r.Path)
		if _, isOtherMount := nonRootMounts[clean]; isOtherMount {
			continue
		}
		scanned += r.TotalSizeBytes
		if clean == "/" {
			scannedRoot = true
		}
	}
	pct := 100.0 * float64(scanned) / float64(root.UsedBytes)
	if pct > 100 {
		// Multiple overlapping paths can over-count; cap at 100.
		pct = 100
	}
	cov := &duCoverage{
		RootMount:    root.Mountpoint,
		FsUsedBytes:  root.UsedBytes,
		FsUsedHuman:  root.UsedHuman,
		ScannedBytes: scanned,
		ScannedHuman: humanBytesSimple(scanned),
		ExplainedPct: roundOnePlace(pct),
	}
	// The hint exists for one reason: when weak models pick a
	// too-narrow scope (e.g. /var/*) and stop. Tell them concretely
	// what to do next.
	if !scannedRoot && pct < 80 {
		cov.Hint = fmt.Sprintf(
			"INCOMPLETE: your scanned paths only explain %.1f%% of root-fs usage (%s of %s used). "+
				"The remaining %.1f%% is in directories you haven't scanned. "+
				"Re-call host_du_summary with paths=[\"/\"] at depth=1 to find the dominant top-level directory, "+
				"then drill down. Do not finalize an answer until coverage is ≥80%% or you've explicitly "+
				"identified where the unaccounted space lives.",
			cov.ExplainedPct, cov.ScannedHuman, cov.FsUsedHuman, 100-cov.ExplainedPct,
		)
	}
	return cov
}

// humanBytesSimple — tiny mirror of the edge's humanBytes so we don't
// have to import the edge package into the manager. Binary prefix.
func humanBytesSimple(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func roundOnePlace(x float64) float64 {
	return float64(int64(x*10+0.5)) / 10
}

// =====================================================================
// stat_file
// =====================================================================

// ToolNameStatFile is the stable wire name the LLM sees.
const ToolNameStatFile = "host_stat_file"

// StatFileDescription is the one-line "what does this tool do" blurb.
const StatFileDescription = "Return size / mtime / atime / mode / owner for one or more files or directories on a specific device. Accepts a batch of paths in a single call."

// statFileWhenToUse is the routing hint. Single-point queries are the
// cheapest, so we explicitly recommend it as the "confirm a hot path"
// step after du_summary — and we strongly nudge passing several at once.
const statFileWhenToUse = "When the user asks for size / mtime / mode / owner of specific files or directories. " +
	"ALWAYS pass multiple paths in one call (paths is an array, max 16) — stat-ing one path at a time is " +
	"the canonical anti-pattern. Cheapest of the host_files tools — use after du_summary surfaces several " +
	"hot paths to confirm them in one shot. NOT for listing many files (use find_large_files)."

// StatFileSchema is the JSON Schema of the tool's argument object.
var StatFileSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "description": "Device id to query (same id as the @-mention chip and the Prom device_id label)."},
    "paths": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 16,
      "description": "Absolute paths of files or directories to stat, 1..16. ALWAYS batch 5..8 related paths per call (e.g. [\"/var/log/messages\",\"/var/log/syslog\",\"/var/cache/apt/archives\"]). Calling once per path is the canonical anti-pattern."
    }
  },
  "required": ["device_id", "paths"]
}`)

// statFileArgs is the typed form of StatFileSchema.
type statFileArgs struct {
	DeviceID uint64   `json:"device_id"`
	Paths    []string `json:"paths"`
}

// statFileResultEnvelope wraps the edge response with device_id +
// success/error counts.
type statFileResultEnvelope struct {
	DeviceID     uint64                       `json:"device_id"`
	SuccessCount int                          `json:"success_count"`
	ErrorCount   int                          `json:"error_count"`
	Results      []tunnel.StatFileResultEntry `json:"results"`
}

// StatFileTool is the BaseTool-shape implementation of stat_file.
type StatFileTool struct {
	caller   Caller
	resolver hostFilesDeviceResolver
	log      *slog.Logger
}

// NewStatFileTool builds a new BaseTool. See NewFindLargeFilesTool.
func NewStatFileTool(c Caller, e *edgebiz.Usecase, d *devicebiz.Usecase, log *slog.Logger) *StatFileTool {
	if log == nil {
		log = slog.Default()
	}
	return &StatFileTool{
		caller:   c,
		resolver: deviceResolverAdapter{inner: NewDeviceResolver(d, e)},
		log:      log,
	}
}

// Info returns the tool metadata. Class="read".
func (t *StatFileTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameStatFile,
		Description: StatFileDescription,
		WhenToUse:   statFileWhenToUse,
		Parameters:  StatFileSchema,
		Class:       "read",
	}, nil
}

// InvokableRun parses args, resolves device_id, dispatches the batch,
// returns the response wrapped with device_id + success/error counts.
func (t *StatFileTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.caller == nil {
		return "", fmt.Errorf("%s: tunnel caller not configured", ToolNameStatFile)
	}
	var in statFileArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameStatFile, err)
	}
	if in.DeviceID == 0 {
		return "", fmt.Errorf("%s: device_id required", ToolNameStatFile)
	}
	if err := validateBatchPaths(ToolNameStatFile, in.Paths); err != nil {
		return "", err
	}

	edgeID, err := t.resolver.LookupHostEdge(ctx, in.DeviceID)
	if err != nil {
		return "", fmt.Errorf("%s: resolve device %d: %w", ToolNameStatFile, in.DeviceID, err)
	}
	if edgeID == 0 {
		return "", fmt.Errorf("%s: device_id=%d has no host-edge link (try query_devices to list available device ids)", ToolNameStatFile, in.DeviceID)
	}

	req := tunnel.StatFileRequest{Paths: in.Paths}
	respBody, err := dispatchEdgeCall(ctx, t.caller, edgeID, tunnel.MethodStatFile, req, ToolNameStatFile)
	if err != nil {
		return "", err
	}
	var resp tunnel.StatFileResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("%s: decode resp: %w", ToolNameStatFile, err)
	}
	env := statFileResultEnvelope{
		DeviceID: in.DeviceID,
		Results:  resp.Results,
	}
	for i := range resp.Results {
		if resp.Results[i].Error != "" {
			env.ErrorCount++
		} else {
			env.SuccessCount++
		}
	}
	out, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameStatFile, err)
	}
	return string(out), nil
}
