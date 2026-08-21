package logs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	bizlogs "github.com/ongridio/ongrid/internal/manager/biz/logs"
	logsstore "github.com/ongridio/ongrid/internal/manager/data/logs/store"
	logsmodel "github.com/ongridio/ongrid/internal/manager/model/logs"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	pkggrafana "github.com/ongridio/ongrid/internal/pkg/grafana"
	"github.com/ongridio/ongrid/internal/pkg/logquery"
)

type mapSecrets map[string]map[string]string

func (m mapSecrets) ResolveFields(_ context.Context, name string) (map[string]string, error) {
	fields := m[name]
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out, nil
}

type managedSecrets struct {
	mu           sync.Mutex
	values       map[string]map[string]string
	creates      []string
	deletes      []string
	failCreateAt int
}

func newManagedSecrets() *managedSecrets {
	return &managedSecrets{values: map[string]map[string]string{}}
}

func (m *managedSecrets) ResolveFields(_ context.Context, name string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fields := m.values[name]
	out := make(map[string]string, len(fields))
	for key, value := range fields {
		out[key] = value
	}
	return out, nil
}

func (m *managedSecrets) CreateManaged(_ context.Context, name, credType, _ string, fields map[string]string) error {
	if credType != "elasticsearch" {
		return errors.New("unexpected credential type")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failCreateAt > 0 && len(m.creates)+1 == m.failCreateAt {
		return errors.New("injected managed credential create failure")
	}
	if _, exists := m.values[name]; exists {
		return errors.New("managed credential already exists")
	}
	stored := make(map[string]string, len(fields))
	for key, value := range fields {
		stored[key] = value
	}
	m.values[name] = stored
	m.creates = append(m.creates, name)
	return nil
}

func (m *managedSecrets) DeleteManaged(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.values[name]; !exists {
		return errs.ErrNotFound
	}
	delete(m.values, name)
	m.deletes = append(m.deletes, name)
	return nil
}

func (m *managedSecrets) apiKey(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[name]["api_key"]
}

func (m *managedSecrets) createCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.creates)
}

func (m *managedSecrets) storedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.values)
}

func (m *managedSecrets) deleteCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.deletes)
}

type mapHostDevices map[uint64]uint64

func (m mapHostDevices) LookupHostDevice(_ context.Context, edgeID uint64) (uint64, error) {
	deviceID := m[edgeID]
	if deviceID == 0 {
		return 0, errs.ErrNotFound
	}
	return deviceID, nil
}

type fixedEdgeInventory []bizlogs.ConnectionEdge

func (i fixedEdgeInventory) ListConnectionEdges(context.Context) ([]bizlogs.ConnectionEdge, error) {
	return append([]bizlogs.ConnectionEdge(nil), i...), nil
}

type histogramCountingSearcher struct {
	mu                sync.Mutex
	countRequests     []logquery.SearchRequest
	histogramRequests []logquery.SearchRequest
	intervals         []time.Duration
	includeEndBucket  bool
}

type failingSaveRepo struct {
	bizlogs.Repo
}

func (failingSaveRepo) SaveBackend(context.Context, *logsmodel.Backend) error {
	return errors.New("injected backend save failure")
}

type echoProbeSearcher struct {
	mu       sync.Mutex
	requests []logquery.SearchRequest
}

func (s *echoProbeSearcher) Search(_ context.Context, req logquery.SearchRequest) (*logquery.SearchResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	message := ""
	if len(req.Keywords.Include) > 0 {
		message = req.Keywords.Include[0]
	}
	return &logquery.SearchResult{Records: []logquery.Record{{ID: "loki-probe", Timestamp: req.End, Message: message, Backend: "loki"}}}, nil
}

func (*echoProbeSearcher) Count(context.Context, logquery.SearchRequest) (uint64, error) {
	return 0, nil
}

func (*echoProbeSearcher) Fields(context.Context, time.Time, time.Time, logquery.Scope) ([]logquery.Field, error) {
	return logquery.AllowedFields(), nil
}

func (*echoProbeSearcher) FieldValues(context.Context, logquery.FieldValuesRequest) ([]string, error) {
	return nil, nil
}

func (*echoProbeSearcher) Histogram(context.Context, logquery.SearchRequest, time.Duration) ([]logquery.HistogramBucket, error) {
	return nil, nil
}

func (s *histogramCountingSearcher) Search(context.Context, logquery.SearchRequest) (*logquery.SearchResult, error) {
	return &logquery.SearchResult{}, nil
}

func (s *histogramCountingSearcher) Count(_ context.Context, req logquery.SearchRequest) (uint64, error) {
	s.mu.Lock()
	s.countRequests = append(s.countRequests, req)
	s.mu.Unlock()
	return 0, errors.New("backend Count must not be used for a histogram")
}

func (s *histogramCountingSearcher) Fields(context.Context, time.Time, time.Time, logquery.Scope) ([]logquery.Field, error) {
	return logquery.AllowedFields(), nil
}

func (s *histogramCountingSearcher) FieldValues(context.Context, logquery.FieldValuesRequest) ([]string, error) {
	return nil, nil
}

func (s *histogramCountingSearcher) Histogram(_ context.Context, req logquery.SearchRequest, interval time.Duration) ([]logquery.HistogramBucket, error) {
	s.mu.Lock()
	s.histogramRequests = append(s.histogramRequests, req)
	s.intervals = append(s.intervals, interval)
	s.mu.Unlock()
	buckets := []logquery.HistogramBucket{
		{Start: req.Start, Count: 120},
		{Start: req.Start.Add(interval), Count: 120},
	}
	if s.includeEndBucket {
		buckets = append(buckets, logquery.HistogramBucket{Start: req.End, Count: 7})
	} else {
		buckets = append(buckets, logquery.HistogramBucket{Start: req.Start.Add(2 * interval), Count: 90})
	}
	return buckets, nil
}

