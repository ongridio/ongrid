// tool_search_tool.go implements ToolSearch entry —
// the always-loaded tool the LLM uses to pull schemas for any
// specialty tool that's been redacted-by-default. It mirrors
// Anthropic's harness convention so the LLM's training prior on the
// "select:..." vs keyword form carries straight over.
//
// The tool is pure-read against an in-memory tool list — no external
// I/O, no per-call allocation worth tracing. We deliberately keep the
// implementation small; the heavy lifting (toolbag partitioning) lives
// in toolbag.go and the Registry decides whether to register it at
// all.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
)

// ToolSearchToolName is the wire name the LLM sees. Anthropic's
// harness uses the same string; matching the name keeps prompt
// portability with imported skills/personas.
const ToolSearchToolName = "ToolSearch"

// DeferredToolBagProvider is the seam ToolSearch uses to query the
// underlying ToolBag without importing the concrete type. Defined on
// the producer side so chatruntime can mirror the shape locally
// without a cyclic import (tools → chatruntime → tools).
type DeferredToolBagProvider interface {
	// DeferredTools returns the tools that have been redacted-by-default
	// (specialty tier). ToolSearch biases keyword queries toward this
	// slice to avoid surfacing tools whose schema is already loaded.
	DeferredTools() []basetool.BaseTool
	// AllTools returns every tool the bag knows about. Used by the
	// "select:..." path so an exact name match works even for tools
	// that are already core.
	AllTools() []basetool.BaseTool
}

// toolSearchWhenToUse — same wording style as the other WhenToUse
// strings in this package; routing hint.
const toolSearchWhenToUse = "When you see a tool name in the system reminder but its schema is not loaded — query for it here. " +
	"Use 'select:foo,bar' to fetch by exact name(s), or keywords like 'find files' for substring match. " +
	"Returns up to max_results (default 5) full schemas matching the query. " +
	"NOT a replacement for actually invoking the tool — after fetching schema, call the tool directly with the now-loaded parameters."

const toolSearchSchema = `{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "Either 'select:name1,name2,...' for exact name match, or a free-text keyword query that substring-matches name + description + when_to_use."
    },
    "max_results": {
      "type": "integer",
      "description": "Maximum number of tool schemas to return. Default 5; clamped to [1, 20].",
      "default": 5
    }
  },
  "required": ["query"]
}`

// toolSearchArgs is the parsed args struct.
type toolSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
}

// toolSearchEntry is one matched tool's metadata in the response. We
// inline the JSON-Schema as a json.RawMessage so the LLM gets the
// untouched schema string (no re-encode round-trip).
type toolSearchEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	WhenToUse   string          `json:"when_to_use,omitempty"`
	Class       string          `json:"class,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolSearchResponse is the JSON body returned to the LLM. We keep the
// envelope minimal — `tools` plus a `query` echo so the LLM can
// reference what it asked for in subsequent turns.
type toolSearchResponse struct {
	Query string            `json:"query"`
	Tools []toolSearchEntry `json:"tools"`
}

// ToolSearchTool is the always-loaded entry point for deferred schema
// loading. Construct via NewToolSearchTool; register on the toolBag
// via WithExtra (BuildBaseTools handles this).
type ToolSearchTool struct {
	bag DeferredToolBagProvider
	log *slog.Logger
}

// NewToolSearchTool builds a ToolSearchTool. p MUST be non-nil — when
// nil there is nothing to search and the tool would always return an
// empty result; we let the construction site fail loudly rather than
// silently degrade.
func NewToolSearchTool(p DeferredToolBagProvider, log *slog.Logger) *ToolSearchTool {
	if log == nil {
		log = slog.Default()
	}
	return &ToolSearchTool{bag: p, log: log}
}

// Info advertises the tool to the LLM. Class="read" because
// ToolSearch never mutates state (the bag is constructed once at boot
// and ToolSearch only reads from it).
func (t *ToolSearchTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolSearchToolName,
		Description: "Fetch full JSON schemas for tools whose details are not yet loaded in this conversation.",
		WhenToUse:   toolSearchWhenToUse,
		Parameters:  json.RawMessage(toolSearchSchema),
		Class:       "read",
	}, nil
}

// InvokableRun parses the query and runs either an exact-name match
// (select:...) or a substring keyword scan over (name + description +
// when_to_use). Always returns valid JSON so the LLM can parse the
// result deterministically; an empty match returns `{"query":"...",
// "tools":[]}` rather than an error.
func (t *ToolSearchTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.bag == nil {
		return "", fmt.Errorf("%s: bag provider not wired", ToolSearchToolName)
	}
	var args toolSearchArgs
	if strings.TrimSpace(argsJSON) != "" {
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("%s: bad args: %w", ToolSearchToolName, err)
		}
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("%s: query required", ToolSearchToolName)
	}
	maxResults := args.MaxResults
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 20 {
		maxResults = 20
	}

	// Use the persona-filtered view from ctx when available (set by
	// chatruntime before graph.Invoke), so ToolSearch only returns tools
	// the current persona (coordinator or worker) is allowed to see.
	// Falls back to AllTools() when no filtering has been applied (e.g.
	// legacy agent path, or a persona with no whitelist).
	searchSet := basetool.FilteredToolsFromContext(ctx)
	if searchSet == nil {
		searchSet = t.bag.AllTools()
	}
	matches := matchTools(ctx, searchSet, args.Query, maxResults)

	resp := toolSearchResponse{Query: args.Query, Tools: matches}
	out, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolSearchToolName, err)
	}
	if t.log != nil {
		t.log.Debug("tool_search invoked",
			slog.String("query", args.Query),
			slog.Int("matches", len(matches)),
			slog.Int("max_results", maxResults),
		)
	}
	return string(out), nil
}

