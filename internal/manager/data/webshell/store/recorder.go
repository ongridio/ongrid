package store

import (
	"context"
	"time"

	wsmodel "github.com/ongridio/ongrid/internal/manager/model/webshell"
)

// Recorder wraps the GORM repo behind the narrow
// Recorder surface biz/webshell expects.
type Recorder struct {
	repo *Repo
}

func (a Recorder) Open(ctx context.Context, s *wsmodel.Session) error {
	return a.repo.Insert(ctx, s)
}

func (a Recorder) SetHostFingerprint(ctx context.Context, sessionID, fingerprint string) error {
	return a.repo.SetHostFingerprint(ctx, sessionID, fingerprint)
}

func (a Recorder) Close(ctx context.Context, sessionID string, endedAt time.Time, bytesIn, bytesOut uint64, exitCode int, terminatedBy string) error {
	return a.repo.Close(ctx, sessionID, CloseInput{
		EndedAt:      endedAt,
		BytesStdin:   bytesIn,
		BytesStdout:  bytesOut,
		ExitCode:     exitCode,
		TerminatedBy: terminatedBy,
	})
}

func (a Recorder) List(ctx context.Context, limit int) ([]*wsmodel.Session, error) {
	return a.repo.List(ctx, limit)
}

func NewRecorder(repo *Repo) Recorder { return Recorder{repo: repo} }