func (s *histogramCountingSearcher) snapshot() ([]logquery.SearchRequest, []logquery.SearchRequest, []time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]logquery.SearchRequest(nil), s.countRequests...),
		append([]logquery.SearchRequest(nil), s.histogramRequests...),
		append([]time.Duration(nil), s.intervals...)
}

type countingNotifier struct {
	mu    sync.Mutex
	calls int
}

func (n *countingNotifier) NotifyLogsBackendChanged(context.Context) error {
	n.mu.Lock()
	n.calls++
	n.mu.Unlock()
	return nil
}

func (n *countingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.calls
}

type recordingGrafanaSyncer struct {
	elasticsearch chan pkggrafana.ElasticsearchDatasourceConfig
	loki          chan struct{}
}

func newRecordingGrafanaSyncer() *recordingGrafanaSyncer {
	return &recordingGrafanaSyncer{
		elasticsearch: make(chan pkggrafana.ElasticsearchDatasourceConfig, 1),
		loki:          make(chan struct{}, 1),
	}
}

func (s *recordingGrafanaSyncer) SyncElasticsearch(_ context.Context, config pkggrafana.ElasticsearchDatasourceConfig) error {
	s.elasticsearch <- config
	return nil
}

func (s *recordingGrafanaSyncer) SyncLoki(context.Context) error {
	s.loki <- struct{}{}
	return nil
}

