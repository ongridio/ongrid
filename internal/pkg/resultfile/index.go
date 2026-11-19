package resultfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const MaxIndexEntries = 100000

type Span struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}
type Collection struct {
	Items    []Span `json:"items"`
	Complete bool   `json:"complete"`
}

const (
	maxPathsPerKind   = 128
	PathIndexComplete = "complete"
	PathIndexLimited  = "limited"
)

type Index struct {
	Version         int                   `json:"version"`
	Collections     map[string]Collection `json:"collections,omitempty"`
	TextPointers    []string              `json:"text_pointers,omitempty"`
	ValuePointers   map[string]string     `json:"value_pointers,omitempty"`
	PathIndexStatus string                `json:"path_index_status,omitempty"`
}

type indexBuilder struct {
	Index
	remaining        int
	nativeTextPaths  int
	displayTextPaths int
}

// BuildIndex indexes original byte positions without rewriting the payload.
// Array elements are records, not recursively indexed structures. The MCP
// content array also provides string pointers for its text blocks.
func BuildIndex(body []byte) Index {
	b := indexBuilder{
		Index:     Index{Version: 1, Collections: map[string]Collection{}, ValuePointers: map[string]string{}, PathIndexStatus: PathIndexComplete},
		remaining: MaxIndexEntries,
	}
	if json.Valid(body) {
		b.indexValue(body, 0, "", 0)
	}
	return b.Index
}

func (b *indexBuilder) indexValue(body []byte, base int64, path string, depth int) {
	native := path == "/structuredContent" || strings.HasPrefix(path, "/structuredContent/")
	first := bytes.TrimSpace(body)[0] // BuildIndex validated the complete JSON.
	if len(path) > 1024 || depth > 4 {
		if native || first == '[' || first == '{' {
			b.PathIndexStatus = PathIndexLimited
		}
		return
	}
	if native && first != '[' {
		if first == '"' {
			if b.nativeTextPaths < maxPathsPerKind {
				b.TextPointers = append(b.TextPointers, path)
				b.nativeTextPaths++
			} else {
				b.PathIndexStatus = PathIndexLimited
			}
		} else if len(b.ValuePointers) < maxPathsPerKind {
			kind := "number"
			switch first {
			case '{':
				kind = "object"
			case 't', 'f':
				kind = "boolean"
			case 'n':
				kind = "null"
			}
			b.ValuePointers[path] = kind
		} else {
			b.PathIndexStatus = PathIndexLimited
		}
	}
	if first != '[' && first != '{' {
		return
	}
	d := json.NewDecoder(bytes.NewReader(body))
	if _, err := d.Token(); err != nil {
		return
	}
	if first == '[' {
		b.indexArray(d, base, path)
		return
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return
		}
		name, ok := key.(string)
		if !ok {
			return
		}
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return
		}
		pointer := path + "/" + strings.ReplaceAll(strings.ReplaceAll(name, "~", "~0"), "/", "~1")
		b.indexValue(raw, base+d.InputOffset()-int64(len(raw)), pointer, depth+1)
	}
}

func (b *indexBuilder) indexArray(d *json.Decoder, base int64, path string) {
	if len(b.Collections) >= maxPathsPerKind {
		b.PathIndexStatus = PathIndexLimited
		return
	}
	collection := Collection{Complete: true}
	for d.More() {
		if b.remaining == 0 {
			collection.Complete = false
			if path == "/content" {
				b.PathIndexStatus = PathIndexLimited
			}
			break
		}
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			collection.Complete = false
			break
		}
		end := base + d.InputOffset()
		if path == "/content" && b.displayTextPaths <= maxPathsPerKind {
			var block struct {
				Type string          `json:"type"`
				Text json.RawMessage `json:"text"`
			}
			if json.Unmarshal(raw, &block) == nil && block.Type == "text" && len(block.Text) > 0 && block.Text[0] == '"' {
				if b.displayTextPaths < maxPathsPerKind {
					b.TextPointers = append(b.TextPointers, fmt.Sprintf("/content/%d/text", len(collection.Items)))
				} else {
					b.PathIndexStatus = PathIndexLimited
				}
				b.displayTextPaths++
			}
		}
		collection.Items = append(collection.Items, Span{end - int64(len(raw)), end})
		b.remaining--
	}
	b.Collections[path] = collection
}

func ValidPointer(pointer string) bool {
	if pointer != "" && !strings.HasPrefix(pointer, "/") {
		return false
	}
	for i := 0; i < len(pointer); i++ {
		if pointer[i] == '~' {
			i++
			if i == len(pointer) || (pointer[i] != '0' && pointer[i] != '1') {
				return false
			}
		}
	}
	return true
}

// Pointer selects a JSON field while preserving json.Number precision.
func Pointer(value any, pointer string) (any, bool) {
	if pointer == "" {
		return value, true
	}
	if !ValidPointer(pointer) {
		return nil, false
	}
	for _, part := range strings.Split(pointer[1:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[part]
			if !ok {
				return nil, false
			}
		case []any:
			var n int
			if _, err := fmt.Sscanf(part, "%d", &n); err != nil || fmt.Sprint(n) != part || n < 0 || n >= len(v) {
				return nil, false
			}
			value = v[n]
		default:
			return nil, false
		}
	}
	return value, true
}
