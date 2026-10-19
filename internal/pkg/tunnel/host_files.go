package tunnel

import "time"

// Filesystem scan budgets are shared by Manager and Edge so transport waits
// outlast all four waves of a maximum-size batch plus the df snapshot.
const (
	HostFilesPerPathTimeout  = 5 * time.Minute
	HostFilesBatchTimeout    = 4 * HostFilesPerPathTimeout
	HostFilesMetadataTimeout = 30 * time.Second
	HostFilesCallTimeout     = HostFilesBatchTimeout + HostFilesMetadataTimeout + 30*time.Second
)

// host_files RPC method names. These are the wire constants the manager
// (Caller) and edge (RegisterHandler) use to address the three filesystem
// inspection tools. The LLM calls these via the BaseTool
// implementations in internal/manager/biz/aiops/tools/host_files.go.
//
// Body wire format is JSON (). Field names match the public
// JSON Schema declared in the BaseTool Info() method —
// for the snake_case convention.
//
// Batch protocol (2026-05-07): every request takes `paths []string`
// (1..N) and the response contains `results []*ResultEntry`. Each entry
// echoes the same `path` and carries either the per-path payload or a
// per-path `error` string. The edge runs entries concurrently (bounded);
// per-path failures (sandbox reject, find/du non-zero, missing file)
// surface as Error rather than failing the whole batch — the manager
// BaseTool re-emits the entries verbatim so the LLM can decide what to
// retry. The order of `Results` matches the order of `Paths` in the
// request.
const (
	// MethodFindLargeFiles returns the top-N largest files under each
	// requested path, excluding virtual filesystems by default.
	MethodFindLargeFiles = "host_files.find_large_files"

	// MethodDuSummary returns per-subdirectory size totals for each
	// requested path at one expansion level.
	MethodDuSummary = "host_files.du_summary"

	// MethodStatFile returns size / mtime / mode / owner for each
	// requested file or directory. The cheapest of the three; used to
	// confirm hot paths identified by du_summary.
	MethodStatFile = "host_files.stat_file"
)

// ---------------------------------------------------------------------
// host_files.find_large_files (cloud -> edge)
// ---------------------------------------------------------------------

// FindLargeFilesRequest is the wire body for MethodFindLargeFiles. The
// manager-side BaseTool fills these from the LLM's argsJSON after its
// own validation/clamping; the edge does NOT re-clamp (single source of
// truth on the manager side keeps the behaviour testable in one place).
//
// Paths is 1..16 directories to scan; the edge runs them concurrently
// and returns one entry per path in the same order. A per-path failure
// (sandbox reject / find error) surfaces in Results[i].Error and does
// NOT abort the rest of the batch.
type FindLargeFilesRequest struct {
	// Paths is the list of directories to scan. 1..16 entries.
	Paths []string `json:"paths"`

	// TopN caps the number of returned entries per path (after sort by
	// size desc). 1..100, default 20. Applied independently to each
	// path's result set.
	TopN int `json:"top_n"`

	// MinSizeBytes is the lower bound on file size. Files smaller than
	// this are skipped. Default 1 MiB. Same value applied to all paths.
	MinSizeBytes int64 `json:"min_size_bytes"`

	// ExcludePaths is a list of path prefixes to skip. Defaults to the
	// virtual filesystems (/proc /sys /dev /run). Same value applied to
	// all paths.
	ExcludePaths []string `json:"exclude_paths,omitempty"`
}

// FindLargeFilesResultEntry is one per-path slot in the batch response.
// Exactly one of Files / Error is meaningful per row: success rows fill
// ScannedPath + Files (Error empty), failure rows fill Error (Files nil).
type FindLargeFilesResultEntry struct {
	// Path echoes the request path so callers can correlate by string
	// (rather than relying on slice index alone — defense in depth).
	Path string `json:"path"`

	// ScannedPath is the path the edge actually walked (same as Path on
	// success). Omitted on failure.
	ScannedPath string `json:"scanned_path,omitempty"`

	// Files is the top-N largest under Path, sorted by size desc.
	// Truncated per TopN. Nil on failure.
	Files []HostFileInfo `json:"files,omitempty"`

	// Error is non-empty when this path's scan failed. The other rows
	// in Results are still populated; partial success is the expected
	// shape of a batch with a mix of allow-listed and blocked paths.
	Error string `json:"error,omitempty"`
}

// FindLargeFilesResponse is the batch wire body returned by the edge.
// Results is the same length and order as Request.Paths.
type FindLargeFilesResponse struct {
	Results []FindLargeFilesResultEntry `json:"results"`
}

