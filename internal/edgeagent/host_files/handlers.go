// Package host_files registers the three edge-side handlers
// (find_large_files / du_summary / stat_file) that the manager-side
// BaseTools introduced by PR-8 of dispatch through the frontier
// tunnel. The three method constants live in
// internal/pkg/tunnel/host_files.go.
//
// Batch protocol (2026-05-07): every request now carries `paths []string`
// (1..16) and the response carries `results []*ResultEntry`, one per
// path, in the same order. Each path is sandbox-validated independently
// and run concurrently up to hostFilesBatchConcurrency. Per-path failures
// (sandbox reject, find/du non-zero, missing file) surface as
// Results[i].Error and do NOT abort the rest of the batch.
//
// This PR replaces the PR-8 mocks with real implementations gated by the
// SandboxConfig declared alongside in sandbox.go:
//
//   - find_large_files shells out to `find` with -printf on Linux /
//     -exec stat on Darwin (the BSD find ships without -printf).
//     Output is parsed into HostFileInfo, sorted descending by size,
//     truncated to top_n.
//
//   - du_summary shells out to `du`. Linux uses --max-depth=N -B1; Darwin
//     uses -d N (no -B flag). The TSV output (size<TAB>path) is parsed
//     and the path-itself row peeled off into the total.
//
//   - stat_file is pure Go (os.Stat / os.Readlink + syscall.Stat_t for
//     Uid/Gid lookup). Subprocess overhead would dwarf the actual work.
//
// All three pass through SandboxConfig.ValidatePath before any IO and
// honour a hard hostFilesPerPathTimeout per path; the batch as a whole
// is bounded by hostFilesBatchTimeout. On any sandbox or shell failure
// the per-path entry takes the error string; the manager BaseTool
// surfaces the partial batch to the LLM verbatim.
package host_files

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

// hostFilesPerPathTimeout caps a single path's handler work (one find,
// one du, one stat). 30 s is generous for stat / du -d 1 but means a
// `find /` with a deep tree may be cut short — that is intentional. The
// manager BaseTool uses a 60 s tunnel timeout (matching
// hostFilesBatchTimeout below) so this 30 s budget keeps any single
// path from monopolising the tunnel slot.
const hostFilesPerPathTimeout = 30 * time.Second

// hostFilesBatchTimeout caps the whole batch (all paths together). The
// manager-side BaseTool uses the same value for its tunnel.Caller call
// timeout so the LLM-visible upper bound is uniform.
const hostFilesBatchTimeout = 60 * time.Second

// hostFilesBatchConcurrency caps how many paths run in parallel inside
// a single batch. Set deliberately low because each find/du subprocess
// can pin a CPU + saturate disk read bandwidth; running more than 4
// concurrently typically hurts wall-clock time on a typical edge box
// (which is small / shared). Excess paths queue inside errgroup.
const hostFilesBatchConcurrency = 4

// hostFilesMaxBatchPaths is the hard upper bound on len(req.Paths). The
// manager-side schema sets the same maxItems=16 — duplicating the cap
// at the edge is defense in depth (a misbehaving manager / direct edge
// poke can't slip past the schema). Exceeding it returns an error to
// the tunnel layer (whole-batch fail, not per-path) since requesting 17+
// paths is a programming bug, not a recoverable per-path condition.
const hostFilesMaxBatchPaths = 16

// Register installs the three host_files handlers on client gated by a
// SandboxConfig. Idempotent at the tunnel layer — re-registration
// overwrites. Returns an error when the sandbox itself is unhealthy
// (no allow-listed paths, missing find/du binaries) so the caller can
// decide whether to treat that as fatal or boot without the capability.
// log may be nil; the handlers fall back to slog.Default().
func Register(client tunnel.Client, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	sb := DefaultSandboxConfig()
	if err := sb.Validate(); err != nil {
		return err
	}
	log.Info("host_files: sandbox ready",
		slog.Int("allowed_paths", len(sb.AllowedReadPaths)),
		slog.Int("allowed_binaries", len(sb.AllowedBinaries)),
	)
	client.RegisterHandler(tunnel.MethodFindLargeFiles, makeFindLargeFilesHandler(sb, log))
	client.RegisterHandler(tunnel.MethodDuSummary, makeDuSummaryHandler(sb, log))
	client.RegisterHandler(tunnel.MethodStatFile, makeStatFileHandler(sb, log))
	return nil
}

