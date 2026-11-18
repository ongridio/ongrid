package resultanalysis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const testID = "00000000-0000-4000-8000-000000000001"

func request(source Source, body, expression string) Request {
	source.ID = testID
	return Request{Expression: expression, Inputs: []Input{{Source: source, Bytes: int64(len(body)), Content: strings.NewReader(body)}}}
}
func runTest(t *testing.T, req Request) (output, error) {
	t.Helper()
	var out bytes.Buffer
	err := evaluate(context.Background(), req, &out)
	var result output
	if err == nil {
		if e := json.Unmarshal(out.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
	}
	return result, err
}
func TestStreamingFormatsAndExactNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, body, format, pointer, collection, expression, want string
		rows                                                      int64
	}{
		{"object", `{"structuredContent":{"content":{"id":9007199254740993}}}`, "json_value", "/structuredContent/content", "", `[inputs.value.id]`, `[[9007199254740993]]`, 1},
		{"scalar", `null`, "json_value", "", "", `[inputs.value]`, `[[null]]`, 1},
		{"integer", `[{"n":18446744073709551615}]`, "json", "", "", `reduce inputs as $r (0; .+$r.value.n+1)`, `[18446744073709551616]`, 1},
		{"native", `{"note":"ignored","data":{"items":[{"n":9007199254740993},{"n":2}]},"tail":[]}`, "json", "/data/items", "", `reduce inputs as $r (0; .+$r.value.n)`, `[9007199254740995]`, 2},
		{"structured_text", `{"content":[{"text":"large header"}],"structuredContent":{"content":"{\"a/b~\":[0,2]}"}}`, "json_text", "/structuredContent/content", "/a~1b~0", `[inputs.value]`, `[[0,2]]`, 2},
		{"wrapped", `{"content":[{"text":"{\"items\":[{\"n\":9007199254740993},{\"n\":2}]}"}]}`, "json_text", "/content/0/text", "/items", `reduce inputs as $r (0; .+$r.value.n)`, `[9007199254740995]`, 2},
		{"csv", "id,note,cost\r\n9007199254740993,\"a, \"\"b\"\"\nnext\",23\r\n2,plain,7\r\n", "csv", "", "", `reduce inputs as $r ({count:0,sum:0,max_id:0}; .count+=1|.sum+=($r.value.cost|tonumber)|.max_id=([.max_id,($r.value.id|tonumber)]|max))`, `[{"count":2,"max_id":9007199254740993,"sum":30}]`, 2},
		{"text", "error\nhealthy\nerror", "lines", "", "", `reduce inputs as $r (0; if $r.value=="error" then .+1 else . end)`, `[2]`, 3},
		{"escaped_csv", `{"content":[{"text":"id,note\n1,\uD83D\uDE00\n"}]}`, "csv", "/content/0/text", "", `[inputs.value.id]`, `[["1"]]`, 1},
		{"empty", `{"rows":[]}`, "json", "/rows", "", `reduce inputs as $r (0; .+1)`, `[0]`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runTest(t, request(Source{Format: tc.format, Pointer: tc.pointer, CollectionPointer: tc.collection}, tc.body, tc.expression))
			if err != nil || string(result.Values) != tc.want {
				t.Fatalf("got %s %v; want %s", result.Values, err, tc.want)
			}
			if len(result.Coverage) != 1 || !result.Coverage[0].Complete || result.Coverage[0].RecordsRead != tc.rows {
				t.Fatalf("wrong coverage: %+v", result.Coverage)
			}
		})
	}
}
func TestStreamingRejectsMalformedAndOversizedData(t *testing.T) {
	for _, tc := range []struct{ format, pointer, collection, body string }{
		{"json_value", "/value", "", `{"value":"` + strings.Repeat("x", MaxRecordBytes) + `"}`},
		{"json_value", "/missing", "", `{"value":0}`},
		{"json", "", "", `[1,]`}, {"json", "", "", `[1] true`}, {"json", "", "", `[1`},
		{"json", "/rows", "", `{"rows":[1],"rows":[2]}`}, {"json", "/rows", "", `{"other":[1]}`},
		{"json_text", "/text", "", `{"text":"[1,]"}`}, {"json_text", "/text", "", `{"text":"[1]\uD800"}`},
		{"json", "", "", `["` + strings.Repeat("x", MaxRecordBytes) + `"]`},
		{"csv", "", "", "a,a\n1,2"}, {"csv", "", "", "a,b\n1"},
		{"csv", "", "", "a\n\"" + strings.Repeat("a\n", MaxRecordBytes) + "\""},
		{"lines", "", "", strings.Repeat("a", MaxRecordBytes+1)},
	} {
		_, err := runTest(t, request(Source{Format: tc.format, Pointer: tc.pointer, CollectionPointer: tc.collection}, tc.body, `[inputs]`))
		if err == nil {
			t.Fatalf("invalid %s input accepted (bytes %d)", tc.format, len(tc.body))
		}
	}
	// jq try must not hide a malformed source and turn it into successful evidence.
	_, err := runTest(t, request(Source{Format: "json"}, `[1,]`, `try [inputs] catch "ignored"`))
	if err == nil {
		t.Fatal("source error hidden by expression")
	}
}
func TestPartialCoverageAndCapabilities(t *testing.T) {
	t.Setenv("ANALYSIS_TEST_SECRET", "must-not-leak")
	for _, expression := range []string{`env`, `$ENV`} {
		got, err := runTest(t, request(Source{Format: "json"}, `[1,2]`, expression))
		if err != nil || string(got.Values) != `[{}]` || got.Coverage[0].Complete {
			t.Fatalf("environment/coverage: %+v %v", got, err)
		}
	}
	got, err := runTest(t, request(Source{Format: "json"}, `[1,2,3]`, `first(inputs).value`))
	if err != nil || string(got.Values) != `[1]` || got.Coverage[0].RecordsRead != 1 || got.Coverage[0].Complete {
		t.Fatalf("partial coverage: %+v %v", got, err)
	}
	for _, expr := range []string{`import "/etc/passwd" as p; p`, `"x"*65536`, `range(100000)`} {
		_, err := runTest(t, request(Source{Format: "json"}, `[]`, expr))
		if err == nil {
			t.Fatal("invalid/unbounded expression accepted")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = evaluate(ctx, request(Source{Format: "json"}, `[]`, `def f:f;f`), io.Discard)
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("recursive expression not canceled: %v", err)
	}
	_, err = runTest(t, request(Source{Format: "json"}, `[]`, `"x"*65536`))
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatal(err)
	}
}
func TestRequestRejectsInvalidSources(t *testing.T) {
	for _, source := range []Source{{ID: testID, Format: "bad"}, {ID: "../secret", Format: "json"}, {ID: testID, Format: "json", Pointer: "/bad~"}, {ID: testID, Format: "json", CollectionPointer: "/extra"}} {
		if err := (Request{Expression: ".", Inputs: []Input{{Source: source}}}).Validate(); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	r := request(Source{Format: "json"}, `[]`, ".")
	r.Inputs = append(r.Inputs, r.Inputs[0])
	if r.Validate() == nil {
		t.Fatal("duplicate input accepted")
	}
	r = request(Source{Format: "json"}, `[]`, ".")
	r.Inputs[0].Bytes = MaxInputBytes + 1
	if r.Validate() == nil {
		t.Fatal("oversized input accepted")
	}
}
func TestQuotedReaderChunkBoundaries(t *testing.T) {
	raw := `"a\"b\\c\/\b\f\n\r\t\u4e2d\ud83d\ude00"`
	var want string
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2, 3, 4, 7} {
		q, err := newQuotedReader(bufio.NewReader(strings.NewReader(raw)))
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		p := make([]byte, n)
		for {
			n, err := q.Read(p)
			got.Write(p[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if got.String() != want {
			t.Fatalf("wrong unescape: %q", got.String())
		}
	}
}

// A first-record query must not preload a large native or escaped collection.
func TestStreamDoesNotPreloadCollection(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		body := "[" + strings.Repeat(`{"n":1},`, 100000) + `{"n":2}]`
		source := Source{Format: "json"}
		if wrapped {
			b, _ := json.Marshal(map[string]string{"text": body})
			body = string(b)
			source = Source{Format: "json_text", Pointer: "/text"}
		}
		req := request(source, body, `first(inputs).value.n`)
		counted := &countingReader{Reader: req.Inputs[0].Content}
		req.Inputs[0].Content = counted
		result, err := runTest(t, req)
		if err != nil || string(result.Values) != `[1]` || counted.n > 16384 {
			t.Fatalf("preloaded source: read=%d bytes, err=%v", counted.n, err)
		}
	}
}

type countingReader struct {
	io.Reader
	n int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += n
	return n, err
}

func TestCSVRecordSizeBoundary(t *testing.T) {
	for _, n := range []int{MaxRecordBytes, MaxRecordBytes + 1} {
		req := request(Source{Format: "csv"}, "value\n"+strings.Repeat("x", n), `reduce inputs as $r (0; .+1)`)
		_, err := runTest(t, req)
		if (err == nil) != (n == MaxRecordBytes) {
			t.Fatalf("record bytes=%d error=%v", n, err)
		}
	}
}
