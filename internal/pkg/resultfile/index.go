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
type Index struct {
	Version      int                   `json:"version"`
	Collections  map[string]Collection `json:"collections,omitempty"`
	TextPointers []string              `json:"text_pointers,omitempty"`
}

// BuildIndex indexes original byte positions without rewriting the payload.
// Nested objects up to depth four are supported; arrays inside array elements
// remain available through byte reads instead of creating an unbounded index.
func BuildIndex(body []byte) Index {
	idx := Index{Version: 1, Collections: map[string]Collection{}}
	if !json.Valid(body) {
		return idx
	}
	remaining := MaxIndexEntries
	indexCollections(body, 0, "", 0, &remaining, &idx)
	return idx
}

func indexCollections(body []byte, base int64, path string, depth int, remaining *int, idx *Index) {
	if len(path) > 1024 || depth > 4 || len(idx.Collections) >= 128 {
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '"' && (path == "/structuredContent" || strings.HasPrefix(path, "/structuredContent/")) && len(idx.TextPointers) < 128 {
		idx.TextPointers = append(idx.TextPointers, path)
	}
	if len(trimmed) == 0 || (trimmed[0] != '[' && trimmed[0] != '{') {
		return
	}
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil {
		return
	}
	if start == json.Delim('[') {
		collection := Collection{Complete: true}
		for d.More() {
			if *remaining <= 0 {
				collection.Complete = false
				break
			}
			var raw json.RawMessage
			if err := d.Decode(&raw); err != nil {
				collection.Complete = false
				break
			}
			end := base + d.InputOffset()
			if path == "/content" && len(collection.Items) < 12 && len(idx.TextPointers) < 128 {
				var block struct {
					Type string          `json:"type"`
					Text json.RawMessage `json:"text"`
				}
				if json.Unmarshal(raw, &block) == nil && block.Type == "text" && len(block.Text) > 0 && block.Text[0] == '"' {
					idx.TextPointers = append(idx.TextPointers, fmt.Sprintf("/content/%d/text", len(collection.Items)))
				}
			}
			collection.Items = append(collection.Items, Span{end - int64(len(raw)), end})
			*remaining--
		}
		idx.Collections[path] = collection
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
		indexCollections(raw, base+d.InputOffset()-int64(len(raw)), pointer, depth+1, remaining, idx)
	}
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
