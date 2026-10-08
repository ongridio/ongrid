package logquery

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validSearchRequest() SearchRequest {
	end := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	return SearchRequest{Start: end.Add(-time.Hour), End: end}
}

func TestSearchRequestNormalizeAndValidate_Defaults(t *testing.T) {
	req := validSearchRequest()
	if err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() error = %v", err)
	}
	if req.Limit != DefaultSearchLimit {
		t.Fatalf("Limit = %d, want %d", req.Limit, DefaultSearchLimit)
	}
	if req.Direction != SortBackward {
		t.Fatalf("Direction = %q, want %q", req.Direction, SortBackward)
	}
	if req.Keywords.Mode != MatchAny {
		t.Fatalf("Match mode = %q, want %q", req.Keywords.Mode, MatchAny)
	}
}

func TestSearchRequestNormalizeAndValidate_RejectsUnsafeInputs(t *testing.T) {
	cases := []struct {
		name string
		edit func(*SearchRequest)
		want string
	}{
		{"wide_window", func(r *SearchRequest) { r.Start = r.End.Add(-31 * 24 * time.Hour) }, "time window"},
		{"zero_device", func(r *SearchRequest) { r.Scope.DeviceIDs = []uint64{0} }, "device_id"},
		{"too_many_devices", func(r *SearchRequest) {
			r.Scope.DeviceIDs = make([]uint64, MaxScopeValueCount+1)
			for i := range r.Scope.DeviceIDs {
				r.Scope.DeviceIDs[i] = uint64(i + 1)
			}
		}, "too many values"},
		{"unknown_field", func(r *SearchRequest) {
			r.Filters = []FieldFilter{{Field: "_index", Operator: FilterEqual, Values: []string{"secret"}}}
		}, "not allowed"},
		{"severity_is_not_a_field", func(r *SearchRequest) {
			r.Filters = []FieldFilter{{Field: "severity", Operator: FilterEqual, Values: []string{"ERROR"}}}
		}, "not allowed"},
		{"bad_limit", func(r *SearchRequest) { r.Limit = MaxSearchLimit + 1 }, "limit"},
		{"equal_requires_one_value", func(r *SearchRequest) {
			r.Filters = []FieldFilter{{Field: "service_name", Operator: FilterEqual, Values: []string{"api", "worker"}}}
		}, "exactly one"},
		{"bad_cursor", func(r *SearchRequest) { r.Cursor = "not-base64***" }, "invalid cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validSearchRequest()
			tc.edit(&req)
			err := req.NormalizeAndValidate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestSearchRequestNormalizeAndValidate_DeduplicatesDeviceIDs(t *testing.T) {
	req := validSearchRequest()
	req.Scope.DeviceIDs = []uint64{42, 7, 42, 7, 99}
	if err := req.NormalizeAndValidate(); err != nil {
		t.Fatalf("NormalizeAndValidate() error = %v", err)
	}
	want := []uint64{42, 7, 99}
	if len(req.Scope.DeviceIDs) != len(want) {
		t.Fatalf("DeviceIDs = %v, want %v", req.Scope.DeviceIDs, want)
	}
	for i := range want {
		if req.Scope.DeviceIDs[i] != want[i] {
			t.Fatalf("DeviceIDs = %v, want %v", req.Scope.DeviceIDs, want)
		}
	}
}

func TestFilePathValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"long_path", "/var/log/" + strings.Repeat("service/", 80) + "application.log", ""},
		{"at_limit", "/" + strings.Repeat("a", 4095), ""},
		{"over_limit", "/" + strings.Repeat("a", 4096), "exceeds 4096 bytes"},
		{"multibyte_over_limit", "/" + strings.Repeat("服", 1366), "exceeds 4096 bytes"},
		{"empty", " ", "must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, entry := range []string{"search_scope", "field_values_scope", "field_filter"} {
				t.Run(entry, func(t *testing.T) {
					req := validSearchRequest()
					req.Scope.Files = []string{tc.path}
					var err error
					switch entry {
					case "field_values_scope":
						values := FieldValuesRequest{Field: "file", Start: req.Start, End: req.End, Scope: req.Scope}
						err = values.NormalizeAndValidate()
					case "field_filter":
						req.Scope.Files = nil
						req.Filters = []FieldFilter{{Field: "file", Operator: FilterEqual, Values: []string{tc.path}}}
						err = req.NormalizeAndValidate()
					default:
						err = req.NormalizeAndValidate()
					}
					if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
						t.Fatalf("error = %v, want %q", err, tc.want)
					}
				})
			}
		})
	}

	for _, tc := range []struct {
		name  string
		scope Scope
		want  string
	}{
		{"source_limit_unchanged", Scope{SourceIDs: []string{strings.Repeat("a", 257)}}, "exceeds 256 bytes"},
		{"file_count_limit_unchanged", Scope{Files: make([]string, MaxScopeValueCount+1)}, "too many values"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validSearchRequest()
			req.Scope = tc.scope
			if err := req.NormalizeAndValidate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLongFilePathQueriesAcrossBackends(t *testing.T) {
	path := "/var/log/" + strings.Repeat("service/", 80) + `应用-"quoted"-\worker.log`
	for _, entry := range []string{"scope", "filter"} {
		t.Run(entry, func(t *testing.T) {
			req := validSearchRequest()
			if entry == "scope" {
				req.Scope.Files = []string{path}
			} else {
				req.Filters = []FieldFilter{{Field: "file", Operator: FilterEqual, Values: []string{path}}}
			}
			if err := req.NormalizeAndValidate(); err != nil {
				t.Fatal(err)
			}
			quoted, err := json.Marshal(path)
			if err != nil {
				t.Fatal(err)
			}
			loki, err := compileLogQL(req)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(loki, "| filename="+string(quoted)) {
				t.Fatalf("Loki query did not preserve the full escaped path: %s", loki)
			}
			es, err := buildElasticsearchQuery(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(es)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), `"resource.attributes.filename"`) || !strings.Contains(string(body), string(quoted)) {
				t.Fatalf("Elasticsearch query did not preserve the full escaped path: %s", body)
			}
		})
	}
}

func TestAllowedFields_DoNotExposeElasticsearchIndexControls(t *testing.T) {
	names := map[string]bool{}
	for _, field := range AllowedFields() {
		names[field.Name] = true
		if strings.HasPrefix(field.Name, "_") || field.Name == "elasticsearch.index" {
			t.Fatalf("unsafe field exposed: %q", field.Name)
		}
	}
	if !names["level"] || names["severity"] {
		t.Fatalf("allowed fields = %#v, want level and no severity", names)
	}
}

func TestNormalizeGroupByKeepsPortableDimensionsOnly(t *testing.T) {
	got, err := NormalizeGroupBy([]string{" device_id ", "source_id", "namespace"})
	if err != nil {
		t.Fatalf("NormalizeGroupBy() error = %v", err)
	}
	want := []string{"device_id", "source_id", "namespace"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("NormalizeGroupBy() = %v, want %v", got, want)
	}

	for _, tc := range []struct {
		name   string
		fields []string
	}{
		{name: "unsupported", fields: []string{"pod"}},
		{name: "duplicate", fields: []string{"device_id", "device_id"}},
		{name: "too_many", fields: []string{"device_id", "cluster_id", "source_id", "namespace", "service_name", "pod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NormalizeGroupBy(tc.fields); err == nil {
				t.Fatalf("NormalizeGroupBy(%v) unexpectedly succeeded", tc.fields)
			}
		})
	}
}

