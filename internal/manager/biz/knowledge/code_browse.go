package knowledge

// code_browse.go — read-only access to the on-disk git clones of registered
// repos (HLD-012). Repos sync already clones the FULL tree (incl. source) to
// cloneDir/<id> and keeps it; scanRepoFiles only *indexes* prose into RAG.
// These methods expose the rest so the Agent can correlate an alert/log's
// file:line / symbol to the actual code — without embedding code into RAG.
//
// Everything here is read-only and sandboxed to a single repo's clone dir:
// path-traversal guarded, size-capped, binary-skipping, result-truncating.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/knowledge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

const (
	// maxSourceFileBytes caps a single read_source response. Source files
	// are small; 512 KiB is generous and bounds RAM + LLM context.
	maxSourceFileBytes = 512 << 10
	// maxGrepHits / maxListEntries cap grep + listing output so a huge repo
	// can't blow the LLM context. Both are also clamped from the tool args.
	maxGrepHits    = 200
	maxListEntries = 500
	// codeGrepTimeout bounds a git-grep over a large tree.
	codeGrepTimeout = 30 * time.Second
)

// RepoSourceEntry is one file/dir in a one-level repo listing.
type RepoSourceEntry struct {
	Path  string `json:"path"` // relative to repo root
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// RepoSourceListing is the result of ListRepoSources.
type RepoSourceListing struct {
	CommitSHA string            `json:"commit_sha"`
	Repo      string            `json:"repo"` // resolved repo URL
	RepoID    uint64            `json:"repo_id"`
	Subpath   string            `json:"subpath"`
	Entries   []RepoSourceEntry `json:"entries"`
	Truncated bool              `json:"truncated,omitempty"`
}

// SourceFile is the result of ReadSource (optionally a line window).
type SourceFile struct {
	CommitSHA string `json:"commit_sha"`
	Repo      string `json:"repo"`
	RepoID    uint64 `json:"repo_id"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

// GrepHit is one git-grep match.
type GrepHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GrepResult is the result of GrepSource.
type GrepResult struct {
	CommitSHA string    `json:"commit_sha"`
	Repo      string    `json:"repo"`
	RepoID    uint64    `json:"repo_id"`
	Pattern   string    `json:"pattern"`
	Hits      []GrepHit `json:"hits"`
	Truncated bool      `json:"truncated,omitempty"`
}

// resolveRepoClone maps a repo ref (numeric id, exact URL, or a unique
// case-insensitive URL substring) to its registered row + on-disk clone dir.
// Errors: ErrNotFound (no/ambiguous match), ErrInvalid (matched but the clone
// dir doesn't exist yet — repo registered but never synced).
func (u *Usecase) resolveRepoClone(ctx context.Context, ref string) (*model.Repository, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", fmt.Errorf("%w: repo ref required", errs.ErrInvalid)
	}
	repos, err := u.repo.ListRepos(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("knowledge: list repos: %w", err)
	}
	if len(repos) == 0 {
		return nil, "", fmt.Errorf("%w: no git repos registered — add one on the 代码仓库 page first", errs.ErrNotFound)
	}

	var matched *model.Repository
	if id, e := strconv.ParseUint(ref, 10, 64); e == nil {
		for _, r := range repos {
			if r.ID == id {
				matched = r
				break
			}
		}
	}
	if matched == nil { // exact URL
		for _, r := range repos {
			if strings.EqualFold(r.URL, ref) {
				matched = r
				break
			}
		}
	}
	if matched == nil { // unique substring
		lref := strings.ToLower(ref)
		var hits []*model.Repository
		for _, r := range repos {
			if strings.Contains(strings.ToLower(r.URL), lref) {
				hits = append(hits, r)
			}
		}
		switch len(hits) {
		case 1:
			matched = hits[0]
		case 0:
			return nil, "", fmt.Errorf("%w: no repo matches %q", errs.ErrNotFound, ref)
		default:
			urls := make([]string, 0, len(hits))
			for _, h := range hits {
				urls = append(urls, h.URL)
			}
			return nil, "", fmt.Errorf("%w: %q matches multiple repos (%s) — be more specific or use the id", errs.ErrInvalid, ref, strings.Join(urls, ", "))
		}
	}

	dir := u.repoDir(matched.ID)
	if fi, e := os.Stat(dir); e != nil || !fi.IsDir() {
		return nil, "", fmt.Errorf("%w: repo %q is registered but not synced yet — run a sync first", errs.ErrInvalid, matched.URL)
	}
	return matched, dir, nil
}

// safeRepoPath joins rel onto root and guarantees the result stays within
// root (defeats ../ traversal + absolute-path escapes). rel "" → root.
func safeRepoPath(root, rel string) (string, error) {
	// Rooting rel at "/" then Clean collapses any leading ../ so the join
	// can never climb above root.
	clean := filepath.Clean("/" + strings.TrimSpace(rel))
	full := filepath.Join(root, clean)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: path escapes repo", errs.ErrInvalid)
	}
	return absFull, nil
}

// looksBinary reports whether b appears to be a binary blob (contains a NUL
// in the inspected prefix) — git's own heuristic for -I.
func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// cleanRepoRel normalizes a repo-relative path for a git pathspec: forward
// slashes, no leading "/", no ".." segment. git itself sandboxes "HEAD:<rel>"
// to the repo tree, but we reject ".." for a clean early error.
func cleanRepoRel(p string) (string, error) {
	c := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "/")
	if c == "" {
		return "", fmt.Errorf("%w: path required", errs.ErrInvalid)
	}
	for _, seg := range strings.Split(c, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: path escapes repo", errs.ErrInvalid)
		}
	}
	return c, nil
}

// ListRepoSources lists one directory level of a repo's clone (like `ls`),
// dirs first then files, alpha-sorted. subpath "" = repo root. The ".git"
// dir is hidden.
func (u *Usecase) ListRepoSources(ctx context.Context, ref, subpath, revision string) (*RepoSourceListing, error) {
	repo, dir, err := u.resolveRepoClone(ctx, ref)
	if err != nil {
		return nil, err
	}
	commit, err := sourceRevision(ctx, dir, revision)
	if err != nil {
		return nil, err
	}
	// One level of the HEAD tree via plumbing (works on bare clones). A
	// subpath lists its immediate children; "" lists the repo root.
	subClean := strings.Trim(strings.TrimSpace(filepath.ToSlash(subpath)), "/")
	lsArgs := []string{"-C", dir, "ls-tree", "-l", commit}
	if subClean != "" {
		if _, e := cleanRepoRel(subClean); e != nil {
			return nil, e
		}
		lsArgs = append(lsArgs, "--", subClean+"/")
	}
	cctx, cancel := context.WithTimeout(ctx, codeGrepTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", lsArgs...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	outBytes, runErr := cmd.Output()
	if runErr != nil {
		return nil, fmt.Errorf("knowledge: git ls-tree: %w", runErr)
	}
	out := &RepoSourceListing{CommitSHA: commit, Repo: repo.URL, RepoID: repo.ID, Subpath: subClean}
	for _, ln := range strings.Split(strings.TrimRight(string(outBytes), "\n"), "\n") {
		if ln == "" {
			continue
		}
		if len(out.Entries) >= maxListEntries {
			out.Truncated = true
			break
		}
		// Line: "<mode> <type> <sha> <size>\t<path-from-root>" (size is "-"
		// for trees). Fields() collapses the size-padding whitespace.
		tab := strings.IndexByte(ln, '\t')
		if tab < 0 {
			continue
		}
		meta := strings.Fields(ln[:tab])
		if len(meta) < 2 {
			continue
		}
		isDir := meta[1] == "tree"
		e := RepoSourceEntry{Path: filepath.ToSlash(ln[tab+1:]), IsDir: isDir}
		if !isDir && len(meta) >= 4 {
			if sz, perr := strconv.ParseInt(meta[3], 10, 64); perr == nil {
				e.Size = sz
			}
		}
		out.Entries = append(out.Entries, e)
	}
	if subClean != "" && len(out.Entries) == 0 {
		return nil, fmt.Errorf("%w: %q not found in selected revision (or not a directory)", errs.ErrNotFound, subpath)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		if out.Entries[i].IsDir != out.Entries[j].IsDir {
			return out.Entries[i].IsDir // dirs first
		}
		return out.Entries[i].Path < out.Entries[j].Path
	})
	return out, nil
}

// ReadSource returns a file's text from a repo clone. When startLine>0 it
// returns the inclusive 1-indexed [startLine,endLine] window (endLine<=0 =
// to EOF). Binary files are refused; reads are capped at maxSourceFileBytes.
func (u *Usecase) ReadSource(ctx context.Context, ref, path string, startLine, endLine int, revision string) (*SourceFile, error) {
	repo, dir, err := u.resolveRepoClone(ctx, ref)
	if err != nil {
		return nil, err
	}
	commit, err := sourceRevision(ctx, dir, revision)
	if err != nil {
		return nil, err
	}
	rel, err := cleanRepoRel(path)
	if err != nil {
		return nil, err
	}
	// Pure plumbing: read the blob straight from the HEAD tree object — works
	// on a bare/no-checkout clone, and git's pathspec is inherently sandboxed
	// to the repo tree (no filesystem traversal possible).
	cctx, cancel := context.WithTimeout(ctx, codeGrepTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "-C", dir, "cat-file", "blob", commit+":"+rel)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	raw, runErr := cmd.Output()
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && bytes.Contains(ee.Stderr, []byte("not a blob")) {
			return nil, fmt.Errorf("%w: %q is a directory (use list_repo_sources)", errs.ErrInvalid, path)
		}
		return nil, fmt.Errorf("%w: %q not found in selected revision", errs.ErrNotFound, path)
	}
	truncated := false
	if len(raw) > maxSourceFileBytes {
		raw = raw[:maxSourceFileBytes]
		truncated = true
	}
	if looksBinary(raw) {
		return nil, fmt.Errorf("%w: %q looks binary — source-read is text-only", errs.ErrInvalid, path)
	}
	out := &SourceFile{CommitSHA: commit, Repo: repo.URL, RepoID: repo.ID, Path: filepath.ToSlash(path), Truncated: truncated}
	if startLine <= 0 {
		out.StartLine = 1
		out.Content = string(raw)
		out.EndLine = len(strings.Split(strings.TrimSuffix(out.Content, "\n"), "\n"))
		if len(raw) == 0 {
			out.EndLine = 0
		}
		return out, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(raw) == 0 {
		lines = nil
	}
	if startLine > len(lines) {
		return nil, fmt.Errorf("%w: start_line %d past EOF (%d lines)", errs.ErrInvalid, startLine, len(lines))
	}
	end := endLine
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if end < startLine {
		end = startLine
	}
	out.StartLine = startLine
	out.EndLine = end
	out.Content = strings.Join(lines[startLine-1:end], "\n")
	return out, nil
}

// GrepSource runs `git grep` over a repo clone's tracked files. pattern is a
// basic-regex (git grep default); pathGlob optionally narrows via a pathspec.
// Binary files are skipped (-I); hits are capped at min(max, maxGrepHits).
func (u *Usecase) GrepSource(ctx context.Context, ref, pattern, pathGlob string, max int, revision string) (*GrepResult, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("%w: pattern required", errs.ErrInvalid)
	}
	repo, dir, err := u.resolveRepoClone(ctx, ref)
	if err != nil {
		return nil, err
	}
	commit, err := sourceRevision(ctx, dir, revision)
	if err != nil {
		return nil, err
	}
	if max <= 0 || max > maxGrepHits {
		max = maxGrepHits
	}

	cctx, cancel := context.WithTimeout(ctx, codeGrepTimeout)
	defer cancel()
	// Grep the HEAD tree object (not the working tree) so this works against
	// a bare / no-checkout clone too — pure git plumbing. Output lines are
	// then "HEAD:path:line:text" (the rev is prefixed); we strip it below.
	args := []string{"-C", dir, "grep", "-n", "-I", "--no-color", "-e", pattern, commit}
	if g := strings.TrimSpace(pathGlob); g != "" {
		// Guard the pathspec against traversal too — it's relative to root.
		if _, perr := safeRepoPath(dir, g); perr != nil {
			return nil, perr
		}
		args = append(args, "--", g)
	}
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	outBytes, runErr := cmd.Output()
	// git grep exit codes: 0 = matches, 1 = no matches (NOT an error), >1 = error.
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == 1 {
			return &GrepResult{CommitSHA: commit, Repo: repo.URL, RepoID: repo.ID, Pattern: pattern}, nil
		}
		return nil, fmt.Errorf("knowledge: git grep: %w", runErr)
	}

	res := &GrepResult{CommitSHA: commit, Repo: repo.URL, RepoID: repo.ID, Pattern: pattern}
	for _, ln := range strings.Split(strings.TrimRight(string(outBytes), "\n"), "\n") {
		if ln == "" {
			continue
		}
		if len(res.Hits) >= max {
			res.Truncated = true
			break
		}
		// `git grep <rev>` prefixes each line with "<rev>:" — strip it so
		// the format is the familiar path:line:text.
		ln = strings.TrimPrefix(ln, commit+":")
		// Format: path:line:text
		p1 := strings.IndexByte(ln, ':')
		if p1 < 0 {
			continue
		}
		rest := ln[p1+1:]
		p2 := strings.IndexByte(rest, ':')
		if p2 < 0 {
			continue
		}
		lineNo, _ := strconv.Atoi(rest[:p2])
		res.Hits = append(res.Hits, GrepHit{
			Path: filepath.ToSlash(ln[:p1]),
			Line: lineNo,
			Text: strings.TrimSpace(rest[p2+1:]),
		})
	}
	return res, nil
}