func TestServiceSelectionSecretsAndIndependentConnectionChecks(t *testing.T) {
	db := openTestDB(t)
	repo := logsstore.NewRepo(db)

	var authMu sync.Mutex
	seenAuth := map[string]int{}
	probePattern := regexp.MustCompile(`ongrid-log-probe-[A-Za-z0-9_-]+`)
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authMu.Lock()
		seenAuth[r.Header.Get("Authorization")]++
		authMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]string{"number": "8.16.3"}})
		case r.Method == http.MethodPost && r.URL.Path == "/_security/user/_has_privileges":
			_ = json.NewEncoder(w).Encode(map[string]any{"has_all_requested": true})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "probe-pit"})
		case r.Method == http.MethodPost && r.URL.Path == "/_search":
			body, _ := io.ReadAll(r.Body)
			deviceID := "9001"
			if strings.Contains(string(body), `"resource.attributes.device_id":["9002"]`) {
				deviceID = "9002"
			} else if !strings.Contains(string(body), `"resource.attributes.device_id":["9001"]`) {
				t.Errorf("probe query does not use resolved device_id: %s", body)
			}
			probeID := probePattern.FindString(string(body))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"pit_id": "probe-pit",
				"hits": map[string]any{"hits": []any{map[string]any{
					"_id": "probe-record", "sort": []any{"2026-08-18T12:00:00Z", 1},
					"_source": map[string]any{
						"@timestamp": "2026-08-18T12:00:00Z", "body": map[string]any{"text": probeID},
						"resource": map[string]any{"attributes": map[string]any{"device_id": deviceID}},
					},
				}}},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
			_ = json.NewEncoder(w).Encode(map[string]any{"succeeded": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer es.Close()

	secrets := mapSecrets{
		"es-write": {"api_key": "write-key"},
		"es-query": {"api_key": "query-key"},
	}
	loki := &echoProbeSearcher{}
	svc := bizlogs.NewService(repo, secrets, loki)
	svc.SetHostDeviceResolver(mapHostDevices{42: 9001, 43: 9002})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{{EdgeID: 42, Online: true}, {EdgeID: 43, Online: true}})
	notifier := &countingNotifier{}
	svc.SetBackendChangeNotifier(notifier)
	grafanaSyncer := newRecordingGrafanaSyncer()
	svc.SetGrafanaSyncer(grafanaSyncer)

	first, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints:     []string{es.URL},
		QueryEndpoint:      es.URL,
		Dataset:            "ongrid.system",
		Namespace:          "prod",
		WriteCredentialRef: "es-write",
		QueryCredentialRef: "es-query",
		TLSInsecure:        true,
	})
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if first.Generation != 1 || first.Status != logsmodel.BackendStatusUnselected {
		t.Fatalf("first saved configuration = generation %d status %q", first.Generation, first.Status)
	}

	tested, err := svc.Test(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if tested.Status != "ok" || tested.DetectedVersion != "8.16.3" || tested.TestedAt.IsZero() {
		t.Fatalf("test result = %+v", tested)
	}
	if notifier.count() != 0 {
		t.Fatalf("notifications after test = %d, want 0", notifier.count())
	}
	persistedAfterTest, err := repo.GetBackend(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("GetBackend after Test: %v", err)
	}
	if persistedAfterTest.Status != logsmodel.BackendStatusUnselected || persistedAfterTest.DetectedVersion != "" || persistedAfterTest.LastTestAt != nil {
		t.Fatalf("test mutated saved backend = %+v", persistedAfterTest)
	}

	selectedView, err := svc.Select(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selectedView.Status != logsmodel.BackendStatusSelected || selectedView.DetectedVersion != "8.16.3" {
		t.Fatalf("selected view = %+v", selectedView)
	}
	if notifier.count() != 1 {
		t.Fatalf("notifications after selection = %d, want 1", notifier.count())
	}
	authMu.Lock()
	if seenAuth["ApiKey query-key"] == 0 || seenAuth["ApiKey write-key"] == 0 {
		t.Fatalf("probe auth headers = %#v", seenAuth)
	}
	authMu.Unlock()
	select {
	case config := <-grafanaSyncer.elasticsearch:
		if config.URL != es.URL || config.IndexPattern != "logs-ongrid.*.otel-prod" || config.APIKey != "query-key" {
			t.Fatalf("Grafana Elasticsearch config = %+v", config)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Grafana Elasticsearch sync was not triggered")
	}
	checking, err := svc.StartConnectionCheck(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("StartConnectionCheck: %v", err)
	}
	if checking.Online != 2 || checking.Pending != 2 || checking.Verified != 0 || checking.AllOnlineVerified {
		t.Fatalf("initial connection check = %+v", checking)
	}
	if notifier.count() != 2 {
		t.Fatalf("notifications after connection check = %d, want 2", notifier.count())
	}
	assignments, err := repo.ListAssignments(context.Background(), first.ID)
	if err != nil || len(assignments) != 2 {
		t.Fatalf("connection check assignments = %+v, %v", assignments, err)
	}
	probeIDs := map[uint64]string{}
	for _, item := range assignments {
		if item.ProbeID == "" {
			t.Fatalf("connection-check assignment has no probe ID: %+v", item)
		}
		probeIDs[item.EdgeID] = item.ProbeID
	}
	overlay, err := svc.PluginRuntimeOverlay(context.Background(), 42, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay: %v", err)
	}
	if overlay["log_probe_id"] != probeIDs[42] {
		t.Fatalf("selected connection-check overlay = %#v", overlay)
	}
	secret, err := svc.PluginSecretForEdge(context.Background(), 42, "logs", bizlogs.SecretSlotESAPIKey, 1)
	if err != nil {
		t.Fatalf("PluginSecretForEdge: %v", err)
	}
	wantHash := sha256.Sum256([]byte("write-key"))
	if secret.Content != "write-key" || secret.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("plugin secret metadata = %+v", secret)
	}
	if err := svc.MarkApplied(context.Background(), 42, 1, probeIDs[42], ""); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}
	partial, err := svc.ConnectionCheck(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("ConnectionCheck(partial): %v", err)
	}
	if partial.Verified != 1 || partial.Pending != 1 || partial.AllOnlineVerified || notifier.count() != 2 {
		t.Fatalf("connection check after first probe = %+v notifications=%d", partial, notifier.count())
	}
	if _, err := svc.PluginSecretForEdge(context.Background(), 43, "logs", bizlogs.SecretSlotESAPIKey, 1); err != nil {
		t.Fatalf("PluginSecretForEdge(edge 43): %v", err)
	}
	if err := svc.MarkApplied(context.Background(), 43, 1, probeIDs[43], ""); err != nil {
		t.Fatalf("MarkApplied(edge 43): %v", err)
	}
	verified, err := svc.ConnectionCheck(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("ConnectionCheck(verified): %v", err)
	}
	if verified.Verified != 2 || verified.Pending != 0 || !verified.AllOnlineVerified || notifier.count() != 2 {
		t.Fatalf("verified connection check = %+v notifications=%d", verified, notifier.count())
	}
	stillSelected, err := repo.GetBackend(context.Background(), first.ID)
	if err != nil || stillSelected.Status != logsmodel.BackendStatusSelected {
		t.Fatalf("connection acknowledgement changed selected backend: %+v, %v", stillSelected, err)
	}

	second, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints:     []string{es.URL},
		QueryEndpoint:      es.URL,
		Dataset:            "ongrid.application",
		Namespace:          "prod",
		WriteCredentialRef: "es-write",
		QueryCredentialRef: "es-query",
		TLSInsecure:        true,
	})
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	if second.ID == first.ID || second.Generation != 2 || second.Status != logsmodel.BackendStatusUnselected {
		t.Fatalf("second saved configuration = id %d generation %d status %q; first id=%d", second.ID, second.Generation, second.Status, first.ID)
	}
	runtime, err := svc.SelectedRuntime(context.Background())
	if err != nil {
		t.Fatalf("SelectedRuntime while editing next revision: %v", err)
	}
	if runtime == nil || runtime.BackendID != first.ID || runtime.Generation != 1 || runtime.Dataset != "ongrid.system" {
		t.Fatalf("selected runtime was overwritten by unselected configuration: %+v", runtime)
	}

	// Re-fetching after a restart must not regress an already-applied row.
	if _, err := svc.PluginSecretForEdge(context.Background(), 42, "logs", bizlogs.SecretSlotESAPIKey, 1); err != nil {
		t.Fatalf("PluginSecretForEdge(second): %v", err)
	}
	assignment, err := repo.GetAssignment(context.Background(), first.ID, 42)
	if err != nil {
		t.Fatalf("GetAssignment: %v", err)
	}
	if assignment.Status != logsmodel.AssignmentStatusVerified || assignment.AppliedGeneration != 1 {
		t.Fatalf("assignment regressed after secret refetch: %+v", assignment)
	}
	if assignment.LastWriteSuccessAt == nil {
		t.Fatalf("verified assignment must record a successful real log write")
	}

	lokiView, err := svc.SelectLoki(context.Background())
	if err != nil {
		t.Fatalf("SelectLoki: %v", err)
	}
	if lokiView.Status != logsmodel.BackendStatusUnselected ||
		lokiView.CurrentBackend != "loki" || notifier.count() != 3 {
		t.Fatalf("Loki selection = %+v notifications %d", lokiView, notifier.count())
	}
	lokiOverlay, err := svc.PluginRuntimeOverlay(context.Background(), 42, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay(Loki): %v", err)
	}
	if lokiOverlay["backend"] != "builtin_loki" {
		t.Fatalf("Loki overlay = %#v", lokiOverlay)
	}
	runtime, err = svc.SelectedRuntime(context.Background())
	if err != nil || runtime != nil {
		t.Fatalf("runtime after Loki selection = %+v, %v", runtime, err)
	}
	select {
	case <-grafanaSyncer.loki:
	case <-time.After(2 * time.Second):
		t.Fatal("Grafana Loki sync was not triggered after selection")
	}
	idempotent, err := svc.SelectLoki(context.Background())
	if err != nil {
		t.Fatalf("SelectLoki(already selected): %v", err)
	}
	if idempotent.Status != logsmodel.BackendStatusUnselected || notifier.count() != 3 {
		t.Fatalf("idempotent Loki selection = %+v notifications=%d", idempotent, notifier.count())
	}
	reselected, err := svc.Select(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("Select(unselected configuration): %v", err)
	}
	if reselected.Status != logsmodel.BackendStatusSelected || reselected.ID != first.ID || reselected.Generation != first.Generation || notifier.count() != 4 {
		t.Fatalf("reselection = %+v notifications=%d", reselected, notifier.count())
	}
}

