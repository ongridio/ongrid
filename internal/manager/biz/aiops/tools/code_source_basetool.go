// code_source_basetool.go — HLD-012 Phase 1. Three read-only tools that let
// the Agent read the SOURCE CODE of registered git repos (the on-disk clone
// Repos-sync already keeps), so an alert/log's file:line / function / error
// string can be correlated to the actual code. Thin wrappers: all the path
// safety, sizing, and repo resolution live in the knowledge biz (CodeBrowser).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	knowledgebiz "github.com/ongridio/ongrid/internal/manager/biz/knowledge"
)

// CodeBrowser is the narrow biz contract these tools need. *knowledge.Usecase
// satisfies it (alongside KnowledgeSearcher).
type CodeBrowser interface {
	ListRepoSources(ctx context.Context, ref, subpath, revision string) (*knowledgebiz.RepoSourceListing, error)
	ReadSource(ctx context.Context, ref, path string, startLine, endLine int, revision string) (*knowledgebiz.SourceFile, error)
	GrepSource(ctx context.Context, ref, pattern, pathGlob string, max int, revision string) (*knowledgebiz.GrepResult, error)
}

const codeSourceWhenToUse = "运维/故障分析时把告警或日志里的代码线索关联到源码,做**逻辑探查**:起点可以是 stack trace 的 " +
	"`pkg/foo/bar.go:123`、panic/报错串、函数或类型名,或用户用自然语言描述的某段行为。" +
	"典型流程:grep_source 按符号/报错串定位 → read_source 读那段(命中 file:line 就读它附近) → " +
	"**顺着调用链迭代追**:对读到的代码里它调用的函数、引用的类型、走的报错分支,继续 grep_source 找定义、" +
	"read_source 跟进,逐层把「输入怎么流到这里、为什么会走到这个分支」理清,再下结论——**别只读一处就停**。" +
	"不熟悉仓库时先 list_repo_sources 看结构。前提:目标仓库已在「代码仓库」注册并 sync 过(repo 入参用仓库 URL 子串或 id)。" +
	"区别于 host_bash(读在线主机文件)——这里读的是控制面上仓库的源码(代码事实/为什么这么写)。"

// ===================== list_repo_sources =====================

const ToolNameListRepoSources = "list_repo_sources"

const listRepoSourcesDescription = "List one directory level of a registered git repo's source tree (dirs first, then files with sizes). Use to discover structure before reading."

var listRepoSourcesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "revision": {"type": "string", "description": "Exact locally synced Git tag (prefer refs/tags/v1.0.0) or full commit SHA. Required. Use explicit HEAD only for unversioned browsing, never for a deployed-version investigation. Missing revisions fail; never fall back to HEAD. Reuse the returned commit_sha in subsequent calls."},
    "repo": {"type": "string", "description": "Which registered repo: its URL (or a unique substring like \"liaison-cloud\") or numeric id."},
    "subpath": {"type": "string", "description": "Directory inside the repo to list (e.g. \"internal/manager\"). Empty = repo root."}
  },
  "required": ["revision", "repo"]
}`)

type listRepoSourcesArgs struct {
	Revision string `json:"revision"`
	Repo     string `json:"repo"`
	Subpath  string `json:"subpath"`
}

// ListRepoSourcesTool is the BaseTool for list_repo_sources.
type ListRepoSourcesTool struct {
	svc CodeBrowser
	log *slog.Logger
}

func NewListRepoSourcesTool(svc CodeBrowser, log *slog.Logger) *ListRepoSourcesTool {
	if log == nil {
		log = slog.Default()
	}
	return &ListRepoSourcesTool{svc: svc, log: log}
}

func (t *ListRepoSourcesTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameListRepoSources,
		Description: listRepoSourcesDescription,
		WhenToUse:   codeSourceWhenToUse,
		Parameters:  listRepoSourcesSchema,
		Class:       "read",
	}, nil
}

func (t *ListRepoSourcesTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.svc == nil {
		return "", fmt.Errorf("%s: code browser not configured", ToolNameListRepoSources)
	}
	var in listRepoSourcesArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameListRepoSources, err)
	}
	if strings.TrimSpace(in.Revision) == "" {
		return "", fmt.Errorf("%s: revision required; use the deployed tag/full commit SHA, or explicit HEAD only for unversioned browsing", ToolNameListRepoSources)
	}
	if err := applyAPMSourceScope(ctx, &in.Repo, &in.Revision, &in.Subpath); err != nil {
		return "", err
	}
	if in.Revision == "HEAD" {
		in.Revision = "" // Explicit unversioned browsing retains the existing biz behavior.
	}
	res, err := t.svc.ListRepoSources(ctx, in.Repo, in.Subpath, in.Revision)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolNameListRepoSources, err)
	}
	return marshalToolJSON(ToolNameListRepoSources, res)
}

// ===================== read_source =====================

const ToolNameReadSource = "read_source"

const readSourceDescription = "Read a source file (or a 1-indexed [start_line,end_line] window) from a registered git repo. The content includes absolute line numbers as N: text; cite these numbers without recounting. Binary files are refused; large files are capped."

var readSourceSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "revision": {"type": "string", "description": "Exact locally synced Git tag (prefer refs/tags/v1.0.0) or full commit SHA. Required. Use explicit HEAD only for unversioned browsing, never for a deployed-version investigation. Missing revisions fail; never fall back to HEAD. Reuse the returned commit_sha in subsequent calls."},
    "repo": {"type": "string", "description": "Which registered repo: URL / unique substring / numeric id."},
    "path": {"type": "string", "description": "File path relative to repo root, e.g. \"internal/pkg/tunnel/messages.go\"."},
    "start_line": {"type": "integer", "description": "1-indexed first line to return. Omit/0 = whole file. Set this to the line from a stack trace.", "minimum": 1},
    "end_line": {"type": "integer", "description": "Inclusive last line. Omit/0 = to EOF (or a sensible window around start_line)."}
  },
  "required": ["revision", "repo", "path"]
}`)

