package toolresult

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

// Byte counting is deliberately conservative without a provider tokenizer.
const ViewLimit = 4000
const ScanLimit = 8 << 20

type Repo interface {
	CheckWritable(context.Context) error
	Save(context.Context, *model.Result, []byte, []model.Reference) error
	Get(context.Context, string) (*model.Result, error)
	Open(context.Context, *model.Result) (io.ReadSeekCloser, error)
	Index(context.Context, *model.Result) (resultfile.Index, error)
}
type Sessions interface {
	GetSession(context.Context, string) (*session.Session, error)
}
type Scope struct {
	SessionID string
	UserID    uint64
}
type Usecase struct {
	repo          Repo
	sessions      Sessions
	key           []byte
	log           *slog.Logger
	analyzer      Analyzer
	analysisSlots chan struct{}
}

func New(repo Repo, sessions Sessions, signingKey []byte, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{repo: repo, sessions: sessions, key: append([]byte(nil), signingKey...), log: log}
}

func (u *Usecase) Preflight(ctx context.Context, scope Scope) error {
	if _, err := u.root(ctx, scope); err != nil {
		return err
	}
	return u.repo.CheckWritable(ctx)
}

func (u *Usecase) root(ctx context.Context, scope Scope) (string, error) {
	if scope.SessionID == "" {
		return "", errs.ErrForbidden
	}
	id := scope.SessionID
	for i := 0; i < 32; i++ {
		s, err := u.sessions.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		if s.UserID != scope.UserID {
			return "", errs.ErrForbidden
		}
		if s.ParentSessionID == nil || *s.ParentSessionID == "" {
			return s.ID, nil
		}
		id = *s.ParentSessionID
	}
	return "", errs.ErrForbidden
}

func (u *Usecase) Metadata(ctx context.Context, scope Scope, id string) (*model.Result, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, errs.ErrInvalid
	}
	root, err := u.root(ctx, scope)
	if err != nil {
		return nil, err
	}
	r, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.UserID != scope.UserID {
		return nil, errs.ErrNotFound
	}
	owner, err := u.root(ctx, Scope{r.SessionID, r.UserID})
	if err != nil || owner != root {
		return nil, errs.ErrNotFound
	}
	return r, nil
}