// runBatch is the shared concurrency primitive used by all three
// handlers. It iterates `paths` (already length-validated by the
// caller), runs `work(ctx, path)` for each up to
// hostFilesBatchConcurrency in parallel, and writes the result into
// out[i] preserving input order. work is responsible for catching its
// own per-path errors and stuffing them into out[i].Error — runBatch
// never aborts the loop. The whole batch is ctx-cancelled at
// hostFilesBatchTimeout.
//
// out must already be the same length as paths (the caller pre-allocates
// so worker goroutines can index into it without locking).
func runBatch[T any](ctx context.Context, paths []string, out []T, work func(ctx context.Context, idx int, path string)) error {
	bctx, cancel := context.WithTimeout(ctx, hostFilesBatchTimeout)
	defer cancel()
	g, gctx := errgroup.WithContext(bctx)
	g.SetLimit(hostFilesBatchConcurrency)
	for i, p := range paths {
		i, p := i, p
		g.Go(func() error {
			work(gctx, i, p)
			return nil
		})
	}
	// work() never returns errors (everything goes into out[i].Error)
	// so g.Wait() should only fail if SetLimit panics — propagate just
	// in case future refactors start returning errors from work.
	return g.Wait()
}

// =====================================================================
// find_large_files
// =====================================================================

// makeFindLargeFilesHandler returns a tunnel.Handler that runs `find`
// concurrently across each path in req.Paths, returning one entry per
// path in input order. Per-path failures (sandbox reject, find error)
// land in Results[i].Error and do not abort the rest of the batch.
func makeFindLargeFilesHandler(sb *SandboxConfig, log *slog.Logger) tunnel.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var req tunnel.FindLargeFilesRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, fmt.Errorf("find_large_files: bad req: %w", err)
			}
		}
		if len(req.Paths) == 0 {
			return nil, fmt.Errorf("find_large_files: paths required (1..%d)", hostFilesMaxBatchPaths)
		}
		if len(req.Paths) > hostFilesMaxBatchPaths {
			return nil, fmt.Errorf("find_large_files: too many paths (%d > max %d)", len(req.Paths), hostFilesMaxBatchPaths)
		}
		if req.TopN <= 0 {
			req.TopN = 20
		}
		if req.MinSizeBytes <= 0 {
			req.MinSizeBytes = 1 << 20 // 1 MiB
		}
		findBin, err := sb.ResolveBinary("find")
		if err != nil {
			// Whole-batch fail: no find binary means none of the paths
			// can run; surface a single error rather than N copies.
			return nil, fmt.Errorf("find_large_files: %w", err)
		}

		log.Debug("find_large_files invoked",
			slog.Int("paths", len(req.Paths)),
			slog.Int("top_n", req.TopN),
			slog.Int64("min_size_bytes", req.MinSizeBytes))

		results := make([]tunnel.FindLargeFilesResultEntry, len(req.Paths))
		_ = runBatch(ctx, req.Paths, results, func(gctx context.Context, idx int, path string) {
			results[idx] = runFindOnePath(gctx, sb, findBin, path, req)
		})

		return json.Marshal(tunnel.FindLargeFilesResponse{Results: results})
	}
}

// runFindOnePath validates + runs find for a single path, returning a
// fully-populated entry (success or error). Splitting this out of the
// handler keeps the concurrency loop tiny and lets us unit-test the
// per-path branch without spinning up the batch machinery.
func runFindOnePath(ctx context.Context, sb *SandboxConfig, findBin, path string, req tunnel.FindLargeFilesRequest) tunnel.FindLargeFilesResultEntry {
	entry := tunnel.FindLargeFilesResultEntry{Path: path}
	if err := sb.ValidatePath(path); err != nil {
		entry.Error = err.Error()
		return entry
	}
	cctx, cancel := context.WithTimeout(ctx, hostFilesPerPathTimeout)
	defer cancel()

	files, err := runFindLargeFiles(cctx, findBin, path, req.MinSizeBytes, req.ExcludePaths)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	// Sort descending by size, then truncate to top_n. find on Linux
	// already returns mostly-by-creation-order so an explicit sort here
	// is necessary.
	sort.Slice(files, func(i, j int) bool { return files[i].SizeBytes > files[j].SizeBytes })
	if req.TopN < len(files) {
		files = files[:req.TopN]
	}
	for i := range files {
		files[i].SizeHuman = humanBytes(files[i].SizeBytes)
	}
	entry.ScannedPath = path
	entry.Files = files
	return entry
}

