package aiops

import (
	"context"
	"io"

	"github.com/ongridio/ongrid/internal/manager/biz/toolresult"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func (s *Service) SetToolResults(results *toolresult.Usecase) { s.results = results }
func (s *Service) resultScope(ctx context.Context, caller Caller, sessionID string) (toolresult.Scope, error) {
	if s.results == nil {
		return toolresult.Scope{}, errs.ErrNotWiredYet
	}
	row, err := s.GetSession(ctx, caller, sessionID)
	if err != nil {
		return toolresult.Scope{}, err
	}
	return toolresult.Scope{SessionID: row.ID, UserID: row.UserID}, nil
}
func (s *Service) GetToolResult(ctx context.Context, caller Caller, sessionID, id string) (*model.Result, error) {
	scope, err := s.resultScope(ctx, caller, sessionID)
	if err != nil {
		return nil, err
	}
	return s.results.Metadata(ctx, scope, id)
}
func (s *Service) ReadToolResult(ctx context.Context, caller Caller, sessionID string, req toolresult.Request, query bool) (*toolresult.Page, error) {
	scope, err := s.resultScope(ctx, caller, sessionID)
	if err != nil {
		return nil, err
	}
	return s.results.Read(ctx, scope, req, query)
}
func (s *Service) DownloadToolResult(ctx context.Context, caller Caller, sessionID, id string) (*model.Result, io.ReadSeekCloser, error) {
	scope, err := s.resultScope(ctx, caller, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return s.results.Open(ctx, scope, id)
}
