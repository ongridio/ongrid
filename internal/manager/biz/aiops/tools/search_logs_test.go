package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

type fakeStructuredLogSearcher struct {
	request logquery.SearchRequest
}

func (f *fakeStructuredLogSearcher) Search(_ context.Context, req logquery.SearchRequest) (*logquery.SearchResult, error) {
	f.request = req
	return &logquery.SearchResult{Records: []logquery.Record{{ID: "1", Timestamp: req.End, Message: "connection refused", Backend: "elasticsearch"}}, Backends: []string{"elasticsearch"}}, nil
}

func (*fakeStructuredLogSearcher) Count(context.Context, logquery.SearchRequest) (uint64, error) {
	return 0, nil
}

func (*fakeStructuredLogSearcher) Fields(context.Context, time.Time, time.Time, logquery.Scope) ([]logquery.Field, error) {
	return logquery.AllowedFields(), nil
}

func (*fakeStructuredLogSearcher) FieldValues(context.Context, logquery.FieldValuesRequest) ([]string, error) {
	return nil, nil
}

func (*fakeStructuredLogSearcher) Histogram(context.Context, logquery.SearchRequest, time.Duration) ([]logquery.HistogramBucket, error) {
	return nil, nil
}

func TestSearchLogsRegisteredAndBackendNeutral(t *testing.T) {
	searcher := &fakeStructuredLogSearcher{}
	registry := NewRegistry(nil, nil, nil, nil, nil, nil, nil, nil)
	registry.SetLogSearcher(searcher)
	if !containsName(schemaNames(registry.Schemas()), ToolNameSearchLogs) {
		t.Fatalf("%s not registered", ToolNameSearchLogs)
	}
	if !containsToolName(t, registry.BuildBaseTools().AllTools(), ToolNameSearchLogs) {
		t.Fatalf("%s base tool not registered", ToolNameSearchLogs)
	}
	out, err := registry.Invoke(context.Background(), ToolNameSearchLogs, json.RawMessage(`{
		"keywords":["connection refused"],"match_mode":"phrase","device_ids":[42],
		"nodes":["worker-a"],"units":["kubelet.service"],
		"levels":["ERROR"],"start":"now-15m","limit":25
	}`))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if searcher.request.Keywords.Mode != logquery.MatchPhrase || len(searcher.request.Scope.DeviceIDs) != 1 || searcher.request.Scope.DeviceIDs[0] != 42 ||
		len(searcher.request.Scope.Nodes) != 1 || searcher.request.Scope.Nodes[0] != "worker-a" ||
		len(searcher.request.Scope.Units) != 1 || searcher.request.Scope.Units[0] != "kubelet.service" ||
		len(searcher.request.Scope.Levels) != 1 || searcher.request.Scope.Levels[0] != "ERROR" || searcher.request.Limit != 25 {
		t.Fatalf("request = %+v", searcher.request)
	}
	var result logquery.SearchResult
	if err := json.Unmarshal(out.ResultJSON, &result); err != nil || len(result.Records) != 1 || result.Records[0].Backend != "elasticsearch" {
		t.Fatalf("result = %s err=%v", out.ResultJSON, err)
	}
}

func TestSearchLogsPhraseRequiresOneTerm(t *testing.T) {
	tool := NewSearchLogsTool(&fakeStructuredLogSearcher{}, nil)
	if _, err := tool.InvokableRun(context.Background(), `{"keywords":["one","two"],"match_mode":"phrase"}`); err == nil {
		t.Fatal("phrase search succeeded with two terms")
	}
}