// runFindLargeFiles dispatches to the per-OS find implementation for a
// single path. The split is here (not in the handler) so unit tests can
// target the Linux/Darwin variants independently. minSizeBytes is the
// `-size +<N>c` lower bound; excludePaths get prepended as
// `-path <prefix>* -prune -o` for each prefix.
func runFindLargeFiles(ctx context.Context, findBin, path string, minSizeBytes int64, excludePaths []string) ([]tunnel.HostFileInfo, error) {
	args := []string{path}
	for _, ex := range excludePaths {
		// `-path <prefix>* -prune -o` — must come BEFORE -type f or
		// find still descends into the pruned subtree. Wildcard so
		// /proc/123/... is also pruned, not just /proc itself.
		args = append(args, "-path", ex+"*", "-prune", "-o")
	}
	args = append(args,
		"-type", "f",
		"-size", fmt.Sprintf("+%dc", minSizeBytes),
	)

	switch runtime.GOOS {
	case "linux":
		// GNU find -printf: %s=size, %T@=mtime as unix epoch+frac,
		// %u=owner-name, %p=path. The leading -print at the end of an
		// -o branch is what pairs with -prune to actually skip pruned
		// entries; without it find prints them anyway.
		args = append(args, "-printf", "%s|%T@|%u|%p\n")
		return runFindLinux(ctx, findBin, args)
	case "darwin":
		// BSD find doesn't support -printf. Use -exec with stat -f to
		// emit the same %s|%T@|%Su|%N format. -exec ... + would batch
		// but stat then receives multiple files in one call and prints
		// the format once per arg; we use the safe \; instead.
		args = append(args, "-exec", "stat", "-f", "%z|%m|%Su|%N", "{}", ";")
		return runFindDarwin(ctx, findBin, args)
	default:
		return nil, fmt.Errorf("unsupported GOOS %q", runtime.GOOS)
	}
}

// runFindLinux runs GNU find with the assembled -printf format and
// parses each "%s|%T@|%u|%p\n" line.
func runFindLinux(ctx context.Context, findBin string, args []string) ([]tunnel.HostFileInfo, error) {
	cmd := exec.CommandContext(ctx, findBin, args...)
	// stderr swallowed: find /var with permission errors prints noise
	// for every unreadable subtree but its stdout is still useful.
	out, err := cmd.Output()
	if err != nil && len(out) == 0 && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		// Allow the partial-success case (find returns non-zero when
		// some subtree is inaccessible but we still got valid lines).
		// We only error when there's truly no output to parse.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("find exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return parseFindOutput(out, parseLinuxFindLine)
}