func TestServiceHistogramUsesOneGlobalExactBucketGrid(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	searcher := &histogramCountingSearcher{}
	svc := bizlogs.NewService(repo, nil, searcher)
	start := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	end := start.Add(5*time.Minute + 30*time.Second)

	buckets, err := svc.Histogram(context.Background(), logquery.SearchRequest{
		Start: start, End: end, Limit: 1,
	}, 2*time.Minute)
	if err != nil {
		t.Fatalf("Histogram: %v", err)
	}
	if len(buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3: %#v", len(buckets), buckets)
	}
	wantCounts := []uint64{120, 120, 90}
	for i, bucket := range buckets {
		wantStart := start.Add(time.Duration(i) * 2 * time.Minute)
		if !bucket.Start.Equal(wantStart) || bucket.Count != wantCounts[i] {
			t.Fatalf("bucket[%d] = %+v, want start=%s count=%d", i, bucket, wantStart, wantCounts[i])
		}
	}
	countRequests, histogramRequests, intervals := searcher.snapshot()
	if len(countRequests) != 0 {
		t.Fatalf("exact count requests = %d, want 0", len(countRequests))
	}
	if len(histogramRequests) != 1 || len(intervals) != 1 || intervals[0] != 2*time.Minute {
		t.Fatalf("native histogram calls = %d intervals=%v, want one 2m call", len(histogramRequests), intervals)
	}
	if histogramRequests[0].Cursor != "" || !histogramRequests[0].Start.Equal(start) || !histogramRequests[0].End.Equal(end) {
		t.Fatalf("native histogram request = %+v", histogramRequests[0])
	}
	if _, err := svc.Histogram(context.Background(), logquery.SearchRequest{
		Start: start, End: start.Add(501 * time.Second), Limit: 1,
	}, time.Second); err == nil || !strings.Contains(err.Error(), "exceeds 500 buckets") {
		t.Fatalf("oversized histogram error = %v", err)
	}
}

func TestServiceHistogramFoldsInclusiveEndBoundaryIntoFinalBucket(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	searcher := &histogramCountingSearcher{includeEndBucket: true}
	svc := bizlogs.NewService(repo, nil, searcher)
	start := time.Date(2026, 8, 21, 2, 30, 55, 0, time.UTC)

	buckets, err := svc.Histogram(context.Background(), logquery.SearchRequest{
		Start: start, End: start.Add(2 * time.Minute), Limit: 1,
	}, time.Minute)
	if err != nil {
		t.Fatalf("Histogram: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2: %#v", len(buckets), buckets)
	}
	if buckets[0].Count != 120 || buckets[1].Count != 127 {
		t.Fatalf("bucket counts = [%d %d], want [120 127]", buckets[0].Count, buckets[1].Count)
	}
}

func TestServiceReplacementSelectionUsesOnlyNewElasticsearch(t *testing.T) {
	db := openTestDB(t)
	repo := logsstore.NewRepo(db)
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_security/user/_has_privileges" {
			_ = json.NewEncoder(w).Encode(map[string]any{"has_all_requested": true})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/" {
			_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]string{"number": "8.16.3"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer es.Close()

	svc := bizlogs.NewService(repo, mapSecrets{
		"write-v1": {"api_key": "write-key-v1"},
		"query-v1": {"api_key": "query-key-v1"},
		"write-v2": {"api_key": "write-key-v2"},
		"query-v2": {"api_key": "query-key-v2"},
	}, nil)
	svc.SetHostDeviceResolver(mapHostDevices{42: 9001})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{{EdgeID: 42, Online: true}})
	first, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{es.URL}, QueryEndpoint: es.URL,
		Dataset: "ongrid.host", Namespace: "old",
		WriteCredentialRef: "write-v1", QueryCredentialRef: "query-v1", TLSInsecure: true,
	})
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := repo.SelectBackend(context.Background(), first.ID, "8.16.3", time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("SelectBackend(first): %v", err)
	}
	second, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{es.URL}, QueryEndpoint: es.URL,
		Dataset: "ongrid.host", Namespace: "new",
		WriteCredentialRef: "write-v2", QueryCredentialRef: "query-v2", TLSInsecure: true,
	})
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	if _, err := svc.Select(context.Background(), second.ID); err != nil {
		t.Fatalf("Select(second): %v", err)
	}
	overlay, err := svc.PluginRuntimeOverlay(context.Background(), 42, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay: %v", err)
	}
	if overlay["backend_generation"] != uint64(2) {
		t.Fatalf("replacement generations = %#v", overlay)
	}
	if overlay["elasticsearch_namespace"] != "new" {
		t.Fatalf("replacement routing = %#v", overlay)
	}
	if _, err := svc.PluginSecretForEdge(context.Background(), 42, "logs", bizlogs.SecretSlotESAPIKey, 2); err != nil {
		t.Fatalf("applying backend secret: %v", err)
	}
	if _, err := svc.PluginSecretForEdge(context.Background(), 42, "logs", bizlogs.SecretSlotESAPIKey, 1); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("old generation secret error = %v, want conflict", err)
	}
}

