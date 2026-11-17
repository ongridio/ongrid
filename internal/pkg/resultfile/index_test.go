package resultfile

import (
	"encoding/json"
	"testing"
)

func TestIndexPreservesOriginalRecordOffsets(t *testing.T) {
	body := []byte(" {\"items\": [\n {\"id\":9007199254740993,\"name\":\"中文\"}, {\"id\":2}], \"data\":{\"a/b\":[ 3, 4 ]}} ")
	idx := BuildIndex(body)
	items, ok := idx.Collections["/items"]
	if !ok || !items.Complete || len(items.Items) != 2 {
		t.Fatalf("items=%+v", items)
	}
	if string(body[items.Items[0].Start:items.Items[0].End]) != `{"id":9007199254740993,"name":"中文"}` {
		t.Fatal("incorrect original offset")
	}
	if len(idx.Collections["/data/a~1b"].Items) != 2 {
		t.Fatal("nested pointer not escaped")
	}
	v, ok := Pointer(map[string]any{"a/b": []any{json.Number("9007199254740993")}}, "/a~1b/0")
	if !ok || v != json.Number("9007199254740993") {
		t.Fatal("pointer lost number precision")
	}
}

func TestIndexReadsPreviouslySavedLineMetadata(t *testing.T) {
	var idx Index
	err := json.Unmarshal([]byte(`{"version":1,"lines":[{"line":1,"offset":0}],"line_count":2,"collections":{"/items":{"items":[{"start":12,"end":25}],"complete":true}}}`), &idx)
	if err != nil {
		t.Fatal(err)
	}
	items := idx.Collections["/items"]
	if idx.Version != 1 || !items.Complete || len(items.Items) != 1 || items.Items[0] != (Span{Start: 12, End: 25}) {
		t.Fatalf("previously saved index changed: %+v", idx)
	}
}
