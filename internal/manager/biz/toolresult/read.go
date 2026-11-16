package toolresult

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
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
	Start    int64           `json:"start"`
	End      int64           `json:"end"`
	Record   *int64          `json:"record,omitempty"`
	Data     json.RawMessage `json:"-"`
	JSON     string          `json:"data_json,omitempty"`
	Text     string          `json:"text,omitempty"`
	Fragment bool            `json:"fragment,omitempty"`
}
type Page struct {
	ResultID      string `json:"result_id"`
	CaptureStatus string `json:"capture_status"`
	StorageStatus string `json:"storage_status"`
	Mode          string `json:"mode"`
	Items         []Item `json:"items"`
	NextCursor    string `json:"next_cursor,omitempty"`
	ScannedBytes  int64  `json:"scanned_bytes"`
	ScanStart     int64  `json:"scan_start"`
	ScanEnd       int64  `json:"scan_end"`
	ScanComplete  bool   `json:"scan_complete"`
	Count         int64  `json:"count"`
	CountExact    bool   `json:"count_exact"`
	CountScope    string `json:"count_scope"`
	Notice        string `json:"notice,omitempty"`
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
	if req.Mode != "bytes" && req.Mode != "lines" && req.Mode != "json" {
		return nil, errs.ErrInvalid
	}
	if !query && (req.Contains != "" || len(req.EqualsJSON) > 0 || req.Count) {
		return nil, errs.ErrInvalid
	}
	if req.Mode != "json" && (len(req.Fields) > 0 || len(req.EqualsJSON) > 0 || req.Collection != "") {
		return nil, errs.ErrInvalid
	}
	if len(req.EqualsJSON) > 0 && !json.Valid([]byte(req.EqualsJSON)) {
		return nil, errs.ErrInvalid
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
	if req.Mode == "json" {
		err = u.jsonPage(ctx, row, req, offset, p)
	} else {
		err = u.textPage(ctx, row, req, offset, p, query)
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
	if !p.ScanComplete {
		p.NextCursor = u.continuation(row, fingerprint, p.ScanEnd)
	}
	p.CountExact = p.ScanComplete && p.ScanStart == 0 && p.CaptureStatus == "complete" && p.Notice == ""
	return p, nil
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
		return fmt.Errorf("%w: structured_query_unavailable; use bytes mode", errs.ErrInvalid)
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
		item := Item{Start: span.Start, End: span.End, Record: &i, Data: json.RawMessage(body), JSON: string(body)}
		if !fits(p, item) {
			if len(p.Items) > 0 {
				p.ScanEnd = i
				break
			}
			item.Data = nil
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
		p.ScanComplete = true
		p.Notice = "Collection index limit reached; use bytes mode for remaining data."
	}
	return nil
}

func (u *Usecase) textPage(ctx context.Context, row *model.Result, req Request, offset int64, p *Page, query bool) (err error) {
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
	p.ScanEnd = offset
	// Cursor offsets are bytes in both text modes. Lines mode stops at
	// newline boundaries; oversized lines become explicit fragments.
	for p.ScanEnd < row.CapturedBytes && p.ScannedBytes < ScanLimit && ctx.Err() == nil {
		start := p.ScanEnd
		chunk := make([]byte, min(int64(4096), row.CapturedBytes-start, ScanLimit-p.ScannedBytes))
		n, readErr := io.ReadFull(f, chunk)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		chunk = chunk[:n]
		if n == 0 {
			break
		}
		if !utf8.Valid(chunk) && start+int64(n) < row.CapturedBytes {
			for trim := 1; trim < utf8.UTFMax && trim < len(chunk); trim++ {
				if utf8.Valid(chunk[:len(chunk)-trim]) {
					chunk = chunk[:len(chunk)-trim]
					break
				}
			}
		}
		if !utf8.Valid(chunk) {
			return fmt.Errorf("%w: binary data requires download", errs.ErrInvalid)
		}
		consumed := len(chunk)
		item := Item{Start: start}
		if query && req.Contains != "" {
			pos := strings.Index(string(chunk), req.Contains)
			if pos >= 0 {
				item.Start = start + int64(pos)
				end := min(len(chunk), pos+256)
				item.Text = prefix(string(chunk[pos:end]), 256)
				consumed = pos + len(req.Contains)
				item.End = start + int64(consumed)
			} else if start+int64(consumed) < row.CapturedBytes {
				consumed -= min(len(req.Contains)-1, consumed-1)
			}
			if pos >= 0 && !req.Count && !fits(p, item) {
				break
			}
			if pos >= 0 {
				p.Count++
				if !req.Count {
					p.Items = append(p.Items, item)
				}
			}
		} else {
			consumed = min(consumed, 256)
			if req.Mode == "lines" {
				if pos := bytes.IndexByte(chunk[:consumed], '\n'); pos >= 0 {
					consumed = pos + 1
				} else {
					item.Fragment = start+int64(consumed) < row.CapturedBytes
				}
			}
			item.Text = prefix(string(chunk[:consumed]), consumed)
			consumed = len(item.Text)
			item.End = start + int64(consumed)
			if consumed == 0 || !fits(p, item) {
				break
			}
			p.Items = append(p.Items, item)
			p.Count++
		}
		for consumed > 0 && consumed < len(chunk) && !utf8.RuneStart(chunk[consumed]) {
			consumed--
		}
		if consumed == 0 {
			break
		}
		p.ScanEnd = start + int64(consumed)
		p.ScannedBytes += int64(n)
		if _, err = f.Seek(p.ScanEnd, io.SeekStart); err != nil {
			return err
		}
		if len(p.Items) >= req.Limit || (!query && req.Mode == "bytes") {
			break
		}
	}
	p.ScanComplete = p.ScanEnd == row.CapturedBytes
	return nil
}
