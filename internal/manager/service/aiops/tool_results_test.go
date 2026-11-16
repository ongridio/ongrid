package aiops

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	model "github.com/ongridio/ongrid/internal/manager/model/aiops"
	resultmodel "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

type authorizedResultRepo struct {
	toolresult.Repo
	row *resultmodel.Result
}

func (r authorizedResultRepo) Get(context.Context, string) (*resultmodel.Result, error) {
	return r.row, nil
}

func TestSavedResultsCheckSessionOwnerBeforeResultAccess(t *testing.T) {
	id := uuid.NewString()
	sessions := &memSessions{sessions: map[string]*model.Session{
		"owned":     {ID: "owned", UserID: 7},
		"unrelated": {ID: "unrelated", UserID: 7},
	}}
	results := toolresult.New(authorizedResultRepo{row: &resultmodel.Result{ID: id, SessionID: "owned", UserID: 7}}, sessions, []byte("test"), nil)
	s := &Service{sessions: sessions, results: results}
	ctx := context.Background()
	for _, action := range []string{"metadata", "read", "download"} {
		var err error
		switch action {
		case "metadata":
			_, err = s.GetToolResult(ctx, Caller{UserID: 8}, "owned", id)
		case "read":
			_, err = s.ReadToolResult(ctx, Caller{UserID: 8}, "owned", toolresult.Request{ResultID: id}, false)
		case "download":
			_, _, err = s.DownloadToolResult(ctx, Caller{UserID: 8}, "owned", id)
		}
		if !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("%s authorization=%v", action, err)
		}
	}
	for _, caller := range []Caller{{UserID: 7}, {UserID: 1, Role: RoleAdmin}} {
		if _, err := s.GetToolResult(ctx, caller, "owned", id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetToolResult(ctx, caller, "unrelated", id); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("result crossed session trees: %v", err)
		}
	}
}