// runFindDarwin runs BSD find with -exec stat. Same parser shape as
// Linux because the format string we passed to stat -f produces the
// same | -separated layout.
func runFindDarwin(ctx context.Context, findBin string, args []string) ([]tunnel.HostFileInfo, error) {
	cmd := exec.CommandContext(ctx, findBin, args...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("find exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, err
	}
	return parseFindOutput(out, parseDarwinFindLine)
}

// parseFindOutput is the shared scanner. parseLine handles per-OS
// epoch-format differences (GNU emits "1714000000.0123", BSD emits
// "1714000000"). We tolerate either by parsing as float.
func parseFindOutput(out []byte, parseLine func(string) (tunnel.HostFileInfo, bool)) ([]tunnel.HostFileInfo, error) {
	var files []tunnel.HostFileInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // long paths
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fi, ok := parseLine(line)
		if !ok {
			continue
		}
		files = append(files, fi)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

// parseLinuxFindLine parses one GNU find -printf '%s|%T@|%u|%p' line.
// %T@ is "<epoch>.<frac>" — we accept both with and without fraction.
func parseLinuxFindLine(line string) (tunnel.HostFileInfo, bool) {
	// Path may itself contain | — split on the first three only.
	parts := strings.SplitN(line, "|", 4)
	if len(parts) != 4 {
		return tunnel.HostFileInfo{}, false
	}
	size, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return tunnel.HostFileInfo{}, false
	}
	mtime, err := parseEpochFloat(parts[1])
	if err != nil {
		return tunnel.HostFileInfo{}, false
	}
	return tunnel.HostFileInfo{
		Path:      parts[3],
		SizeBytes: size,
		Mtime:     mtime,
		Owner:     parts[2],
	}, true
}

// parseDarwinFindLine parses one BSD `stat -f '%z|%m|%Su|%N'` line.
// Format identical to Linux except %m emits an integer epoch.
func parseDarwinFindLine(line string) (tunnel.HostFileInfo, bool) {
	parts := strings.SplitN(line, "|", 4)
	if len(parts) != 4 {
		return tunnel.HostFileInfo{}, false
	}
	size, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return tunnel.HostFileInfo{}, false
	}
	mtime, err := parseEpochFloat(parts[1])
	if err != nil {
		return tunnel.HostFileInfo{}, false
	}
	return tunnel.HostFileInfo{
		Path:      parts[3],
		SizeBytes: size,
		Mtime:     mtime,
		Owner:     parts[2],
	}, true
}

// parseEpochFloat handles "1714000000" and "1714000000.0123". Returns
// time.Time in UTC. We avoid time.Local so test output is stable.
func parseEpochFloat(s string) (time.Time, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}, err
	}
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC(), nil
}

// =====================================================================
// du_summary
// =====================================================================

// makeDuSummaryHandler returns a tunnel.Handler that runs `du`
// concurrently across each path in req.Paths, returning one entry per
// path in input order.
func makeDuSummaryHandler(sb *SandboxConfig, log *slog.Logger) tunnel.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var req tunnel.DuSummaryRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, fmt.Errorf("du_summary: bad req: %w", err)
			}
		}
		if len(req.Paths) == 0 {
			return nil, fmt.Errorf("du_summary: paths required (1..%d)", hostFilesMaxBatchPaths)
		}
		if len(req.Paths) > hostFilesMaxBatchPaths {
			return nil, fmt.Errorf("du_summary: too many paths (%d > max %d)", len(req.Paths), hostFilesMaxBatchPaths)
		}
		if req.Depth <= 0 {
			req.Depth = 1
		}
		duBin, err := sb.ResolveBinary("du")
		if err != nil {
			return nil, fmt.Errorf("du_summary: %w", err)
		}

		log.Debug("du_summary invoked",
			slog.Int("paths", len(req.Paths)),
			slog.Int("depth", req.Depth))

		results := make([]tunnel.DuSummaryResultEntry, len(req.Paths))
		_ = runBatch(ctx, req.Paths, results, func(gctx context.Context, idx int, path string) {
			results[idx] = runDuOnePath(gctx, sb, duBin, path, req.Depth)
		})

		// Best-effort df snapshot. We always include "/" plus any
		// distinct mountpoint that one of the requested paths sits on,
		// so the manager can compute a coverage hint ("scanned du
		// totals only explain X% of fs_used"). Errors here are
		// non-fatal — the LLM still gets the du results back.
		filesystems := collectFilesystems(ctx, sb, req.Paths)

		return json.Marshal(tunnel.DuSummaryResponse{Results: results, Filesystems: filesystems})
	}
}

// collectFilesystems runs `df` for "/" and any other unique mount that
// one of the requested paths lives on, so the manager can compute a
// coverage hint when the scanned du totals only explain a small slice
// of fs_used.
func collectFilesystems(ctx context.Context, sb *SandboxConfig, paths []string) []tunnel.HostFilesystem {
	dfBin, err := sb.ResolveBinary("df")
	if err != nil {
		return nil
	}
	wanted := []string{"/"}
	seen := map[string]struct{}{"/": {}}
	for _, p := range paths {
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		wanted = append(wanted, clean)
	}
	cctx, cancel := context.WithTimeout(ctx, hostFilesPerPathTimeout)
	defer cancel()
	out := make([]tunnel.HostFilesystem, 0, 2)
	seenMount := map[string]struct{}{}
	for _, p := range wanted {
		fs, ok := runDfOne(cctx, dfBin, p)
		if !ok {
			continue
		}
		if _, dup := seenMount[fs.Mountpoint]; dup {
			continue
		}
		seenMount[fs.Mountpoint] = struct{}{}
		out = append(out, fs)
	}
	return out
}