func (u *Usecase) Open(ctx context.Context, scope Scope, id string) (*model.Result, io.ReadSeekCloser, error) {
	r, err := u.Metadata(ctx, scope, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := u.repo.Open(ctx, r)
	return r, f, err
}

type View struct {
	Operation          *basetool.Operation `json:"operation,omitempty"`
	Kind               string              `json:"kind"`
	ResultID           string              `json:"result_id,omitempty"`
	Status             string              `json:"status"`
	ExecutionStatus    string              `json:"execution_status"`
	CaptureStatus      string              `json:"capture_status"`
	StorageStatus      string              `json:"storage_status"`
	ViewStatus         string              `json:"view_status"`
	CapturedBytes      int64               `json:"captured_bytes"`
	CollectionPointers []string            `json:"collection_pointers,omitempty"`
	TextPointers       []string            `json:"text_pointers,omitempty"`
	ValuePointers      map[string]string   `json:"value_pointers,omitempty"`
	MoreIndexedPaths   bool                `json:"more_indexed_paths,omitempty"`
	PathIndexPartial   bool                `json:"path_index_partial,omitempty"`
	Preview            string              `json:"preview,omitempty"`
	StructuredContent  json.RawMessage     `json:"structuredContent,omitempty"`
	TableSource        *TableSource        `json:"table_source,omitempty"`
	Instruction        string              `json:"instruction,omitempty"`
}

func sortedPointers(paths []string) []string {
	structured := func(p string) bool { return p == "/structuredContent" || strings.HasPrefix(p, "/structuredContent/") }
	slices.SortFunc(paths, func(a, b string) int {
		if structured(a) != structured(b) {
			if structured(a) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(paths)
}

// Include whole pointers within the model's view budget, with structured MCP data first.
func visiblePointers(paths []string) []string {
	var out []string
	remaining := 768
	for _, path := range sortedPointers(paths) {
		quoted, _ := json.Marshal(path) // A string always marshals.
		if len(quoted)+1 > remaining {
			continue
		}
		out = append(out, path)
		remaining -= len(quoted) + 1
		if len(out) == 12 {
			break
		}
	}
	return out
}

func isMCPResult(tool string) bool { return strings.HasPrefix(tool, "mcp__") || tool == "mcp_call" }

const mcpBlocksNotice = "Selected /content contains MCP display blocks. Counts describe blocks. For business records select the matching structuredContent array, or decode the relevant text block."

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func encodeView(v View) string {
	for {
		b, err := json.Marshal(v)
		if err != nil {
			return `{"status":"failed","error":"result view encoding failed"}`
		}
		if len(b) <= ViewLimit {
			return string(b)
		}
		switch {
		case len(v.Preview) > 0:
			v.Preview = prefix(v.Preview, len(v.Preview)/2)
		case v.TableSource != nil && len(v.TableSource.SampleFields) > 0:
			v.TableSource.SampleFields = v.TableSource.SampleFields[:len(v.TableSource.SampleFields)-1]
		case v.TableSource != nil && len(v.TableSource.ValueCounts) > 0:
			v.TableSource.ValueCounts = v.TableSource.ValueCounts[:len(v.TableSource.ValueCounts)-1]
		case len(v.TextPointers) > 0:
			v.MoreIndexedPaths = true
			v.TextPointers = v.TextPointers[:len(v.TextPointers)-1]
		case len(v.ValuePointers) > 0:
			paths := sortedPointers(slices.Collect(maps.Keys(v.ValuePointers)))
			delete(v.ValuePointers, paths[len(paths)-1])
			v.MoreIndexedPaths = true
		case len(v.CollectionPointers) > 0:
			v.MoreIndexedPaths = true
			v.CollectionPointers = v.CollectionPointers[:len(v.CollectionPointers)-1]
		default:
			return string(b)
		}
	}
}

// Capture runs after execution. Storage failures are data, never a retryable
// tool error: an external write may already have succeeded.
func (u *Usecase) Capture(ctx context.Context, scope Scope, executionID, tool, output string, runErr error, refs []model.Reference) string {
	raw, linked, partial := toolcapture.Get(ctx)
	if linked != "" {
		return output
	} // server-linked approval result; reads still reauthorize its ID
	status, _ := basetool.ResultOutcome(output)
	if runErr != nil {
		status = "error"
	}
	if raw == nil {
		raw = []byte(output)
		if runErr != nil {
			b, err := json.Marshal(struct {
				Output string `json:"output,omitempty"`
				Error  string `json:"error"`
			}{output, runErr.Error()})
			if err != nil {
				return encodeView(View{Kind: "tool_result", Status: status, ExecutionStatus: status, CaptureStatus: "failed", StorageStatus: "failed", ViewStatus: "unavailable"})
			}
			raw = b
		}
	}
	executionStatus := status
	if partial && runErr != nil {
		executionStatus = "unknown"
	}
	row := &model.Result{ID: uuid.NewString(), SessionID: scope.SessionID, UserID: scope.UserID, ToolName: tool, ExecutionStatus: executionStatus, CaptureStatus: "complete", StorageStatus: "ready", ContentType: "text/plain", ExpectedBytes: int64(len(raw)), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour)}
	key := sha256.Sum256([]byte(scope.SessionID + "\x00" + executionID))
	row.ExecutionKey = hex.EncodeToString(key[:])
	if json.Valid(raw) {
		row.ContentType = "application/json"
	}
	if partial || len(raw) > resultfile.MaxSize {
		row.CaptureStatus = "partial"
		row.CaptureReason = "source_or_capture_limit"
	}
	if len(raw) > resultfile.MaxSize {
		raw = raw[:resultfile.MaxSize]
	}
	// Preserve evidence even if the originating request was cancelled.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err := u.repo.Save(writeCtx, row, raw, refs)
	instruction := "Use query_tool_result to select evidence, or read_tool_result for a bounded page. Preview is only an excerpt; do not re-run the original tool to retrieve more data."
	if row.CaptureStatus == "partial" {
		instruction += " The source or capture limit omitted data; saved evidence is incomplete and cannot establish whole-dataset counts or absence."
	} else {
		if status == "success" && tool != "analyze_tool_results" {
			instruction += " For an HTML list, write the complete page and CSS in serve_page.html. Bind one data-ongrid-row with data-ongrid-field elements to this result_id, rows_pointer and filters in serve_page.table. Use data-ongrid-count for totals."
		}
		if u.analyzer != nil {
			instruction += " For custom calculations, joins or nested formats, use analyze_tool_results to evaluate jq expressions over the complete saved inputs."
		}
	}
	v := View{Kind: "tool_result", ResultID: row.ID, Status: status, ExecutionStatus: executionStatus, CaptureStatus: row.CaptureStatus, StorageStatus: "ready", ViewStatus: "excerpt", CapturedBytes: int64(len(raw)), Preview: prefix(string(raw), 2048), Instruction: instruction}
	v.Operation = captureOperation(tool, output)
	if err != nil {
		u.log.Error("tool result persistence failed", slog.String("result_id", row.ID), slog.Any("err", err))
		v.StorageStatus = "failed"
		if errors.Is(err, model.ErrPending) {
			v.StorageStatus = "pending"
			toolcapture.Link(ctx, row.ID)
		}
		v.Instruction = "Execution has already finished. Result storage failed or awaits recovery. Do not repeat the original action."
		return encodeView(v)
	}
	toolcapture.Link(ctx, row.ID)
	if runErr == nil && row.CaptureStatus == "complete" {
		small := len(output) <= ViewLimit && len(raw) <= ViewLimit
		if isMCPResult(tool) {
			if result, ok := inlineMCPResult(v, raw, output, small); ok {
				return result
			}
		} else if small || tool == "draft_config_change" {
			return output
		}
	}
	if row.ContentType == "application/json" {
		idx, indexErr := u.repo.Index(writeCtx, row)
		if indexErr != nil {
			v.PathIndexPartial = true
			u.log.WarnContext(writeCtx, "cannot describe saved result index", "result_id", row.ID, "err", indexErr)
		} else {
			v.CollectionPointers = visiblePointers(slices.Collect(maps.Keys(idx.Collections)))
			v.TextPointers = visiblePointers(idx.TextPointers)
			if len(idx.ValuePointers) > 0 {
				v.ValuePointers = make(map[string]string)
				for _, path := range visiblePointers(slices.Collect(maps.Keys(idx.ValuePointers))) {
					v.ValuePointers[path] = idx.ValuePointers[path]
				}
			}
			v.MoreIndexedPaths = len(v.CollectionPointers) < len(idx.Collections) || len(v.TextPointers) < len(idx.TextPointers) || len(v.ValuePointers) < len(idx.ValuePointers)
			v.PathIndexPartial = idx.PathIndexStatus != resultfile.PathIndexComplete
		}
	}
	if tool == "analyze_tool_results" && status == "success" && u.analyzer != nil {
		v.Instruction += " For this saved analysis use json_text at /stdout with collection_pointer /values or /coverage."
	}
	if isMCPResult(tool) && status == "success" && row.CaptureStatus == "complete" {
		v.TableSource = describeSavedTable(raw, true, true)
		if v.TableSource != nil {
			v.Instruction = "Use saved_row_value_counts for exact individual field distributions within this capture. For a complete HTML list, choose the requested filters from those values and call serve_page directly with your own HTML/CSS, field and count bindings, and table.result_id plus filters. The exporter applies all filters to each source row and verifies pagination. If field relationships or examples are needed, use query_tool_result once with mode=rows, group_by and/or fields and filters. It resolves the business rows by result_id. The preview is an excerpt."
		}
	}
	return encodeView(v)
}

// Fit the business payload independently of the MCP display text. Keep the
// saved reference for follow-up reads and exports. Never truncate the payload.
func inlineMCPResult(v View, raw []byte, output string, small bool) (string, bool) {
	var mcp struct {
		Structured inlineMCPPayload `json:"structuredContent"`
	}
	if json.Unmarshal(raw, &mcp) != nil {
		return output, small
	}
	if len(mcp.Structured) == 0 || string(mcp.Structured) == "null" {
		direct := small || (v.Status == "success" && len(output) <= ViewLimit && isCompleteMCPText(raw, output))
		if direct && v.Status == "success" && json.Valid([]byte(output)) {
			if h := describeSavedTable(raw, true, false); h != nil {
				// Preserve the full business JSON and a usable export reference together when they fit.
				v.TableSource, v.Preview, v.Instruction, v.ViewStatus = h, output, "", "inline"
				if body, err := json.Marshal(v); err == nil && len(body) <= ViewLimit {
					return string(body), true
				}
			}
		}
		return output, direct
	}
	direct := small && string(mcp.Structured) == output
	if direct && (v.Status != "success" || len(resultfile.BuildIndex(mcp.Structured).Collections) == 0) {
		return output, true
	}
	if v.Status != "success" {
		return "", false
	}
	v.ViewStatus = "inline"
	v.StructuredContent = json.RawMessage(mcp.Structured)
	v.Instruction = ""
	v.Preview = ""
	b, err := json.Marshal(v)
	if err != nil || len(b) > ViewLimit {
		return output, direct
	}
	if string(mcp.Structured) != output {
		v.Preview = prefix(output, max(0, ViewLimit-len(b)-len(`,"preview":""`)))
		if v.Preview != "" {
			return encodeView(v), true
		}
	}
	return string(b), true
}

// A JSON string can fit after unescaping even when its wire representation
// does not. Only bypass the saved view if the text accounts for the result.
func isCompleteMCPText(raw []byte, output string) bool {
	var result mcpclient.CallResult
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.IsError || len(result.Content) == 0 {
		return false
	}
	for _, block := range result.Content {
		if block.Type != "text" || len(block.Text) > len(output) {
			return false
		}
	}
	return result.TextContent() == output
}

type inlineMCPPayload []byte

func (p *inlineMCPPayload) UnmarshalJSON(data []byte) error {
	// Skip large business arrays without making another full-size copy. Pretty
	// printing can put a small value over the limit, so ignore JSON whitespace
	// for this lower bound. json.Compact preserves whitespace inside strings.
	if len(data) > ViewLimit {
		n := 0
		for _, b := range data {
			if b > ' ' {
				n++
			}
			if n > ViewLimit {
				return errors.New("MCP payload exceeds inline limit")
			}
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return err
		}
		data = compact.Bytes()
		if len(data) > ViewLimit {
			return errors.New("MCP payload exceeds inline limit")
		}
	}
	*p = append((*p)[:0], data...)
	return nil
}

type cursor struct {
	ID     string `json:"i"`
	Hash   string `json:"h"`
	Query  string `json:"q"`
	Offset int64  `json:"o"`
	Expiry int64  `json:"e"`
}

func (u *Usecase) sign(c cursor) string {
	b, _ := json.Marshal(c) // fixed scalar fields cannot fail to marshal
	m := hmac.New(sha256.New, u.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, m.Sum(nil)...))
}
func (u *Usecase) resume(value string, row *model.Result, fingerprint string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) > 2048 {
		return 0, errs.ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) < sha256.Size {
		return 0, errs.ErrInvalid
	}
	body, sig := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	m := hmac.New(sha256.New, u.key)
	m.Write(body)
	var c cursor
	if !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(body, &c) != nil || c.ID != row.ID || c.Hash != row.SHA256 || c.Query != fingerprint || c.Offset < 0 || time.Now().Unix() > c.Expiry {
		return 0, errs.ErrInvalid
	}
	return c.Offset, nil
}
func (u *Usecase) continuation(row *model.Result, fingerprint string, offset int64) string {
	return u.sign(cursor{row.ID, row.SHA256, fingerprint, offset, time.Now().Add(time.Hour).Unix()})
}

// Only the internal capture tool may carry UI controls into a bounded view.
func captureOperation(tool, output string) *basetool.Operation {
	if tool != "capture_pcap" {
		return nil
	}
	var envelope struct {
		Operation *basetool.Operation `json:"operation"`
	}
	if json.Unmarshal([]byte(output), &envelope) != nil || envelope.Operation == nil {
		return nil
	}
	op := envelope.Operation
	if op.Kind != "packet_capture_session" || len(op.ID) == 0 || len(op.ID) > 128 {
		return nil
	}
	op.State, op.Title, op.Summary = prefix(op.State, 32), prefix(op.Title, 128), prefix(op.Summary, 256)
	detail := op.Links["detail"]
	op.Links = nil
	if len(detail) <= 256 && strings.HasPrefix(detail, "/artifacts/packet-sessions/") {
		op.Links = map[string]string{"detail": detail}
	}
	actions := op.Actions
	op.Actions = nil
	for _, action := range actions {
		if action.Kind == "cancel" {
			op.Actions = []basetool.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: action.Enabled}}
			break
		}
	}
	return op
}
