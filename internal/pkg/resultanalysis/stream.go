package resultanalysis

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var errStopped = errors.New("input iteration stopped")

// streamSource retains one record, including when JSON/CSV is escaped in MCP text.
func streamSource(r io.Reader, source Source, emit func(any) bool) error {
	if source.Format == "json" {
		return selectJSON(r, source.Pointer, func(b *bufio.Reader) error { return arrayRows(b, emit) })
	}
	if source.Format == "json_value" {
		return selectJSON(r, source.Pointer, func(b *bufio.Reader) error {
			value, err := jsonValue(b)
			if err != nil {
				return err
			}
			if !emit(value) {
				return errStopped
			}
			return nil
		})
	}
	parse := func(text io.Reader) error {
		switch source.Format {
		case "json_text":
			return selectJSON(text, source.CollectionPointer, func(b *bufio.Reader) error { return arrayRows(b, emit) })
		case "csv":
			return csvRows(text, emit)
		default:
			s := bufio.NewScanner(text)
			s.Buffer(make([]byte, 4096), MaxRecordBytes)
			for s.Scan() {
				if !emit(s.Text()) {
					return errStopped
				}
			}
			return s.Err()
		}
	}
	if source.Pointer == "" && source.Format != "json_text" {
		return parse(r)
	}
	return selectJSON(r, source.Pointer, func(b *bufio.Reader) error {
		q, err := newQuotedReader(b)
		if err != nil {
			return err
		}
		return parse(q)
	})
}

func csvRows(r io.Reader, emit func(any) bool) error {
	// Bound one CSV record even when a quoted field contains many newlines.
	limited := &recordReader{r: r, left: MaxRecordBytes + 1}
	c := csv.NewReader(limited)
	header, err := c.Read()
	if err != nil {
		return fmt.Errorf("CSV header: %w", err)
	}
	seen := map[string]bool{}
	for _, name := range header {
		if name == "" || seen[name] {
			return fmt.Errorf("CSV column names must be unique and nonempty")
		}
		seen[name] = true
	}
	last := c.InputOffset()
	if last > MaxRecordBytes {
		return fmt.Errorf("CSV header exceeds 1 MiB")
	}
	for {
		// csv.Reader can buffer ahead; account for those bytes in the next record.
		limited.left = MaxRecordBytes + 1 - (limited.read - last)
		record, err := c.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("CSV row: %w", err)
		}
		if c.InputOffset()-last > MaxRecordBytes {
			return fmt.Errorf("CSV record exceeds 1 MiB")
		}
		last = c.InputOffset()
		row := make(map[string]any, len(header))
		for i, name := range header {
			row[name] = record[i]
		}
		if !emit(row) {
			return errStopped
		}
	}
}

type recordReader struct {
	r          io.Reader
	left, read int64
}

func (r *recordReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, fmt.Errorf("record exceeds 1 MiB")
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.r.Read(p)
	r.left -= int64(n)
	r.read += int64(n)
	return n, err
}

func skipSpace(r *bufio.Reader) error {
	for {
		b, err := r.Peek(1)
		if err != nil {
			return err
		}
		if !strings.ContainsRune(" \t\r\n", rune(b[0])) {
			return nil
		}
		_, _ = r.ReadByte()
	}
}
func expect(r *bufio.Reader, want byte) error {
	if err := skipSpace(r); err != nil {
		return err
	}
	b, err := r.ReadByte()
	if err != nil {
		return err
	}
	if b != want {
		return fmt.Errorf("expected %q in JSON", want)
	}
	return nil
}

// selectJSON validates the wrapper without retaining unrelated arrays or strings.
func selectJSON(in io.Reader, pointer string, selected func(*bufio.Reader) error) error {
	r := bufio.NewReader(in)
	matches := 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if depth > 128 {
			return fmt.Errorf("JSON nesting exceeds 128")
		}
		if err := skipSpace(r); err != nil {
			return err
		}
		if path == pointer {
			matches++
			if matches > 1 {
				return fmt.Errorf("JSON pointer matches duplicate keys")
			}
			return selected(r)
		}
		first, _ := r.Peek(1)
		switch first[0] {
		case '{':
			_, _ = r.ReadByte()
			if err := skipSpace(r); err != nil {
				return err
			}
			b, _ := r.Peek(1)
			if b[0] == '}' {
				_, _ = r.ReadByte()
				return nil
			}
			for {
				if err := skipSpace(r); err != nil {
					return err
				}
				q, err := newQuotedReader(r)
				if err != nil {
					return err
				}
				key, err := io.ReadAll(io.LimitReader(q, 4097))
				if err != nil {
					return err
				}
				if len(key) > 4096 {
					return fmt.Errorf("JSON key exceeds 4 KiB")
				}
				if err = expect(r, ':'); err != nil {
					return err
				}
				name := strings.ReplaceAll(strings.ReplaceAll(string(key), "~", "~0"), "/", "~1")
				if err = walk(path+"/"+name, depth+1); err != nil {
					return err
				}
				if err = skipSpace(r); err != nil {
					return err
				}
				end, err := r.ReadByte()
				if err != nil {
					return err
				}
				if end == '}' {
					return nil
				}
				if end != ',' {
					return fmt.Errorf("expected JSON object separator")
				}
			}
		case '[':
			_, _ = r.ReadByte()
			if err := skipSpace(r); err != nil {
				return err
			}
			b, _ := r.Peek(1)
			if b[0] == ']' {
				_, _ = r.ReadByte()
				return nil
			}
			for i := 0; ; i++ {
				if err := walk(path+"/"+strconv.Itoa(i), depth+1); err != nil {
					return err
				}
				if err := skipSpace(r); err != nil {
					return err
				}
				end, err := r.ReadByte()
				if err != nil {
					return err
				}
				if end == ']' {
					return nil
				}
				if end != ',' {
					return fmt.Errorf("expected JSON array separator")
				}
			}
		case '"':
			q, err := newQuotedReader(r)
			if err != nil {
				return err
			}
			_, err = io.Copy(io.Discard, q)
			return err
		default:
			_, err := jsonValue(r)
			return err
		}
	}
	if err := walk("", 0); err != nil {
		return err
	}
	if err := skipSpace(r); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("trailing JSON content")
	}
	if matches != 1 {
		return fmt.Errorf("JSON pointer not found: %s", pointer)
	}
	return nil
}