func TestServiceSelectChecksEveryWriteEndpointWithoutBroadeningWriteKey(t *testing.T) {
	var requestMu sync.Mutex
	newEndpoint := func(requests *[]string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestMu.Lock()
			*requests = append(*requests, r.Header.Get("Authorization")+" "+r.Method+" "+r.URL.Path)
			requestMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/_security/user/_has_privileges":
				_ = json.NewEncoder(w).Encode(map[string]any{"has_all_requested": true})
			case r.Method == http.MethodGet && r.URL.Path == "/":
				_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]string{"number": "8.16.3"}})
			default:
				http.NotFound(w, r)
			}
		}))
	}
	var firstRequests, secondRequests []string
	firstEndpoint := newEndpoint(&firstRequests)
	defer firstEndpoint.Close()
	secondEndpoint := newEndpoint(&secondRequests)
	defer secondEndpoint.Close()

	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, nil)
	svc.SetHostDeviceResolver(mapHostDevices{42: 9001})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{{EdgeID: 42, Online: true}})
	backend, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{firstEndpoint.URL, secondEndpoint.URL}, QueryEndpoint: firstEndpoint.URL,
		Dataset: "ongrid.host", Namespace: "prod",
		WriteCredentialRef: "write", QueryCredentialRef: "query", TLSInsecure: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	applied, err := svc.Select(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if applied.Status != logsmodel.BackendStatusSelected || applied.DetectedVersion != "8.16.3" {
		t.Fatalf("applied backend = %+v", applied)
	}

	requestMu.Lock()
	defer requestMu.Unlock()
	wantQueryProbe := false
	wantWritePrivileges := false
	for _, request := range secondRequests {
		switch request {
		case "ApiKey query-key GET /":
			wantQueryProbe = true
		case "ApiKey write-key POST /_security/user/_has_privileges":
			wantWritePrivileges = true
		case "ApiKey write-key GET /":
			t.Fatalf("runtime write key was used for cluster version probe: %#v", secondRequests)
		}
	}
	if !wantQueryProbe || !wantWritePrivileges {
		t.Fatalf("second write endpoint was not fully checked: %#v", secondRequests)
	}
}

func TestServiceStoresDirectAPIKeysAsManagedWriteOnlyCredentials(t *testing.T) {
	secrets := newManagedSecrets()
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)

	first, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", Namespace: "prod",
		WriteAPIKey: "ApiKey encoded-write-v1", QueryAPIKey: "encoded-query-v1",
	})
	if err != nil {
		t.Fatalf("Save(direct keys): %v", err)
	}
	if first.WriteCredentialRef == first.QueryCredentialRef ||
		!strings.HasPrefix(first.WriteCredentialRef, "ongrid-managed-logs-es-write-") ||
		!strings.HasPrefix(first.QueryCredentialRef, "ongrid-managed-logs-es-query-") {
		t.Fatalf("managed refs = write %q query %q", first.WriteCredentialRef, first.QueryCredentialRef)
	}
	if got := secrets.apiKey(first.WriteCredentialRef); got != "encoded-write-v1" {
		t.Fatalf("stored write key = %q", got)
	}
	if got := secrets.apiKey(first.QueryCredentialRef); got != "encoded-query-v1" {
		t.Fatalf("stored query key = %q", got)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal backend view: %v", err)
	}
	if strings.Contains(string(encoded), "encoded-write-v1") || strings.Contains(string(encoded), "encoded-query-v1") {
		t.Fatalf("backend response leaked direct API key: %s", encoded)
	}

	unchanged, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.application", Namespace: "prod",
	})
	if err != nil {
		t.Fatalf("Save(blank write-only fields): %v", err)
	}
	if unchanged.WriteCredentialRef != first.WriteCredentialRef || unchanged.QueryCredentialRef != first.QueryCredentialRef {
		t.Fatalf("blank write-only fields replaced refs: first=%+v unchanged=%+v", first, unchanged)
	}
	if secrets.createCount() != 2 {
		t.Fatalf("blank write-only fields performed %d creates, want 2 total", secrets.createCount())
	}

	rotated, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.application", Namespace: "prod", WriteAPIKey: "encoded-write-v2",
	})
	if err != nil {
		t.Fatalf("Save(rotate write key): %v", err)
	}
	if rotated.WriteCredentialRef == first.WriteCredentialRef || rotated.QueryCredentialRef != first.QueryCredentialRef {
		t.Fatalf("saved credential rotation did not isolate the new write ref: first=%+v rotated=%+v", first, rotated)
	}
	if got := secrets.apiKey(rotated.WriteCredentialRef); got != "encoded-write-v2" {
		t.Fatalf("rotated write key = %q", got)
	}
	if got := secrets.apiKey(first.WriteCredentialRef); got != "" {
		t.Fatalf("superseded managed write key was retained = %q", got)
	}
	if secrets.storedCount() != 2 || secrets.deleteCount() != 1 {
		t.Fatalf("managed credentials after rotation: stored=%d deleted=%d, want 2/1", secrets.storedCount(), secrets.deleteCount())
	}
}

func TestServiceSavedConfigurationReplacementDoesNotDeleteExternalCredentialRefs(t *testing.T) {
	secrets := newManagedSecrets()
	secrets.values["external-write"] = map[string]string{"api_key": "write-v1"}
	secrets.values["external-query"] = map[string]string{"api_key": "query-v1"}
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)

	first, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteCredentialRef: "external-write", QueryCredentialRef: "external-query",
	})
	if err != nil {
		t.Fatalf("Save(external refs): %v", err)
	}
	rotated, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "write-v2", QueryAPIKey: "query-v2",
	})
	if err != nil {
		t.Fatalf("Save(managed rotation): %v", err)
	}
	if rotated.ID != first.ID {
		t.Fatalf("saved configuration ID changed from %d to %d", first.ID, rotated.ID)
	}
	if secrets.apiKey("external-write") != "write-v1" || secrets.apiKey("external-query") != "query-v1" {
		t.Fatal("external credential refs were deleted during managed rotation")
	}
	if secrets.deleteCount() != 0 {
		t.Fatalf("external rotation deleted %d credentials", secrets.deleteCount())
	}
}