// runDfOne shells out to `df` for one path and returns the single
// filesystem entry it sits on. Best-effort: returns ok=false on any
// failure so the caller silently drops the entry.
func runDfOne(ctx context.Context, dfBin, path string) (tunnel.HostFilesystem, bool) {
	var args []string
	var unitMul int64
	switch runtime.GOOS {
	case "linux":
		args = []string{"-B1", "--output=target,used,size", path}
		unitMul = 1
	case "darwin":
		// BSD df has no -B / --output: -k for 1024-block units, -P
		// for POSIX columns (Filesystem, 1024-blocks, Used,
		// Available, Capacity, Mounted-on).
		args = []string{"-kP", path}
		unitMul = 1024
	default:
		return tunnel.HostFilesystem{}, false
	}
	cmd := exec.CommandContext(ctx, dfBin, args...)
	raw, err := cmd.Output()
	if err != nil || len(raw) == 0 {
		return tunnel.HostFilesystem{}, false
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// Skip header.
	if !scanner.Scan() {
		return tunnel.HostFilesystem{}, false
	}
	if !scanner.Scan() {
		return tunnel.HostFilesystem{}, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 3 {
		return tunnel.HostFilesystem{}, false
	}
	var mount, usedStr, sizeStr string
	switch runtime.GOOS {
	case "linux":
		mount, usedStr, sizeStr = fields[0], fields[1], fields[2]
	case "darwin":
		if len(fields) < 6 {
			return tunnel.HostFilesystem{}, false
		}
		sizeStr, usedStr, mount = fields[1], fields[2], fields[5]
	}
	used, err1 := strconv.ParseInt(usedStr, 10, 64)
	size, err2 := strconv.ParseInt(sizeStr, 10, 64)
	if err1 != nil || err2 != nil {
		return tunnel.HostFilesystem{}, false
	}
	used *= unitMul
	size *= unitMul
	return tunnel.HostFilesystem{
		Mountpoint: mount,
		UsedBytes:  used,
		SizeBytes:  size,
		UsedHuman:  humanBytes(used),
		SizeHuman:  humanBytes(size),
	}, true
}

// runDuOnePath validates + runs du for a single path.
func runDuOnePath(ctx context.Context, sb *SandboxConfig, duBin, path string, depth int) tunnel.DuSummaryResultEntry {
	entry := tunnel.DuSummaryResultEntry{Path: path}
	if err := sb.ValidatePath(path); err != nil {
		entry.Error = err.Error()
		return entry
	}
	cctx, cancel := context.WithTimeout(ctx, hostFilesPerPathTimeout)
	defer cancel()

	entries, total, err := runDu(cctx, duBin, path, depth)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].SizeBytes > entries[j].SizeBytes })
	for i := range entries {
		entries[i].SizeHuman = humanBytes(entries[i].SizeBytes)
	}
	entry.Subpaths = entries
	entry.TotalSizeBytes = total
	entry.TotalSizeHuman = humanBytes(total)
	return entry
}

