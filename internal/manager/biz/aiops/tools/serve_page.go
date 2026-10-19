package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
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
	store PageStore
	log   *slog.Logger
}

// NewServePageTool builds the tool.
func NewServePageTool(s PageStore, log *slog.Logger) *ServePageTool {
	if log == nil {
		log = slog.Default()
	}
	return &ServePageTool{store: s, log: log}
}

var servePageSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "html": { "type": "string", "description": "完整的 HTML 文档字符串（自带 <html><body>…）。会原样托管。" },
    "title": { "type": "string", "description": "可选标题，用于页面列表展示。" }
  },
  "required": ["html"]
}`)

const servePageWhenToUse = "把你生成的 HTML 报告 / 临时状态面板托管成一个可访问的内网链接时用——" +
	"比如\"把这次巡检结果做成一个网页给我\"。传完整 HTML，返回的 url 直接发给用户即可（内网可打开）。" +
	"适合一次性的报告页，不是长期应用。"

// Info — Class=write: it publishes a page (side-effecting) but not destructive.
func (t *ServePageTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:         ToolNameServePage,
		Description:  "Host a generated HTML page at an internal, shareable URL (a diagnostic report / status panel). Returns the URL to give the user.",
		WhenToUse:    servePageWhenToUse,
		Parameters:   servePageSchema,
		Class:        "write",
		Confirmation: basetool.ConfirmationNotRequired,
	}, nil
}

type servePageArgs struct {
	HTML  string `json:"html"`
	Title string `json:"title"`
}

// InvokableRun persists the page and returns its URL.
func (t *ServePageTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.store == nil {
		return "", fmt.Errorf("serve_page: page store not wired")
	}
	var in servePageArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("serve_page: bad args: %w", err)
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
	out, _ := json.Marshal(map[string]any{
		"id":  id,
		"url": viewURL,
		// Tell the model to render url as a Markdown link, not a bare path —
		// so the user gets a clickable "查看页面" instead of /pages/<hash> text.
		"note": "页面已生成。请用 Markdown 链接形式把它给用户（例如 [查看页面](" + viewURL + ")），不要只贴路径文本。页面需登录查看；要发给未登录的人，让用户去「产物」里点该页的「分享」按钮生成公开链接。",
	})
	return string(out), nil
}
