package mcpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

const AllPagesArgument = "ongrid_all_pages"

// Legacy servers often omit annotations. Only an explicit query verb can
// opt such a tool into repeated execution; substring classification alone is too broad.
var collectionVerb = regexp.MustCompile(`(^|[._/-])(list|search|query|get|fetch|read)([._/-]|$|[A-Z])`)

type pager struct {
	key, kind string
	first     int
}

func (t Tool) collectionPager() (pager, bool) {
	var s struct{ Properties map[string]json.RawMessage }
	if t.Annotations != nil && t.Annotations.ReadOnlyHint != nil {
		if !*t.Annotations.ReadOnlyHint {
			return pager{}, false
		}
	} else if !collectionVerb.MatchString(t.Name) {
		return pager{}, false
	}
	if ToolClass(t.Name) != "read" || json.Unmarshal(t.InputSchema, &s) != nil || s.Properties[AllPagesArgument] != nil {
		return pager{}, false
	}
	fields := []struct{ key, kind string }{
		{"cursor", "cursor"}, {"page_token", "cursor"}, {"pageToken", "cursor"},
		{"offset", "offset"},
		{"page", "page"}, {"page_num", "page"}, {"pageNumber", "page"}, {"pageNum", "page"}, {"page_number", "page"},
	}
	for _, field := range fields {
		var prop struct {
			Type    string
			Default *int
			Minimum *int
		}
		if json.Unmarshal(s.Properties[field.key], &prop) != nil {
			continue
		}
		if field.kind == "cursor" && prop.Type != "string" || field.kind != "cursor" && prop.Type != "integer" && prop.Type != "number" {
			continue
		}
		first := 0
		if field.kind == "page" {
			first = 1
			if prop.Default != nil && *prop.Default == 0 || prop.Minimum != nil && *prop.Minimum == 0 && prop.Default == nil {
				first = 0
			}
		}
		return pager{field.key, field.kind, first}, true
	}
	return pager{}, false
}

func (t Tool) ChatInputSchema() json.RawMessage {
	schema := t.InputSchema
	if _, ok := t.collectionPager(); !ok {
		return schema
	}
	var s map[string]json.RawMessage
	var props map[string]json.RawMessage
	if json.Unmarshal(schema, &s) != nil || json.Unmarshal(s["properties"], &props) != nil {
		return schema
	}
	props[AllPagesArgument] = json.RawMessage(`{"type":"object","description":"For a complete list or count, pass {} on the first query. Ongrid follows pages of a unique record array and saves it at /structuredContent/rows. Omit for a single page. Use the returned candidate paths if the source is ambiguous.","properties":{"rows_pointer":{"type":"string","description":"Explicit array path relative to structuredContent or a decoded JSON text block. Use an empty string for the document root."},"text_pointer":{"type":"string","description":"Select a specific /content/N/text block when multiple blocks carry the requested rows."},"total_pointer":{"type":"string","description":"Upstream total if nonstandard; total, totalCount and total_count are detected"},"next_pointer":{"type":"string","description":"Upstream continuation if nonstandard; nextCursor, next_cursor and nextPageToken are detected"},"id_pointer":{"type":"string","description":"Unique row identifier within each record, when known"}},"additionalProperties":false}`)
	s["properties"], _ = json.Marshal(props)
	out, _ := json.Marshal(s)
	return out
}

// CanCollect distinguishes a local argument from an identically named upstream parameter.
func CanCollect(t Tool) bool { _, ok := t.collectionPager(); return ok }

type collectionPlan struct {
	resultset.Selection
	ID          string `json:"id_pointer"`
	TextPointer string `json:"text_pointer,omitempty"`
	discover    bool
}

