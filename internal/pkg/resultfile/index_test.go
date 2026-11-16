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
	if idx.LineCount != 2 {
		t.Fatal("wrong line count")
	}
	v, ok := Pointer(map[string]any{"a/b": []any{json.Number("9007199254740993")}}, "/a~1b/0")
	if !ok || v != json.Number("9007199254740993") {
		t.Fatal("pointer lost number precision")
	}
}