func TestServiceNewGenerationRetainsManagedCredentialsNeededBySelectedBackend(t *testing.T) {
	secrets := newManagedSecrets()
	repo := logsstore.NewRepo(openTestDB(t))
	svc := bizlogs.NewService(repo, secrets, nil)

	first, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "write-v1", QueryAPIKey: "query-v1",
	})
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := repo.SelectBackend(t.Context(), first.ID, "8.16.3", time.Now().UTC()); err != nil {
		t.Fatalf("select first backend: %v", err)
	}
	second, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://next-es.example.com"}, QueryEndpoint: "https://next-es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "write-v2", QueryAPIKey: "query-v2",
	})
	if err != nil {
		t.Fatalf("Save(second generation): %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("second generation = %d, want %d", second.Generation, first.Generation+1)
	}
	if secrets.apiKey(first.WriteCredentialRef) != "write-v1" || secrets.apiKey(first.QueryCredentialRef) != "query-v1" {
		t.Fatal("selected generation credentials were deleted while creating the next saved configuration")
	}
	if secrets.storedCount() != 4 || secrets.deleteCount() != 0 {
		t.Fatalf("managed credentials across generations: stored=%d deleted=%d, want 4/0", secrets.storedCount(), secrets.deleteCount())
	}
}

func TestServiceCanReuseWriteAPIKeyWithoutSharingCredentialReference(t *testing.T) {
	secrets := newManagedSecrets()
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)

	backend, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "encoded-shared", ReuseWriteAPIKey: true,
	})
	if err != nil {
		t.Fatalf("Save(reuse write key): %v", err)
	}
	if backend.WriteCredentialRef == backend.QueryCredentialRef {
		t.Fatalf("reuse mode shared a credential reference: %+v", backend)
	}
	if write, query := secrets.apiKey(backend.WriteCredentialRef), secrets.apiKey(backend.QueryCredentialRef); write != "encoded-shared" || query != write {
		t.Fatalf("stored reuse keys = write %q query %q", write, query)
	}
}

func TestServiceRejectsSharedDirectKeyBeforeWritingManagedCredentials(t *testing.T) {
	secrets := newManagedSecrets()
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)

	_, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "encoded-shared", QueryAPIKey: "encoded-shared",
	})
	if err == nil || !strings.Contains(err.Error(), "reuse_write_api_key") {
		t.Fatalf("Save error = %v, want explicit reuse-mode validation", err)
	}
	if secrets.createCount() != 0 {
		t.Fatalf("rejected request created %d managed credentials", secrets.createCount())
	}
}

func TestServiceDirectAPIKeyRequiresEncryptedManagedStore(t *testing.T) {
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), mapSecrets{}, nil)
	_, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "encoded-write", QueryAPIKey: "encoded-query",
	})
	if err == nil || !strings.Contains(err.Error(), "credential storage is unavailable") {
		t.Fatalf("Save error = %v, want managed storage failure", err)
	}
}

func TestServiceSaveCleansManagedCredentialsWhenCreateFails(t *testing.T) {
	secrets := newManagedSecrets()
	secrets.failCreateAt = 2
	svc := bizlogs.NewService(logsstore.NewRepo(openTestDB(t)), secrets, nil)

	_, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "encoded-write", QueryAPIKey: "encoded-query",
	})
	if err == nil || !strings.Contains(err.Error(), "injected managed credential create failure") {
		t.Fatalf("Save error = %v, want injected create failure", err)
	}
	if secrets.storedCount() != 0 || secrets.deleteCount() != 1 {
		t.Fatalf("managed credentials after failed create: stored=%d deleted=%d", secrets.storedCount(), secrets.deleteCount())
	}
}

func TestServiceSaveCleansManagedCredentialsWhenBackendSaveFails(t *testing.T) {
	secrets := newManagedSecrets()
	repo := failingSaveRepo{Repo: logsstore.NewRepo(openTestDB(t))}
	svc := bizlogs.NewService(repo, secrets, nil)

	_, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteAPIKey: "encoded-write", QueryAPIKey: "encoded-query",
	})
	if err == nil || !strings.Contains(err.Error(), "injected backend save failure") {
		t.Fatalf("Save error = %v, want injected save failure", err)
	}
	if secrets.storedCount() != 0 || secrets.deleteCount() != 2 {
		t.Fatalf("managed credentials after failed save: stored=%d deleted=%d", secrets.storedCount(), secrets.deleteCount())
	}
}

func TestServiceRejectsUnsafeBackendInput(t *testing.T) {
	db := openTestDB(t)
	svc := bizlogs.NewService(logsstore.NewRepo(db), mapSecrets{
		"shared": {"api_key": "key"},
		"query":  {"api_key": "query"},
	}, nil)

	tests := []struct {
		name string
		in   bizlogs.SaveInput
	}{
		{
			name: "plain HTTP without explicit compatibility switch",
			in: bizlogs.SaveInput{WriteEndpoints: []string{"http://es.example"}, QueryEndpoint: "http://es.example",
				Dataset: "ongrid.system", WriteCredentialRef: "shared", QueryCredentialRef: "query"},
		},
		{
			name: "same write and query credential",
			in: bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example"}, QueryEndpoint: "https://es.example",
				Dataset: "ongrid.system", WriteCredentialRef: "shared", QueryCredentialRef: "shared"},
		},
		{
			name: "dataset outside product namespace",
			in: bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example"}, QueryEndpoint: "https://es.example",
				Dataset: "customer-arbitrary", WriteCredentialRef: "shared", QueryCredentialRef: "query"},
		},
		{
			name: "direct API key containing whitespace",
			in: bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example"}, QueryEndpoint: "https://es.example",
				Dataset: "ongrid.system", WriteAPIKey: "encoded write", QueryAPIKey: "encoded-query"},
		},
		{
			name: "query API key supplied in reuse mode",
			in: bizlogs.SaveInput{WriteEndpoints: []string{"https://es.example"}, QueryEndpoint: "https://es.example",
				Dataset: "ongrid.system", WriteAPIKey: "encoded-write", QueryAPIKey: "encoded-query", ReuseWriteAPIKey: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := svc.Save(context.Background(), tt.in); err == nil {
				t.Fatal("Save succeeded, want validation error")
			}
		})
	}
}