func TestAPMIdentityFiltersAcrossBackends(t *testing.T) {
	req := validSearchRequest()
	req.Filters = []FieldFilter{{Field: "service_version", Operator: FilterEqual, Values: []string{"v2"}}, {Field: "instance_id", Operator: FilterEqual, Values: []string{"orders-2"}}, {Field: "service_namespace", Operator: FilterEqual, Values: []string{"trade"}}, {Field: "environment", Operator: FilterEqual, Values: []string{""}}, {Field: "trace_id", Operator: FilterEqual, Values: []string{"abc123"}}}
	if err := req.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	loki, err := compileLogQL(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`service_version="v2"`, `service_instance_id="orders-2"`, `service_namespace="trade"`, `deployment_environment_name=""`, `trace_id="abc123"`} {
		if !strings.Contains(loki, want) {
			t.Fatalf("missing %s: %s", want, loki)
		}
	}
	es, err := buildElasticsearchQuery(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(es)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`resource.attributes.service.version`, `resource.attributes.service.instance.id`, `resource.attributes.service.namespace`, `resource.attributes.deployment.environment.name`, `minimum_should_match`, `must_not`, `trace_id`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("missing %s: %s", want, body)
		}
	}
	req.Filters = []FieldFilter{{Field: "trace_id", Operator: FilterEqual, Values: []string{""}}}
	if err := req.NormalizeAndValidate(); err == nil {
		t.Fatal("empty trace id accepted")
	}
}