// runDu shells out to du and parses the size+path TSV output. unitMul
// converts the per-OS reporting unit (bytes on Linux with -B1, KiB-blocks
// on Darwin with -k) into bytes.
func runDu(ctx context.Context, duBin, path string, depth int) ([]tunnel.HostDuEntry, int64, error) {
	var (
		args    []string
		unitMul int64
	)
	switch runtime.GOOS {
	case "linux":
		args = []string{"--max-depth=" + strconv.Itoa(depth), "-B1", path}
		unitMul = 1
	case "darwin":
		// BSD du: -d <depth>, -k for KiB blocks (since -B isn't
		// supported on BSD). 1 KiB = 1024 bytes.
		args = []string{"-d", strconv.Itoa(depth), "-k", path}
		unitMul = 1024
	default:
		return nil, 0, fmt.Errorf("unsupported GOOS %q", runtime.GOOS)
	}
	cmd := exec.CommandContext(ctx, duBin, args...)
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, 0, fmt.Errorf("du exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, 0, err
	}

	// Parse output: each line is "<size>\t<subpath>". The last (or
	// any) line whose subpath equals the request path is the total.
	cleanReq := filepath.Clean(path)
	var entries []tunnel.HostDuEntry
	var total int64
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		// Tab is the documented separator on both GNU and BSD du.
		tab := strings.IndexByte(line, '\t')
		if tab < 0 {
			continue
		}
		sizeBlocks, err := strconv.ParseInt(line[:tab], 10, 64)
		if err != nil {
			continue
		}
		sub := line[tab+1:]
		bytes := sizeBlocks * unitMul
		if filepath.Clean(sub) == cleanReq {
			total = bytes
			continue
		}
		entries = append(entries, tunnel.HostDuEntry{
			Subpath:   sub,
			SizeBytes: bytes,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// =====================================================================
// stat_file
// =====================================================================

// makeStatFileHandler returns a tunnel.Handler that calls os.Lstat for
// each path in req.Paths concurrently. Stat is cheap enough that the
// concurrency is mostly to keep symmetry with the other two handlers
// (and to absorb network/syscall jitter on slow filesystems).
func makeStatFileHandler(sb *SandboxConfig, log *slog.Logger) tunnel.Handler {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, _ tunnel.Session, _ string, body []byte) ([]byte, error) {
		var req tunnel.StatFileRequest
		if len(body) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				return nil, fmt.Errorf("stat_file: bad req: %w", err)
			}
		}
		if len(req.Paths) == 0 {
			return nil, fmt.Errorf("stat_file: paths required (1..%d)", hostFilesMaxBatchPaths)
		}
		if len(req.Paths) > hostFilesMaxBatchPaths {
			return nil, fmt.Errorf("stat_file: too many paths (%d > max %d)", len(req.Paths), hostFilesMaxBatchPaths)
		}

		log.Debug("stat_file invoked", slog.Int("paths", len(req.Paths)))

		results := make([]tunnel.StatFileResultEntry, len(req.Paths))
		_ = runBatch(ctx, req.Paths, results, func(gctx context.Context, idx int, path string) {
			results[idx] = runStatOnePath(gctx, sb, path)
		})

		return json.Marshal(tunnel.StatFileResponse{Results: results})
	}
}

// runStatOnePath does the Lstat + owner/group lookup for a single path.
// Per-path errors (sandbox reject, missing file) become Entry.Error.
func runStatOnePath(_ context.Context, sb *SandboxConfig, path string) tunnel.StatFileResultEntry {
	entry := tunnel.StatFileResultEntry{Path: path}
	if err := sb.ValidatePath(path); err != nil {
		entry.Error = err.Error()
		return entry
	}

	// Lstat first so we can detect symlinks. If it's not a symlink we
	// fall through to the same FileInfo for size/mtime (Lstat is
	// identical to Stat for non-symlinks on Linux/Darwin).
	li, err := os.Lstat(path)
	if err != nil {
		entry.Error = err.Error()
		return entry
	}
	typ := "file"
	switch {
	case li.Mode()&os.ModeSymlink != 0:
		typ = "symlink"
	case li.IsDir():
		typ = "dir"
	}

	// Owner/group — resolved per-GOOS in owner_{unix,windows}.go.
	owner, group := fileOwner(li)

	// Atime is OS-specific in syscall.Stat_t. fileTimes() resolves it
	// per-GOOS so the handler stays portable.
	mtime, atime := fileTimes(li)

	entry.Type = typ
	entry.SizeBytes = li.Size()
	entry.SizeHuman = humanBytes(li.Size())
	entry.Mtime = mtime
	entry.Atime = atime
	entry.Mode = fmt.Sprintf("0%o", li.Mode().Perm())
	entry.Owner = owner
	entry.Group = group
	return entry
}

// =====================================================================
// helpers
// =====================================================================

// humanBytes returns a "12.3 MiB"-style binary size string. Lifted from
// internal/pkg/humanize semantics; kept local so this package has no
// dependency outside std + tunnel types.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	if exp >= len(suffixes) {
		exp = len(suffixes) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(n)/float64(div), suffixes[exp])
}