func TestServiceSelectGuardBlocksSelection(t *testing.T) {
	db := openTestDB(t)
	svc := bizlogs.NewService(logsstore.NewRepo(db), mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, nil)
	backend, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteCredentialRef: "write", QueryCredentialRef: "query",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc.SetSelectGuard(func(context.Context) error { return errors.New("legacy-log-rule") })
	if _, err := svc.Select(context.Background(), backend.ID); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("Select error = %v, want conflict", err)
	}
	latest, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if latest.Status != logsmodel.BackendStatusUnselected {
		t.Fatalf("status = %q, want unselected", latest.Status)
	}
}

func TestServiceSelectingLokiClearsSelectedElasticsearch(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	svc := bizlogs.NewService(repo, mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, &echoProbeSearcher{})
	input := bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", Namespace: "old", WriteCredentialRef: "write", QueryCredentialRef: "query",
	}
	selected, err := svc.Save(t.Context(), input)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := repo.SelectBackend(t.Context(), selected.ID, "8.16.3", time.Now().UTC()); err != nil {
		t.Fatalf("SelectBackend: %v", err)
	}
	lokiView, err := svc.SelectLoki(t.Context())
	if err != nil {
		t.Fatalf("SelectLoki: %v", err)
	}
	if lokiView.Status != logsmodel.BackendStatusUnselected || lokiView.CurrentBackend != "loki" {
		t.Fatalf("selected Loki view = %+v", lokiView)
	}
	assignments, err := repo.ListAssignments(t.Context(), selected.ID)
	if err != nil || len(assignments) != 0 {
		t.Fatalf("assignments after selecting Loki = %+v, %v", assignments, err)
	}
	runtime, err := svc.SelectedRuntime(t.Context())
	if err != nil || runtime != nil {
		t.Fatalf("selected runtime after choosing Loki = %+v, %v", runtime, err)
	}
}

func TestServiceConnectionCheckIncludesOfflineLogEnabledEdgeWithoutBlockingCutover(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/_security/user/_has_privileges":
			_ = json.NewEncoder(w).Encode(map[string]any{"has_all_requested": true})
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]string{"number": "8.16.3"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer es.Close()
	svc := bizlogs.NewService(repo, mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, nil)
	svc.SetHostDeviceResolver(mapHostDevices{42: 9001})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{
		{EdgeID: 42, Name: "online-edge", Online: true},
		{EdgeID: 43, Name: "offline-edge", Online: false},
	})
	backend, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{es.URL}, QueryEndpoint: es.URL,
		Dataset: "ongrid.system", WriteCredentialRef: "write", QueryCredentialRef: "query", TLSInsecure: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	applied, err := svc.Select(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if applied.Status != logsmodel.BackendStatusSelected {
		t.Fatalf("selection should be immediate and assignment-free: %+v", applied)
	}
	check, err := svc.StartConnectionCheck(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("StartConnectionCheck: %v", err)
	}
	if check.Total != 2 || check.Online != 1 || check.Pending != 1 || check.Offline != 1 || len(check.Edges) != 2 {
		t.Fatalf("connection check = %+v", check)
	}
	if check.Edges[0].EdgeName != "online-edge" || check.Edges[0].Status != bizlogs.ConnectionStatusPending ||
		check.Edges[1].EdgeName != "offline-edge" || check.Edges[1].Status != bizlogs.ConnectionStatusOffline {
		t.Fatalf("connection check edges = %+v", check.Edges)
	}
	firstAssignment, err := repo.GetAssignment(context.Background(), backend.ID, 42)
	if err != nil {
		t.Fatalf("GetAssignment(first check): %v", err)
	}
	if _, err := svc.StartConnectionCheck(context.Background(), backend.ID); err != nil {
		t.Fatalf("StartConnectionCheck(retry): %v", err)
	}
	retriedAssignment, err := repo.GetAssignment(context.Background(), backend.ID, 42)
	if err != nil {
		t.Fatalf("GetAssignment(retry): %v", err)
	}
	if retriedAssignment.ProbeID == "" || retriedAssignment.ProbeID == firstAssignment.ProbeID || retriedAssignment.AppliedGeneration != 0 {
		t.Fatalf("retried assignment did not reset with a fresh probe: first=%+v retry=%+v", firstAssignment, retriedAssignment)
	}
	if err := svc.MarkApplied(context.Background(), 42, backend.Generation, retriedAssignment.ProbeID, "dial Elasticsearch: timeout"); err != nil {
		t.Fatalf("MarkApplied(connection failure): %v", err)
	}
	failed, err := svc.ConnectionCheck(context.Background(), backend.ID)
	if err != nil {
		t.Fatalf("ConnectionCheck(failed): %v", err)
	}
	if failed.Failed != 1 || failed.Offline != 1 || failed.Edges[0].LastError != "dial Elasticsearch: timeout" {
		t.Fatalf("failed connection check = %+v", failed)
	}
	stillSelected, err := repo.GetBackend(context.Background(), backend.ID)
	if err != nil || stillSelected.Status != logsmodel.BackendStatusSelected {
		t.Fatalf("connection failure changed the selected backend: %+v, %v", stillSelected, err)
	}
}