func decodePlan(args map[string]any) (collectionPlan, error) {
	var plan collectionPlan
	raw, err := json.Marshal(args[AllPagesArgument])
	if err != nil {
		return plan, err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return plan, fmt.Errorf("ongrid_all_pages must be an object")
	}
	plan.discover = fields["rows_pointer"] == nil
	if string(fields["rows_pointer"]) == "null" {
		return plan, fmt.Errorf("rows_pointer must be a string when supplied")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&plan); err != nil {
		return plan, err
	}
	for _, ptr := range []string{plan.Rows, plan.Total, plan.Next, plan.ID, plan.TextPointer} {
		if !resultfile.ValidPointer(ptr) {
			return plan, fmt.Errorf("invalid pagination JSON pointer %q", ptr)
		}
	}
	return plan, nil
}

func (pg pager) firstArgs(original map[string]any) (map[string]any, error) {
	args := maps.Clone(original)
	delete(args, AllPagesArgument)
	supplied := args[pg.key]
	if pg.kind == "cursor" {
		if supplied != nil && supplied != "" {
			return nil, fmt.Errorf("a full query must start at the first cursor")
		}
		return args, nil
	}
	if supplied != nil && fmt.Sprint(supplied) != strconv.Itoa(pg.first) {
		return nil, fmt.Errorf("a full query must start at %s=%d", pg.key, pg.first)
	}
	args[pg.key] = pg.first
	return args, nil
}

type collectedRows struct {
	rows         []json.RawMessage
	pages, bytes int
	total        *int
	seenPages    map[[32]byte]bool
	seenIDs      map[string]bool
}

func (s *collectedRows) add(ctx context.Context, page resultset.Page, idPointer string) error {
	if page.Total != nil {
		if s.total != nil && *s.total != *page.Total {
			return fmt.Errorf("upstream total changed during pagination")
		}
		s.total = page.Total
	}
	count := len(s.rows) + len(page.Rows)
	if s.total != nil && count > *s.total {
		return fmt.Errorf("row count exceeds upstream total")
	}
	if count > 100000 {
		return fmt.Errorf("collection exceeds 100000 rows")
	}
	batch := make([]json.RawMessage, 0, len(page.Rows))
	hash := sha256.New()
	for _, row := range page.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if idPointer != "" {
			id, ok := resultfile.Pointer(row, idPointer)
			if !ok || id == nil {
				return fmt.Errorf("missing row identifier")
			}
			encoded, err := json.Marshal(id)
			if err != nil {
				return err
			}
			key := string(encoded)
			if s.seenIDs[key] {
				return fmt.Errorf("duplicate row identifier")
			}
			s.seenIDs[key] = true
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		hash.Write(encoded)
		hash.Write([]byte{'\n'})
		batch = append(batch, encoded)
	}
	digest := [32]byte(hash.Sum(nil))
	if len(batch) > 0 && s.seenPages[digest] {
		return fmt.Errorf("upstream repeated a page")
	}
	s.seenPages[digest] = true
	s.rows = append(s.rows, batch...)
	s.pages++
	return nil
}

func (s *collectedRows) capture(ctx context.Context, cause error) (string, error) {
	data := map[string]any{"pages": s.pages, "rows": s.rows}
	if s.total != nil {
		data["total"] = *s.total
	}
	summary := fmt.Sprintf("Collected %d rows from %d pages. Use /structuredContent/rows.", len(s.rows), s.pages)
	if cause != nil {
		if s.total != nil {
			cause = fmt.Errorf("read %d of %d upstream rows: %w", len(s.rows), *s.total, cause)
		} else {
			cause = fmt.Errorf("read %d upstream rows: %w", len(s.rows), cause)
		}
		summary = cause.Error()
	}
	body, err := json.Marshal(map[string]any{"content": []ContentBlock{{Type: "text", Text: summary}}, "structuredContent": data})
	if err != nil {
		return "", err
	}
	toolcapture.Raw(ctx, body, cause != nil)
	return summary, cause
}