func arrayRows(r *bufio.Reader, emit func(any) bool) error {
	if err := expect(r, '['); err != nil {
		return err
	}
	if err := skipSpace(r); err != nil {
		return err
	}
	b, _ := r.Peek(1)
	if b[0] == ']' {
		_, _ = r.ReadByte()
		return nil
	}
	for {
		value, err := jsonValue(r)
		if err != nil {
			return err
		}
		if !emit(value) {
			return errStopped
		}
		if err = skipSpace(r); err != nil {
			return err
		}
		end, err := r.ReadByte()
		if err != nil {
			return err
		}
		if end == ']' {
			return nil
		}
		if end != ',' {
			return fmt.Errorf("expected JSON array separator")
		}
	}
}

// jsonValue frames one value; encoding/json validates its syntax and numbers.
func jsonValue(r *bufio.Reader) (any, error) {
	if err := skipSpace(r); err != nil {
		return nil, err
	}
	first, _ := r.Peek(1)
	compound := first[0] == '{' || first[0] == '['
	quoted := first[0] == '"'
	depth := 0
	inString := false
	escaped := false
	raw := make([]byte, 0, 512)
	for {
		b, err := r.Peek(1)
		if err == io.EOF && !compound && !quoted && len(raw) > 0 {
			break
		}
		if err != nil {
			return nil, err
		}
		c := b[0]
		if !compound && !quoted && strings.ContainsRune(" \t\r\n,]}", rune(c)) {
			break
		}
		_, _ = r.ReadByte()
		raw = append(raw, c)
		if len(raw) > MaxRecordBytes {
			return nil, fmt.Errorf("JSON record exceeds 1 MiB")
		}
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
				if quoted {
					break
				}
			}
			continue
		}
		if c == '"' {
			inString = true
		} else if c == '{' || c == '[' {
			depth++
		} else if c == '}' || c == ']' {
			depth--
			if compound && depth == 0 {
				break
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid JSON record")
	}
	return value, nil
}

type quotedReader struct {
	r       *bufio.Reader
	pending []byte
	done    bool
}

func newQuotedReader(r *bufio.Reader) (*quotedReader, error) {
	if err := expect(r, '"'); err != nil {
		return nil, err
	}
	return &quotedReader{r: r}, nil
}
func (q *quotedReader) hexRune() (rune, error) {
	var b [4]byte
	if _, err := io.ReadFull(q.r, b[:]); err != nil {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := strconv.ParseUint(string(b[:]), 16, 16)
	return rune(n), err
}
func (q *quotedReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(q.pending) > 0 {
			copied := copy(p[n:], q.pending)
			q.pending = q.pending[copied:]
			n += copied
			continue
		}
		if q.done {
			return n, io.EOF
		}
		b, err := q.r.ReadByte()
		if err != nil {
			return n, io.ErrUnexpectedEOF
		}
		if b == '"' {
			q.done = true
			return n, io.EOF
		}
		if b < 0x20 {
			return n, fmt.Errorf("unescaped control character in JSON string")
		}
		if b != '\\' {
			p[n] = b
			n++
			continue
		}
		b, err = q.r.ReadByte()
		if err != nil {
			return n, io.ErrUnexpectedEOF
		}
		switch b {
		case '"', '\\', '/':
			p[n] = b
		case 'b':
			p[n] = '\b'
		case 'f':
			p[n] = '\f'
		case 'n':
			p[n] = '\n'
		case 'r':
			p[n] = '\r'
		case 't':
			p[n] = '\t'
		case 'u':
			value, err := q.hexRune()
			if err != nil {
				return n, err
			}
			if utf16.IsSurrogate(value) {
				if value < 0xD800 || value > 0xDBFF {
					return n, fmt.Errorf("unpaired JSON surrogate")
				}
				var prefix [2]byte
				if _, err = io.ReadFull(q.r, prefix[:]); err != nil || string(prefix[:]) != "\\u" {
					return n, fmt.Errorf("unpaired JSON surrogate")
				}
				low, err := q.hexRune()
				if err != nil || low < 0xDC00 || low > 0xDFFF {
					return n, fmt.Errorf("unpaired JSON surrogate")
				}
				value = utf16.DecodeRune(value, low)
			}
			q.pending = utf8.AppendRune(q.pending, value)
			continue
		default:
			return n, fmt.Errorf("invalid JSON string escape")
		}
		n++
	}
	return n, nil
}