func TestServiceCurrentConnectionCheckVerifiesBuiltinLokiWithoutElasticsearchBackend(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	loki := &echoProbeSearcher{}
	svc := bizlogs.NewService(repo, nil, loki)
	svc.SetHostDeviceResolver(mapHostDevices{42: 9001})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{
		{EdgeID: 42, Name: "online-edge", Online: true},
		{EdgeID: 43, Name: "offline-edge", Online: false},
	})
	notifier := &countingNotifier{}
	svc.SetBackendChangeNotifier(notifier)

	checking, err := svc.StartConnectionCheck(t.Context(), 0)
	if err != nil {
		t.Fatalf("StartConnectionCheck(current Loki): %v", err)
	}
	if checking.BackendID != 0 || checking.Backend != "loki" || checking.Generation == 0 ||
		checking.Online != 1 || checking.Pending != 1 || checking.Verified != 0 || checking.Offline != 1 {
		t.Fatalf("initial Loki connection check = %+v", checking)
	}
	if notifier.count() != 1 {
		t.Fatalf("Loki connection check notifications = %d, want 1", notifier.count())
	}
	overlay, err := svc.PluginRuntimeOverlay(t.Context(), 42, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay(Loki check): %v", err)
	}
	probeID, _ := overlay["log_probe_id"].(string)
	if overlay["backend"] != "builtin_loki" || overlay["backend_generation"] != checking.Generation || probeID == "" {
		t.Fatalf("Loki connection check overlay = %#v", overlay)
	}
	if err := svc.MarkApplied(t.Context(), 42, checking.Generation, probeID, ""); err != nil {
		t.Fatalf("MarkApplied(Loki check): %v", err)
	}
	verified, err := svc.ConnectionCheck(t.Context(), 0)
	if err != nil {
		t.Fatalf("ConnectionCheck(current Loki): %v", err)
	}
	if verified.Verified != 1 || verified.Pending != 0 || verified.Offline != 1 || !verified.AllOnlineVerified {
		t.Fatalf("verified Loki connection check = %+v", verified)
	}

	retried, err := svc.StartConnectionCheck(t.Context(), 0)
	if err != nil {
		t.Fatalf("StartConnectionCheck(retry current Loki): %v", err)
	}
	retryOverlay, err := svc.PluginRuntimeOverlay(t.Context(), 42, "logs")
	if err != nil {
		t.Fatalf("PluginRuntimeOverlay(retry Loki check): %v", err)
	}
	if retried.Generation == checking.Generation || retryOverlay["log_probe_id"] == probeID {
		t.Fatalf("Loki retry did not issue a fresh generation/probe: first=%+v retry=%+v", checking, retried)
	}
}

func TestServiceSelectionWithNoOnlineEdgesUsesManagerProbe(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/_security/user/_has_privileges":
			_ = json.NewEncoder(w).Encode(map[string]any{"has_all_requested": true})
		case r.Method == http.MethodGet && r.URL.Path == "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]string{"number": "8.16.3"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer es.Close()

	svc := bizlogs.NewService(repo, mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, nil)
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{{EdgeID: 43, Online: false}})
	backend, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{es.URL}, QueryEndpoint: es.URL,
		Dataset: "ongrid.system", WriteCredentialRef: "write", QueryCredentialRef: "query", TLSInsecure: true,
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	applied, err := svc.Select(t.Context(), backend.ID)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if applied.Status != logsmodel.BackendStatusSelected || applied.CurrentBackend != string(logsmodel.BackendTypeElasticsearch) {
		t.Fatalf("manager-probed selection = %+v", applied)
	}
}

func TestServiceLokiSelectionWithNoOnlineEdgesDoesNotBlock(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	svc := bizlogs.NewService(repo, mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, &echoProbeSearcher{})
	svc.SetConnectionEdgeInventory(fixedEdgeInventory{{EdgeID: 43, Online: false}})
	backend, err := svc.Save(t.Context(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", WriteCredentialRef: "write", QueryCredentialRef: "query",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := repo.SelectBackend(t.Context(), backend.ID, "8.16.3", time.Now().UTC()); err != nil {
		t.Fatalf("SelectBackend: %v", err)
	}

	lokiView, err := svc.SelectLoki(t.Context())
	if err != nil {
		t.Fatalf("SelectLoki: %v", err)
	}
	if lokiView.Status != logsmodel.BackendStatusUnselected || lokiView.CurrentBackend != "loki" {
		t.Fatalf("offline fleet Loki selection = %+v", lokiView)
	}
}

func TestServiceUnselectedConfigurationKeepsSelectedBackendMetadata(t *testing.T) {
	repo := logsstore.NewRepo(openTestDB(t))
	svc := bizlogs.NewService(repo, mapSecrets{
		"write": {"api_key": "write-key"},
		"query": {"api_key": "query-key"},
	}, nil)
	first, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", Namespace: "old", WriteCredentialRef: "write", QueryCredentialRef: "query",
	})
	if err != nil {
		t.Fatalf("Save(first): %v", err)
	}
	if err := repo.SelectBackend(context.Background(), first.ID, "8.16.3", time.Now().UTC()); err != nil {
		t.Fatalf("SelectBackend(first): %v", err)
	}
	second, err := svc.Save(context.Background(), bizlogs.SaveInput{
		WriteEndpoints: []string{"https://es.example.com"}, QueryEndpoint: "https://es.example.com",
		Dataset: "ongrid.system", Namespace: "new", WriteCredentialRef: "write", QueryCredentialRef: "query",
	})
	if err != nil {
		t.Fatalf("Save(second): %v", err)
	}
	if second.CurrentBackend != "elasticsearch" || second.CurrentBackendID != first.ID {
		t.Fatalf("Save(second) current backend = %q #%d, want elasticsearch #%d", second.CurrentBackend, second.CurrentBackendID, first.ID)
	}
	visible, err := svc.Get(context.Background())
	if err != nil || visible.ID != second.ID || visible.Status != logsmodel.BackendStatusUnselected || visible.CurrentBackend != "elasticsearch" || visible.CurrentBackendID != first.ID {
		t.Fatalf("Get with an unselected configuration = %+v, %v", visible, err)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := logsstore.Migrate(db); err != nil {
		t.Fatalf("migrate logs store: %v", err)
	}
	return db
}
