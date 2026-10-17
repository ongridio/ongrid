package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	model "github.com/ongridio/ongrid/internal/manager/model/mcp"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/mcpclient"
)

func TestDiscoveryCannotRestoreDisabledOrDeletedServer(t *testing.T) {
	for _, action := range []string{"disable", "delete", "replace", "refresh"} {
		t.Run(action, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			first := true // httptest requests are sequential until the first tools/list blocks.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := `{"protocolVersion":"` + mcpclient.ProtocolVersion + `"}`
				if req.Method == "tools/list" {
					if first {
						first = false
						close(entered)
						<-release
					}
					result = `{"tools":[{"name":"search","inputSchema":{"type":"object"}}]}`
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": json.RawMessage(result)}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			uc := NewUsecase(newFakeRepo(), fakeSecrets{}, nil)
			row, err := uc.Create(ctx, &model.Server{Name: "docs", Endpoint: server.URL, Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			published := 0
			uc.SetToolChangeHook(func(_ context.Context, _ string, s *model.Server, _ []mcpclient.Tool) {
				if s != nil {
					published++
				}
			})
			done := make(chan error, 1)
			go func() { _, err := uc.TestConnection(ctx, row.ID); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			switch action {
			case "delete":
				err = uc.Delete(ctx, row.ID)
			case "refresh":
				_, err = uc.TestConnection(ctx, row.ID)
			default:
				patch := *row
				if action == "disable" {
					patch.Enabled = false
				} else {
					patch.Endpoint = server.URL + "/new"
				}
				err = uc.Update(ctx, row.ID, &patch)
			}
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, errs.ErrConflict) {
				t.Fatalf("stale discovery: %v", err)
			}
			want := 0
			if action == "refresh" {
				want = 1
			}
			if published != want {
				t.Fatalf("published %d catalogs, want %d", published, want)
			}
		})
	}
}

func TestCallToolRejectsDisabledServerBeforeConnecting(t *testing.T) {
	uc := NewUsecase(newFakeRepo(), fakeSecrets{}, nil)
	if _, err := uc.Create(context.Background(), &model.Server{Name: "disabled", Endpoint: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := uc.CallTool(context.Background(), "disabled", "write", nil); !errors.Is(err, errs.ErrForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestApprovalRevisionRejectsChangedEndpointAndCredential(t *testing.T) {
	for _, change := range []string{"endpoint", "credential", "schema", "permission"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			repo := newFakeRepo()
			secrets := fakeSecrets{fields: map[string]string{"token": "first"}}
			uc := NewUsecase(repo, secrets, nil)
			row, err := uc.Create(ctx, &model.Server{Name: "test", Endpoint: "http://127.0.0.1:1", Credential: "test", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			revision, err := uc.Revision(ctx, row.Name)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "endpoint":
				row.Endpoint += "/replacement"
				err = uc.Update(ctx, row.ID, row)
			case "permission":
				row.Trusted = true
				err = uc.Update(ctx, row.ID, row)
			case "schema":
				err = repo.SetToolsCache(ctx, row.ID, `[{"name":"different"}]`)
			case "credential":
				secrets.fields["token"] = "rotated"
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := uc.CallToolRevision(ctx, row.Name, "write", nil, revision); !errors.Is(err, errs.ErrConflict) {
				t.Fatalf("expected revision conflict before connection, got %v", err)
			}
		})
	}
}