type readSourceArgs struct {
	Revision  string `json:"revision"`
	Repo      string `json:"repo"`
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// ReadSourceTool is the BaseTool for read_source.
type ReadSourceTool struct {
	svc CodeBrowser
	log *slog.Logger
}

func NewReadSourceTool(svc CodeBrowser, log *slog.Logger) *ReadSourceTool {
	if log == nil {
		log = slog.Default()
	}
	return &ReadSourceTool{svc: svc, log: log}
}

func (t *ReadSourceTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameReadSource,
		Description: readSourceDescription,
		WhenToUse:   codeSourceWhenToUse,
		Parameters:  readSourceSchema,
		Class:       "read",
	}, nil
}

func (t *ReadSourceTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.svc == nil {
		return "", fmt.Errorf("%s: code browser not configured", ToolNameReadSource)
	}
	var in readSourceArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameReadSource, err)
	}
	if strings.TrimSpace(in.Revision) == "" {
		return "", fmt.Errorf("%s: revision required; use the deployed tag/full commit SHA, or explicit HEAD only for unversioned browsing", ToolNameReadSource)
	}
	if err := applyAPMSourceScope(ctx, &in.Repo, &in.Revision, &in.Path); err != nil {
		return "", err
	}
	if in.Revision == "HEAD" {
		in.Revision = "" // Explicit unversioned browsing retains the existing biz behavior.
	}
	res, err := t.svc.ReadSource(ctx, in.Repo, in.Path, in.StartLine, in.EndLine, in.Revision)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolNameReadSource, err)
	}
	// Number at the tool boundary; the underlying source API still returns raw text.
	if res != nil {
		copy := *res
		var numbered strings.Builder
		for i, line := range strings.Split(res.Content, "\n") {
			if res.StartLine+i > res.EndLine {
				break
			}
			fmt.Fprintf(&numbered, "%d: %s\n", res.StartLine+i, line)
		}
		copy.Content = numbered.String()
		return marshalToolJSON(ToolNameReadSource, &copy)
	}
	return marshalToolJSON(ToolNameReadSource, res)
}

// ===================== grep_source =====================

const ToolNameGrepSource = "grep_source"

const grepSourceDescription = "Search a registered git repo's tracked source for a regex (function/type names, error strings). Returns path:line:text hits. Binary files skipped."

var grepSourceSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "revision": {"type": "string", "description": "Exact locally synced Git tag (prefer refs/tags/v1.0.0) or full commit SHA. Required. Use explicit HEAD only for unversioned browsing, never for a deployed-version investigation. Missing revisions fail; never fall back to HEAD. Reuse the returned commit_sha in subsequent calls."},
    "repo": {"type": "string", "description": "Which registered repo: URL / unique substring / numeric id."},
    "pattern": {"type": "string", "description": "git-grep basic regex. e.g. a function name \"func ResolveEdgeID\" or an error string \"connection refused\"."},
    "path_glob": {"type": "string", "description": "Optional pathspec to narrow the search, e.g. \"*.go\" or \"internal/manager/\". Empty = whole repo."},
    "max_results": {"type": "integer", "description": "Cap on hits returned. Default 50, max 200.", "default": 50, "minimum": 1, "maximum": 200}
  },
  "required": ["revision", "repo", "pattern"]
}`)

type grepSourceArgs struct {
	Revision   string `json:"revision"`
	Repo       string `json:"repo"`
	Pattern    string `json:"pattern"`
	PathGlob   string `json:"path_glob"`
	MaxResults int    `json:"max_results"`
}

// GrepSourceTool is the BaseTool for grep_source.
type GrepSourceTool struct {
	svc CodeBrowser
	log *slog.Logger
}

func NewGrepSourceTool(svc CodeBrowser, log *slog.Logger) *GrepSourceTool {
	if log == nil {
		log = slog.Default()
	}
	return &GrepSourceTool{svc: svc, log: log}
}

func (t *GrepSourceTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameGrepSource,
		Description: grepSourceDescription,
		WhenToUse:   codeSourceWhenToUse,
		Parameters:  grepSourceSchema,
		Class:       "read",
	}, nil
}

func (t *GrepSourceTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.svc == nil {
		return "", fmt.Errorf("%s: code browser not configured", ToolNameGrepSource)
	}
	var in grepSourceArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameGrepSource, err)
	}
	if strings.TrimSpace(in.Revision) == "" {
		return "", fmt.Errorf("%s: revision required; use the deployed tag/full commit SHA, or explicit HEAD only for unversioned browsing", ToolNameGrepSource)
	}
	if err := applyAPMSourceScope(ctx, &in.Repo, &in.Revision, &in.PathGlob); err != nil {
		return "", err
	}
	if in.Revision == "HEAD" {
		in.Revision = "" // Explicit unversioned browsing retains the existing biz behavior.
	}
	res, err := t.svc.GrepSource(ctx, in.Repo, in.Pattern, in.PathGlob, in.MaxResults, in.Revision)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolNameGrepSource, err)
	}
	return marshalToolJSON(ToolNameGrepSource, res)
}

// marshalToolJSON is the shared "encode the result envelope" helper.
func marshalToolJSON(toolName string, v any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", toolName, err)
	}
	return string(out), nil
}

// All three source tools share the persisted APM boundary. The LLM cannot change
// repositories, switch to HEAD, or escape the bound source directory.
func applyAPMSourceScope(ctx context.Context, repo, revision, subpath *string) error {
	scope := basetool.APMSourceFromContext(ctx)
	if scope == nil {
		return nil
	}
	if scope.Error != "" {
		return fmt.Errorf("APM source unavailable: %s", scope.Error)
	}
	if scope.CommitSHA == "" || scope.RepoID == "" {
		return fmt.Errorf("APM source scope incomplete")
	}
	if *repo != scope.RepoID {
		return fmt.Errorf("APM source: repo must be %s", scope.RepoID)
	}
	if *revision != scope.CommitSHA && *revision != scope.Revision {
		return fmt.Errorf("APM source: revision must be pinned commit %s", scope.CommitSHA)
	}
	*revision = scope.CommitSHA
	// Restrict Git pathspecs to plain paths/globs inside the bound directory.
	// Reject magic/exclusion syntax and traversal instead of attempting to rewrite it.
	if strings.ContainsAny(*subpath, ":\\") || strings.HasPrefix(*subpath, "/") {
		return fmt.Errorf("APM source: invalid source path")
	}
	for _, part := range strings.Split(*subpath, "/") {
		if part == ".." {
			return fmt.Errorf("APM source: path escapes scope")
		}
	}
	dir := strings.Trim(scope.SourceDirectory, "/")
	if dir == "" {
		return nil
	}
	if *subpath == "" {
		*subpath = dir
		return nil
	}
	clean := path.Clean(*subpath)
	if clean != dir && !strings.HasPrefix(clean, dir+"/") {
		return fmt.Errorf("APM source: path must stay inside %s", dir)
	}
	*subpath = clean
	return nil
}
