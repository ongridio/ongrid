package toolresult

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/resultset"
)

type Request struct {
	ResultID      string   `json:"result_id"`
	Mode          string   `json:"mode,omitempty"`
	Start         int64    `json:"start,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Collection    string   `json:"collection_pointer,omitempty"`
	Fields        []string `json:"fields,omitempty"`
	Contains      string   `json:"contains,omitempty"`
	FilterPointer string   `json:"filter_pointer,omitempty"`
	EqualsJSON    string   `json:"equals_json,omitempty"`
	Count         bool     `json:"count,omitempty"`
}
type Item struct {
	Start    int64  `json:"start"`
	End      int64  `json:"end"`
	Record   *int64 `json:"record,omitempty"`
	JSON     string `json:"data_json,omitempty"`
	Text     string `json:"text,omitempty"`
	Fragment bool   `json:"fragment,omitempty"`
}
type Page struct {
	ResultID       string       `json:"result_id"`
	CaptureStatus  string       `json:"capture_status"`
	StorageStatus  string       `json:"storage_status"`
	Mode           string       `json:"mode"`
	Items          []Item       `json:"items"`
	NextCursor     string       `json:"next_cursor,omitempty"`
	ScannedBytes   int64        `json:"scanned_bytes"`
	ScanStart      int64        `json:"scan_start"`
	ScanEnd        int64        `json:"scan_end"`
	ScanComplete   bool         `json:"scan_complete"`
	Count          int64        `json:"count"`
	CountExact     bool         `json:"count_exact"`
	CountScope     string       `json:"count_scope"`
	Notice         string       `json:"notice,omitempty"`
	TableSource    *TableSource `json:"table_source,omitempty"`
	indexExhausted bool
}

func (u *Usecase) Read(ctx context.Context, scope Scope, req Request, query bool) (*Page, error) {
	if req.Mode == "" {
		req.Mode = "bytes"
	}
	if req.Limit == 0 {
		req.Limit = 50
	}
	if req.Start < 0 || req.Limit < 1 || req.Limit > 200 || len(req.Fields) > 32 || len(req.Contains) > 512 || len(req.EqualsJSON) > 1024 || (req.Cursor != "" && req.Start != 0) {
		return nil, errs.ErrInvalid
	}
	if req.Mode != "bytes" && req.Mode != "lines" && req.Mode != "json" && req.Mode != "structure" {
		return nil, errs.ErrInvalid
	}
	if !query && (req.Contains != "" || len(req.EqualsJSON) > 0 || req.Count) {
		return nil, errs.ErrInvalid
	}
	if req.Mode != "json" && (len(req.Fields) > 0 || len(req.EqualsJSON) > 0 || (req.Mode != "structure" && req.Collection != "")) {
		return nil, errs.ErrInvalid
	}
	if len(req.EqualsJSON) > 0 && !json.Valid([]byte(req.EqualsJSON)) {
		return nil, errs.ErrInvalid
	}
	for _, path := range append(slices.Clone(req.Fields), req.Collection, req.FilterPointer) {
		if len(path) > 1024 || !resultfile.ValidPointer(path) {
			return nil, fmt.Errorf("%w: invalid JSON pointer; use an empty root pointer or a slash-prefixed path with ~0 and ~1 escapes", errs.ErrInvalid)
		}
	}
	if req.FilterPointer != "" && req.EqualsJSON == "" {
		return nil, fmt.Errorf("%w: filter_pointer requires equals_json", errs.ErrInvalid)
	}
	if req.Mode == "structure" && (req.Contains != "" || req.Count) {
		return nil, fmt.Errorf("%w: structure lists saved paths; use json or lines for queries", errs.ErrInvalid)
	}
	if req.Count && req.Mode == "bytes" && req.Contains == "" {
		return nil, fmt.Errorf("%w: use contains to count text matches, or lines to count lines", errs.ErrInvalid)
	}
	row, err := u.Metadata(ctx, scope, req.ResultID)
	if err != nil {
		return nil, err
	}
	if row.StorageStatus != "ready" {
		return nil, fmt.Errorf("%w: result %s", errs.ErrNotFound, row.StorageStatus)
	}
	identity := req
	identity.Cursor = ""
	identity.Start = 0
	b, err := json.Marshal(struct {
		Request
		Query bool
	}{identity, query})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	fingerprint := hex.EncodeToString(hash[:])
	offset, err := u.resume(req.Cursor, row, fingerprint)
	if err != nil {
		return nil, err
	}
	if req.Cursor == "" {
		offset = req.Start
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	p := &Page{ResultID: row.ID, CaptureStatus: row.CaptureStatus, StorageStatus: row.StorageStatus, Mode: req.Mode, Items: []Item{}, ScanStart: offset, ScanEnd: offset, CountScope: "scanned_range_only"}
	switch req.Mode {
	case "structure":
		err = u.structurePage(ctx, row, req, offset, p)
	case "json":
		err = u.jsonPage(ctx, row, req, offset, p)
	default:
		err = u.textPage(ctx, row, req, offset, p)
	}
	if parentCtx.Err() != nil {
		return nil, parentCtx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) && parentCtx.Err() == nil {
		err = nil
		p.ScanComplete = false
	}
	if err != nil {
		return nil, err
	}
	if !p.ScanComplete && !p.indexExhausted {
		if p.ScanEnd == p.ScanStart {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("result page made no progress; check the start offset or use a different mode")
		}
		p.NextCursor = u.continuation(row, fingerprint, p.ScanEnd)
	}
	p.CountExact = p.ScanComplete && p.ScanStart == 0 && p.CaptureStatus == "complete" && p.Notice == ""
	if isMCPResult(row.ToolName) && req.Mode == "json" && req.Collection == "/content" {
		p.Notice = strings.TrimSpace(p.Notice + " " + mcpBlocksNotice)
	}
	return p, nil
}

func (u *Usecase) structurePage(ctx context.Context, row *model.Result, req Request, offset int64, p *Page) error {
	idx, err := u.repo.Index(ctx, row)
	if err != nil {
		return err
	}
	// Older file-backed indexes lack native values and most text locations.
	// Rediscover them only when the payload fits a single reader scan.
	rebuildIndex := idx.PathIndexStatus == "" && row.ContentType == "application/json" && row.CapturedBytes <= ScanLimit
	describeRows := req.Collection == "" && offset == 0 && isMCPResult(row.ToolName) && row.ExecutionStatus == "success" && row.CaptureStatus == "complete" && row.CapturedBytes <= maxTextTableHintBytes && (rebuildIndex || len(idx.Collections) > 0)
	if rebuildIndex || describeRows {
		f, err := u.repo.Open(ctx, row)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(f, row.CapturedBytes))
		if err = errors.Join(readErr, f.Close()); err != nil {
			return err
		}
		p.ScannedBytes = int64(len(body))
		if p.ScannedBytes != row.CapturedBytes {
			return resultfile.ErrCorrupt
		}
		if rebuildIndex {
			idx = resultfile.BuildIndex(body)
		}
		if describeRows {
			p.TableSource, err = describeSavedTable(body, true, true)
			if errors.Is(err, resultset.ErrAmbiguousRows) {
				p.Notice = prefix(err.Error(), 1024)
			}
		}
	}
	kinds := make(map[string]string, len(idx.Collections)+len(idx.TextPointers)+len(idx.ValuePointers))
	maps.Copy(kinds, idx.ValuePointers)
	for _, path := range idx.TextPointers {
		kinds[path] = "text"
	}
	for path := range idx.Collections {
		kinds[path] = "array"
	}
	paths := sortedPointers(slices.Collect(maps.Keys(kinds)))
	if req.Collection != "" {
		paths = slices.DeleteFunc(paths, func(path string) bool {
			return path != req.Collection && !strings.HasPrefix(path, req.Collection+"/")
		})
	}
	if offset > int64(len(paths)) {
		return errs.ErrInvalid
	}
	for i := offset; i < int64(len(paths)); i++ {
		kind := kinds[paths[i]]
		descriptor, err := json.Marshal(struct {
			Kind    string `json:"kind"`
			Pointer string `json:"pointer"`
		}{kind, paths[i]})
		if err != nil {
			return err
		}
		item := Item{Start: i, End: i + 1, JSON: string(descriptor)}
		if !fits(p, item) {
			if len(p.Items) > 0 {
				break
			}
			item.JSON = ""
			item.Text = "Path exceeds the tool response limit."
			item.Fragment = true
		}
		p.Items = append(p.Items, item)
		p.ScanEnd = i + 1
		p.Count++
		if len(p.Items) == req.Limit {
			break
		}
	}
	p.ScanComplete = p.ScanEnd == int64(len(paths))
	if p.ScanComplete && idx.PathIndexStatus != resultfile.PathIndexComplete {
		p.ScanComplete = false
		p.indexExhausted = true
		p.Notice = "Path index limit reached. Further paths may exist in the saved data. Use byte reads or analyze a known pointer."
		if idx.PathIndexStatus == "" {
			p.Notice = "This earlier index lists only previously saved paths. Use byte reads or analyze a known pointer to inspect other data."
		}
	}
	return nil
}

func fits(p *Page, item Item) bool {
	p.Items = append(p.Items, item)
	b, err := json.Marshal(p)
	p.Items = p.Items[:len(p.Items)-1]
	// Leave room for a signed continuation and its metadata.
	return err == nil && len(b) < ViewLimit-1024
}

func (u *Usecase) jsonPage(ctx context.Context, row *model.Result, req Request, offset int64, p *Page) (err error) {
	idx, err := u.repo.Index(ctx, row)
	if err != nil {
		return err
	}
	col, ok := idx.Collections[req.Collection]
	if !ok {
		paths := visiblePointers(slices.Collect(maps.Keys(idx.Collections)))
		return fmt.Errorf("%w: structured_query_unavailable for collection_pointer %q; indexed arrays include %q; use mode=rows with result_id and no wrapper pointers for decoded business rows, structure to list paths or bytes for unindexed data", errs.ErrInvalid, req.Collection, paths)
	}
	if offset > int64(len(col.Items)) {
		return errs.ErrInvalid
	}
	f, err := u.repo.Open(ctx, row)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	p.ScanEnd = offset
	var expected any
	if len(req.EqualsJSON) > 0 {
		d := json.NewDecoder(strings.NewReader(req.EqualsJSON))
		d.UseNumber()
		if err := d.Decode(&expected); err != nil {
			return errs.ErrInvalid
		}
	}
	for i := offset; i < int64(len(col.Items)); i++ {
		if ctx.Err() != nil || p.ScannedBytes >= ScanLimit {
			break
		}
		span := col.Items[i]
		size := span.End - span.Start
		if size < 0 || span.Start < 0 || span.End > row.CapturedBytes {
			return resultfile.ErrCorrupt
		}
		if req.Count && req.Contains == "" && req.EqualsJSON == "" {
			p.Count++
			p.ScanEnd = i + 1
			continue
		}
		if size > 1<<20 {
			item := Item{Start: span.Start, End: span.End, Record: &i, Fragment: true, Text: "Record exceeds structured query limit; use read_tool_result bytes mode at this range."}
			if !fits(p, item) {
				break
			}
			p.Items = append(p.Items, item)
			p.ScanEnd = i + 1
			p.Notice = "Some records exceed the structured query limit; count excludes them."
			if len(p.Items) >= req.Limit {
				break
			}
			continue
		}
		if p.ScannedBytes+size > ScanLimit {
			break
		}
		if _, err = f.Seek(span.Start, io.SeekStart); err != nil {
			return err
		}
		body := make([]byte, int(size))
		if _, err = io.ReadFull(f, body); err != nil {
			return err
		}
		p.ScannedBytes += size
		p.ScanEnd = i + 1
		if req.Contains != "" && !bytes.Contains(body, []byte(req.Contains)) {
			continue
		}
		var value any
		if len(req.Fields) > 0 || len(req.EqualsJSON) > 0 {
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if err = d.Decode(&value); err != nil {
				return resultfile.ErrCorrupt
			}
		}
		if len(req.EqualsJSON) > 0 {
			actual, found := resultfile.Pointer(value, req.FilterPointer)
			if !found {
				continue
			}
			a, e := json.Marshal(actual)
			if e != nil {
				return e
			}
			b, e := json.Marshal(expected)
			if e != nil {
				return e
			}
			if !bytes.Equal(a, b) {
				continue
			}
		}
		if req.Count {
			p.Count++
			continue
		}
		if len(req.Fields) > 0 {
			fields := map[string]any{}
			for _, field := range req.Fields {
				if v, ok := resultfile.Pointer(value, field); ok {
					fields[field] = v
				}
			}
			body, err = json.Marshal(fields)
			if err != nil {
				return err
			}
		}
		item := Item{Start: span.Start, End: span.End, Record: &i, JSON: string(body)}
		if !fits(p, item) {
			if len(p.Items) > 0 {
				p.ScanEnd = i
				break
			}
			item.JSON = ""
			item.Text = prefix(string(body), 256)
			item.Fragment = true
		}
		p.Items = append(p.Items, item)
		p.Count++
		if len(p.Items) >= req.Limit {
			break
		}
	}
	p.ScanComplete = p.ScanEnd == int64(len(col.Items)) && col.Complete
	if p.ScanEnd == int64(len(col.Items)) && !col.Complete {
		p.indexExhausted = true
		p.Notice = "Collection index limit reached. Use analyze_tool_results on the saved array when available, or bytes mode for the remaining data."
	}
	return nil
}

func (u *Usecase) textPage(ctx context.Context, row *model.Result, req Request, offset int64, p *Page) (err error) {
	if offset > row.CapturedBytes {
		return errs.ErrInvalid
	}
	f, err := u.repo.Open(ctx, row)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	// Read ahead once and keep unconsumed bytes for the next item. Charge
	// the actual read, including lookahead, against the per-run budget.
	size := min(ScanLimit, row.CapturedBytes-offset)
	source := &io.LimitedReader{R: f, N: size}
	reader := bufio.NewReaderSize(source, 4096)
	scanLimit := offset + size
	for p.ScanEnd < scanLimit && ctx.Err() == nil {
		n := int(min(int64(reader.Size()), scanLimit-p.ScanEnd))
		chunk, err := reader.Peek(n)
		p.ScannedBytes = size - source.N
		if err != nil {
			return err
		}
		atEOF := p.ScanEnd+int64(n) == row.CapturedBytes
		if !utf8.Valid(chunk) {
			// The buffer or scan budget can end inside a valid rune.
			end := len(chunk) - 1
			for end > 0 && !utf8.RuneStart(chunk[end]) {
				end--
			}
			if atEOF || utf8.FullRune(chunk[end:]) || !utf8.Valid(chunk[:end]) {
				return fmt.Errorf("%w: binary data requires download", errs.ErrInvalid)
			}
			chunk = chunk[:end]
		}
		start := p.ScanEnd
		switch {
		case req.Contains != "":
			readTextMatches(p, req, chunk, atEOF)
		case req.Count:
			p.Count += int64(bytes.Count(chunk, []byte{'\n'}))
			if atEOF && len(chunk) > 0 && chunk[len(chunk)-1] != '\n' {
				p.Count++
			}
			p.ScanEnd += int64(len(chunk))
		default:
			readTextItems(p, req, chunk, atEOF)
		}
		used := int(p.ScanEnd - start)
		if used == 0 || len(p.Items) >= req.Limit {
			break
		}
		if _, err := reader.Discard(used); err != nil {
			return err
		}
	}
	p.ScanComplete = p.ScanEnd == row.CapturedBytes
	return nil
}

func readTextItems(p *Page, req Request, chunk []byte, atEOF bool) {
	for len(chunk) > 0 && len(p.Items) < req.Limit {
		n := min(len(chunk), 256)
		lineEnd := false
		if req.Mode == "lines" {
			if newline := bytes.IndexByte(chunk[:n], '\n'); newline >= 0 {
				n = newline + 1
				lineEnd = true
			}
		}
		if !lineEnd && !atEOF && len(chunk) < 256 {
			return // Carry this tail into the next buffer.
		}
		for n < len(chunk) && !utf8.RuneStart(chunk[n]) {
			n--
		}
		fragment := req.Mode == "lines" && !lineEnd && !(atEOF && n == len(chunk))
		item := Item{Start: p.ScanEnd, End: p.ScanEnd + int64(n), Text: string(chunk[:n]), Fragment: fragment}
		if !fits(p, item) {
			return
		}
		p.Items = append(p.Items, item)
		if !fragment {
			p.Count++
		}
		p.ScanEnd = item.End
		chunk = chunk[n:]
	}
}

func readTextMatches(p *Page, req Request, chunk []byte, atEOF bool) {
	needle := []byte(req.Contains)
	for len(chunk) > 0 && len(p.Items) < req.Limit {
		pos := bytes.Index(chunk, needle)
		if pos < 0 {
			n := len(chunk)
			if !atEOF {
				// A match may span the next buffer or the next page.
				n = max(0, n-len(needle)+1)
				for n > 0 && n < len(chunk) && !utf8.RuneStart(chunk[n]) {
					n--
				}
			}
			p.ScanEnd += int64(n)
			return
		}
		end := pos + len(needle)
		if !req.Count {
			excerptEnd := min(pos+256, len(chunk))
			for excerptEnd < len(chunk) && !utf8.RuneStart(chunk[excerptEnd]) {
				excerptEnd--
			}
			item := Item{Start: p.ScanEnd + int64(pos), End: p.ScanEnd + int64(end), Text: string(chunk[pos:excerptEnd])}
			if !fits(p, item) {
				return
			}
			p.Items = append(p.Items, item)
		}
		p.Count++
		p.ScanEnd += int64(end)
		chunk = chunk[end:]
	}
}