// HostFileInfo is one row in the file-listing results. Used by both
// FindLargeFilesResultEntry and StatFileResultEntry (the two share
// enough fields that a single struct is cleaner than two near-duplicates).
type HostFileInfo struct {
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	SizeHuman string    `json:"size_human"`
	Mtime     time.Time `json:"mtime"`
	Owner     string    `json:"owner,omitempty"`
}

// ---------------------------------------------------------------------
// host_files.du_summary (cloud -> edge)
// ---------------------------------------------------------------------

// DuSummaryRequest is the wire body for MethodDuSummary.
//
// Paths is 1..16 directories to summarise; the edge runs them
// concurrently and returns one entry per path. Per-path failures land
// in Results[i].Error and do not abort the rest.
type DuSummaryRequest struct {
	// Paths is the list of directories to summarise. 1..16 entries.
	Paths []string `json:"paths"`

	// Depth is how many levels of subdirectories to expand. 1..5,
	// default 1. depth=1 is the recommended drill-down primitive.
	// Same value applied to all paths.
	Depth int `json:"depth,omitempty"`
}

// DuSummaryResultEntry is one per-path slot in the batch response.
type DuSummaryResultEntry struct {
	// Path echoes the request path.
	Path string `json:"path"`

	// Subpaths is the per-subdirectory size breakdown, sorted desc.
	// Nil on failure.
	Subpaths []HostDuEntry `json:"subpaths,omitempty"`

	// TotalSizeBytes is the recursive total for Path itself (peeled off
	// from du output, not the sum of Subpaths — du --max-depth lists the
	// path-itself row separately and that number is the authoritative
	// total). 0 on failure.
	TotalSizeBytes int64 `json:"total_size_bytes,omitempty"`

	// TotalSizeHuman is the binary-prefix human form of TotalSizeBytes.
	TotalSizeHuman string `json:"total_size_human,omitempty"`

	// Error is non-empty when this path's du failed.
	Error string `json:"error,omitempty"`
}

// DuSummaryResponse is the batch wire body returned by the edge.
type DuSummaryResponse struct {
	Results []DuSummaryResultEntry `json:"results"`

	// Filesystems is the per-mountpoint capacity of every distinct fs
	// the requested paths live on. Always includes "/" when reachable.
	// Manager uses these to compute a coverage warning when the
	// scanned du-totals only explain a small fraction of fs_used — the
	// canonical "LLM stopped digging too early" case.
	//
	// Optional / best-effort: if df fails the slice is empty but the
	// per-path du results still go through.
	Filesystems []HostFilesystem `json:"filesystems,omitempty"`
}

// HostFilesystem is one mountpoint's capacity, derived from df.
type HostFilesystem struct {
	Mountpoint string `json:"mountpoint"`
	UsedBytes  int64  `json:"used_bytes"`
	SizeBytes  int64  `json:"size_bytes"`
	UsedHuman  string `json:"used_human,omitempty"`
	SizeHuman  string `json:"size_human,omitempty"`
}

// HostDuEntry is one row in DuSummaryResultEntry — one subpath plus its
// recursive size.
type HostDuEntry struct {
	Subpath   string `json:"subpath"`
	SizeBytes int64  `json:"size_bytes"`
	SizeHuman string `json:"size_human"`
}

// ---------------------------------------------------------------------
// host_files.stat_file (cloud -> edge)
// ---------------------------------------------------------------------

// StatFileRequest is the wire body for MethodStatFile.
//
// Paths is 1..16 files or directories to stat. The edge stat'ing is
// pure Go (no subprocess) but the batch interface is symmetric with
// the other two so the LLM only learns one shape.
type StatFileRequest struct {
	// Paths is the list of paths to stat. 1..16 entries.
	Paths []string `json:"paths"`
}

// StatFileResultEntry is one per-path slot in the batch response.
// On success Type/SizeBytes/etc are filled; on failure Error is set
// and the rest are zero values.
type StatFileResultEntry struct {
	// Path echoes the request path.
	Path string `json:"path"`

	Type      string    `json:"type,omitempty"` // "file" | "dir" | "symlink"
	SizeBytes int64     `json:"size_bytes,omitempty"`
	SizeHuman string    `json:"size_human,omitempty"`
	Mtime     time.Time `json:"mtime,omitempty"`
	Atime     time.Time `json:"atime,omitempty"`
	Mode      string    `json:"mode,omitempty"`  // octal, e.g. "0644"
	Owner     string    `json:"owner,omitempty"` // textual user
	Group     string    `json:"group,omitempty"` // textual group

	// Error is non-empty when this path's stat failed (sandbox reject,
	// missing file, permission denied).
	Error string `json:"error,omitempty"`
}

// StatFileResponse is the batch wire body returned by the edge.
type StatFileResponse struct {
	Results []StatFileResultEntry `json:"results"`
}
