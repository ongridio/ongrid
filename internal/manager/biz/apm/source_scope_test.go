package apm

import (
	"context"
	"encoding/json"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/tracequery"
	"strings"
	"testing"
)

type sourceTrace struct {
	TraceQuerier
	body json.RawMessage
}

func (s sourceTrace) GetTrace(context.Context, string) (*tracequery.TraceResult, error) {
	return &tracequery.TraceResult{Body: s.body}, nil
}

type sourceRefs map[string]string

func (s sourceRefs) ResolveSourceRevision(_ context.Context, _, ref string) (string, error) {
	if value, ok := s[ref]; ok {
		return value, nil
	}
	return "", errs.ErrNotFound
}
func TestAPMSourceResolvesTraceAndFailsClosed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct{ name, version, build, tag, service, wantError string }{
		{"tag", "v1", "", sha, "orders", ""},
		{"build", "v1", sha, sha, "orders", ""},
		{"build without tag", "v1", sha, "", "orders", ""},
		{"missing version", "", "", "", "orders", "missing"},
		{"missing tag", "v1", "", "", "orders", "revision"},
		{"tag conflict", "v1", sha, strings.Repeat("b", 40), "orders", "conflicts"},
		{"invalid build", "v1", "HEAD", sha, "orders", "invalid"},
		{"wrong service", "v1", sha, sha, "other", "no failing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := []map[string]any{}
			for key, value := range map[string]string{"service.name": tc.service, "service.namespace": "trade", "deployment.environment.name": "prod", "service.version": tc.version, "vcs.ref.head.revision": tc.build} {
				attrs = append(attrs, map[string]any{"key": key, "value": map[string]string{"stringValue": value}})
			}
			span := map[string]any{"kind": 2, "status": map[string]int{"code": 2}}
			resource := map[string]any{"resource": map[string]any{"attributes": attrs}, "scopeSpans": []any{map[string]any{"spans": []any{span}}}}
			body, err := json.Marshal(map[string]any{"resourceSpans": []any{resource}})
			if err != nil {
				t.Fatal(err)
			}
			svc := New(nil, sourceTrace{body: body}, nil).WithRepositoryBindings(bindingMemory{}, bindingRepos{1: {ID: 1, URL: "ssh://git@example/demo.git"}})
			refs := sourceRefs{sha: sha}
			if tc.tag != "" {
				refs["refs/tags/v1"] = tc.tag
			}
			svc.WithSourceRevisions(refs)
			id := Identity{ServiceName: "orders", ServiceNamespace: "trade", Environment: "prod"}
			if _, err := svc.PutRepositoryBinding(context.Background(), RepositoryBinding{Identity: id, RepoID: 1, SourceDirectory: "services/orders", TagPattern: "{version}"}); err != nil {
				t.Fatal(err)
			}
			got := svc.ResolveAPMSource(context.Background(), model.APMSourceTarget{TraceID: strings.Repeat("1", 32), ServiceName: "orders", ServiceNamespace: "trade", Environment: "prod"})
			if tc.wantError != "" {
				if !strings.Contains(got.Error, tc.wantError) {
					t.Fatalf("scope %+v", got)
				}
				return
			}
			if got.Error != "" || got.CommitSHA != sha || got.RepoID != "1" || got.SourceDirectory != "services/orders" {
				t.Fatalf("scope %+v", got)
			}
			for _, target := range []model.APMSourceTarget{
				{TraceID: strings.Repeat("1", 32), ServiceName: "orders", ServiceNamespace: "trade", Environment: "prod", ServiceVersion: "wrong-version"},
				{TraceID: strings.Repeat("1", 32), ServiceName: "orders", ServiceNamespace: "trade", Environment: "prod", InstanceID: "wrong-instance"},
			} {
				if denied := svc.ResolveAPMSource(context.Background(), target); denied.Error == "" {
					t.Fatalf("accepted mismatched selection: %+v", target)
				}
			}
		})
	}
}