// matchTools applies the query against the tool universe. select:
// prefix → exact-name match (CSV-split). Otherwise substring keyword
// match over name + description + when_to_use. Stable order: input
// order is preserved (no scoring / fuzzy ranking in v1; the LLM
// usually has a clear name in mind).
func matchTools(ctx context.Context, all []basetool.BaseTool, query string, maxResults int) []toolSearchEntry {
	q := strings.TrimSpace(query)
	out := make([]toolSearchEntry, 0, maxResults)

	// "select:" prefix: exact-name CSV match. Empty match → empty
	// result (LLM gets {"tools":[]}).
	if rest, ok := strings.CutPrefix(q, "select:"); ok {
		want := splitNonEmpty(rest, ",")
		if len(want) == 0 {
			return out
		}
		// Use a set for O(1) lookups; preserve all-tools order so
		// the result is deterministic regardless of the input
		// CSV order.
		set := make(map[string]struct{}, len(want))
		for _, w := range want {
			set[w] = struct{}{}
		}
		for _, tool := range all {
			if len(out) >= maxResults {
				break
			}
			info, err := tool.Info(ctx)
			if err != nil || info == nil {
				continue
			}
			if _, ok := set[info.Name]; !ok {
				continue
			}
			out = append(out, toolSearchEntryFromInfo(info))
		}
		return out
	}

	// Keyword match: lower-case substring over (name, description,
	// when_to_use). Multi-token queries require ALL tokens to match
	// somewhere across the haystack — keeps "find files" from
	// matching every read-only tool.
	tokens := splitNonEmpty(strings.ToLower(q), " ")
	if len(tokens) == 0 {
		return out
	}
	for _, tool := range all {
		if len(out) >= maxResults {
			break
		}
		info, err := tool.Info(ctx)
		if err != nil || info == nil {
			continue
		}
		hay := strings.ToLower(info.Name + "\n" + info.Description + "\n" + info.WhenToUse)
		matched := true
		for _, tok := range tokens {
			if !strings.Contains(hay, tok) {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		out = append(out, toolSearchEntryFromInfo(info))
	}
	return out
}

// toolSearchEntryFromInfo flattens a ToolInfo into the response shape.
// Defensive on nil Parameters (turns into "{}") so the LLM always sees
// a valid JSON value at the .parameters position.
func toolSearchEntryFromInfo(info *basetool.ToolInfo) toolSearchEntry {
	params := info.Parameters
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	return toolSearchEntry{
		Name:        info.Name,
		Description: info.Description,
		WhenToUse:   info.WhenToUse,
		Class:       info.Class,
		Parameters:  params,
	}
}

// splitNonEmpty is strings.Split with empty-token + whitespace cleanup.
// Used by both the select:... CSV path and the keyword tokeniser.
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
