package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
)

// ToolNameServePage is the wire name.
const ToolNameServePage = "serve_page"

// PageStore is the seam to the hosted-pages store. Implemented in cmd/main.go;
// it persists the HTML and the manager serves it at the returned URL.
type PageStore interface {
	// SavePage stores an HTML page and returns its id + the relative URL the
	// manager serves it at (e.g. /pages/<token>).
	SavePage(ctx context.Context, title, html string) (id string, url string, err error)
}

// ServePageTool lets the assistant turn a generated HTML page (a diagnostic
// report, a temporary status panel) into a hosted, shareable internal URL.
type ServePageTool struct {
	store   PageStore
	results *toolresult.Usecase
	log     *slog.Logger
}

// NewServePageTool builds the tool.
func NewServePageTool(s PageStore, results *toolresult.Usecase, log *slog.Logger) *ServePageTool {
	if log == nil {
		log = slog.Default()
	}
	return &ServePageTool{store: s, results: results, log: log}
}

var servePageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "html": { "type": "string", "description": "Write the complete HTML document and CSS for the requested page. For a saved list, also supply table. Design one repeatable element (tr, li, article or div) marked data-ongrid-row alone in its container. Bind empty cells or spans with data-ongrid-field (a row JSON Pointer). Example: <tbody><tr data-ongrid-row><td data-ongrid-field=\"/name\"></td></tr></tbody>. Put verified totals in empty spans with data-ongrid-count=\"exported_rows\", \"excluded_rows\" or \"source_rows\" outside the row. Use these bindings for records and metrics; sample rows and hardcoded totals can be inaccurate." },
    "title": { "type":"string", "description":"Page title" },
    "table": {
      "type":"object","description":"Put result_id, rows_pointer and filters inside this object. filters is a JSON array. Include both html and table for complete lists and inventory exports. A unique business row array is resolved automatically from result_id; supply explicit paths when ambiguous. A previously saved page with the full upstream total can be exported directly. For incomplete upstream pages collect the remaining rows first. For a broad upstream search, include the requested entity type and scope as well as exclusions in filters, unless the upstream query already enforced them. Use saved field values to choose the criteria.",
      "properties":{
        "result_id":{"type":"string"},
        "rows_pointer":{"type":"string","description":"JSON Pointer to the business row array, e.g. /structuredContent/rows. Use an empty string for a root array."},
        "text_pointer":{"type":"string","description":"For JSON encoded inside a text block, use its discovered text_pointer when ambiguous. rows_pointer and pagination pointers are relative to the decoded text; column and filter pointers are relative to each row."},
        "total_pointer":{"type":"string","description":"Upstream total if nonstandard"},
        "next_pointer":{"type":"string","description":"Upstream continuation if nonstandard"},
        "all_remaining_categories":{"type":"boolean","description":"For an exclusion-only export, set true only when every remaining category belongs to the requested population. Otherwise apply the requested positive filters."},
        "expected_rows":{"type":"integer","minimum":0,"description":"Optional consistency check. Omit unless the upstream declares a total for the entire source array before any table filters. Filtered and exported counts are computed by the server."},
        "columns":{"type":"array","description":"Optional legacy projection. Normally the field bindings in html select the columns.","minItems":1,"maxItems":32,"items":{"type":"object","properties":{"label":{"type":"string"},"pointer":{"type":"string","description":"Field pointer within one row"}},"required":["label","pointer"],"additionalProperties":false}},
        "filters":` + toolresult.TableFiltersSchema + `
      },"required":["result_id"],"additionalProperties":false
    }
  },
  "required":["html"],
  "additionalProperties":false
}`)

const servePageWhenToUse = "把你生成的 HTML 报告 / 临时状态面板托管成一个可访问的内网链接时用——" +
	"比如\"把这次巡检结果做成一个网页给我\"。由你编写页面布局、样式和记录的展示方式。使用已保存的清单时，同时传 html 和 table，将字段和统计数绑定到原始数据。" +
	"适合一次性的报告页，不是长期应用。"

// Info — Class=write: it publishes a page (side-effecting) but not destructive.
func (t *ServePageTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:         ToolNameServePage,
		Description:  "Host a generated HTML page at an internal, shareable URL (a diagnostic report / status panel). Write the HTML and CSS. For lists from saved data, bind the verified rows to the page with table. Returns the URL to give the user.",
		WhenToUse:    servePageWhenToUse,
		Parameters:   servePageSchema,
		Class:        "write",
		Confirmation: basetool.ConfirmationNotRequired,
	}, nil
}

type servePageArgs struct {
	Table *toolresult.TableSpec `json:"table,omitempty"`
	HTML  string                `json:"html"`
	Title string                `json:"title"`
}

// InvokableRun persists the page and returns its URL.
func (t *ServePageTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("serve_page: page store not wired")
	}
	var in servePageArgs
	decoder := json.NewDecoder(strings.NewReader(argsJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return "", fmt.Errorf("serve_page: bad args (put saved-result filters and paths inside table): %w", err)
	}
	if strings.TrimSpace(argsJSON[decoder.InputOffset():]) != "" {
		return "", fmt.Errorf("serve_page: unexpected trailing arguments")
	}
	var counts *toolresult.TableCounts
	if in.Table != nil {
		if t.results == nil {
			return "", fmt.Errorf("serve_page: table requires a saved-result reader")
		}
		scope := toolresult.Scope{SessionID: basetool.SessionIDFromContext(ctx), UserID: basetool.ResolveOptions(opts).UserID}
		var n toolresult.TableCounts
		var err error
		if in.HTML != "" {
			in.HTML, n, err = t.results.RenderTablePage(ctx, scope, in.HTML, *in.Table)
		} else if basetool.ArtifactSourceFromContext(ctx) == basetool.ArtifactSourceChat {
			return "", fmt.Errorf("serve_page: write the page in html with a data-ongrid-row and field bindings; pass table alongside it")
		} else {
			in.HTML, n, err = t.results.RenderTable(ctx, scope, in.Title, *in.Table)
		}
		if err != nil {
			return "", fmt.Errorf("serve_page: %w", err)
		}
		counts = &n
	} else if id := toolresult.ChatPageSource(ctx); toolresult.PageNeedsSource(in.HTML, id != "") {
		if id != "" {
			return "", fmt.Errorf("serve_page: this run returned a saved collection (%s); bind the intended result in table and keep your HTML layout", id)
		}
		return "", fmt.Errorf("serve_page: HTML data bindings require table with the saved result_id and rows_pointer")
	}
	if strings.TrimSpace(in.HTML) == "" {
		return "", fmt.Errorf("serve_page: html is required")
	}
	id, _, err := t.store.SavePage(ctx, in.Title, in.HTML)
	if err != nil {
		return "", fmt.Errorf("serve_page: %w", err)
	}
	// Hand the user the IN-APP artifact link, not the raw /api/pages read
	// endpoint: pages are private (login required) and a bare API path 401s on a
	// top-level click. /pages/<id> opens the page in the 产物 viewer, where the
	// user is already authenticated — and where the 分享 button mints a public
	// link if they need to send it off-platform.
	viewURL := "/pages/" + id
	t.log.Info("serve_page: hosted", slog.String("id", id), slog.String("url", viewURL))
	response := map[string]any{
		"id":  id,
		"url": viewURL,
		// Tell the model to render url as a Markdown link, not a bare path —
		// so the user gets a clickable "查看页面" instead of /pages/<hash> text.
		"note": "页面已生成。请用 Markdown 链接形式把它给用户（例如 [查看页面](" + viewURL + ")），不要只贴路径文本。页面需登录查看；要发给未登录的人，让用户去「产物」里点该页的「分享」按钮生成公开链接。",
	}
	if counts != nil {
		response["counts"] = counts
		if len(counts.ExcludedByFilter) > 0 {
			response["count_note"] = "excluded_by_filter counts rows first rejected by each table.filters entry in order. Use those counts for the breakdown. excluded_rows is the combined total."
		}
	}
	out, _ := json.Marshal(response)
	return string(out), nil
}