// Collect runs one explicitly requested read across upstream pages. All calls
// reuse the initialized client, original filters, deadline and authorization.
func (c *Client) Collect(ctx context.Context, t Tool, original map[string]any) (string, error) {
	pg, ok := t.collectionPager()
	if !ok {
		return "", fmt.Errorf("tool has no supported read pagination contract")
	}
	plan, err := decodePlan(original)
	if err != nil {
		return "", err
	}
	args, err := pg.firstArgs(original)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	state := collectedRows{rows: []json.RawMessage{}, seenPages: map[[32]byte]bool{}, seenIDs: map[string]bool{}}
	err = c.collectPages(ctx, t.Name, pg, plan, args, &state)
	return state.capture(ctx, err)
}

func (c *Client) collectPages(ctx context.Context, name string, pg pager, plan collectionPlan, args map[string]any, state *collectedRows) error {
	cursors := map[string]bool{}
	for state.pages < 100 {
		// Only the aggregate is linked to the enclosing chat/approval execution.
		res, err := c.CallTool(toolcapture.WithState(ctx), name, args)
		if err != nil {
			return err
		}
		if res.IsError {
			return fmt.Errorf("upstream reported an error: %.512s", res.TextContent())
		}
		page, err := plan.readPage(res, state)
		if err != nil {
			return err
		}
		if err = state.add(ctx, page, plan.ID); err != nil {
			return err
		}
		count := len(state.rows)
		if pg.kind == "cursor" {
			if page.Next == "" {
				if page.HasMore || state.total != nil && count != *state.total || state.total == nil && !page.HasNextField && state.pages == 1 {
					return fmt.Errorf("cannot verify completion without a terminal cursor or matching total")
				}
				return nil
			}
			if cursors[page.Next] {
				return fmt.Errorf("upstream repeated a cursor")
			}
			cursors[page.Next] = true
			args[pg.key] = page.Next
		} else {
			if state.total == nil {
				return fmt.Errorf("page or offset pagination requires an upstream total")
			}
			if count == *state.total {
				if page.Next != "" || page.HasMore {
					return fmt.Errorf("continuation conflicts with upstream total")
				}
				return nil
			}
			if pg.kind == "offset" {
				args[pg.key] = count
			} else {
				args[pg.key] = pg.first + state.pages
			}
		}
		if len(page.Rows) == 0 {
			return fmt.Errorf("upstream pagination made no progress")
		}
	}
	return fmt.Errorf("collection exceeds 100 pages")
}

func (plan *collectionPlan) readPage(res *CallResult, state *collectedRows) (resultset.Page, error) {
	var root any
	var err error
	path := plan.Rows
	if raw := res.StructuredContent; plan.TextPointer == "" && len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		state.bytes += len(raw)
		if state.bytes <= MaxResponseBytes {
			root, err = resultset.Decode(bytes.NewReader(raw))
			if err == nil && plan.discover {
				path, _, err = resultset.DiscoverRows(root, "")
			}
		}
	} else {
		texts := make([]string, len(res.Content))
		for i, block := range res.Content {
			if block.Type == "text" {
				texts[i] = block.Text
				state.bytes += len(block.Text)
			}
		}
		if state.bytes <= MaxResponseBytes {
			requested := &plan.Rows
			if plan.discover {
				requested = nil
			}
			var source resultset.TextRows
			source, err = resultset.SelectTextRows(texts, requested, plan.TextPointer)
			root, path = source.Document, source.RowsPointer
		}
	}
	if state.bytes > MaxResponseBytes {
		return resultset.Page{}, fmt.Errorf("collection exceeds %d bytes", MaxResponseBytes)
	}
	if err != nil {
		return resultset.Page{}, err
	}
	if state.pages > 0 && path != plan.Rows {
		return resultset.Page{}, fmt.Errorf("row path changed from %q to %q during pagination; specify rows_pointer", plan.Rows, path)
	}
	plan.Rows = path
	return plan.Page(root)
}
