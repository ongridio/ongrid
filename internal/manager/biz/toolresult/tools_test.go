package toolresult

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
)

type budgetSessions struct{}

func (budgetSessions) GetSession(context.Context, string) (*session.Session, error) {
	return &session.Session{ID: "session", UserID: 7}, nil
}

type blockingResultRepo struct {
	Repo
	row              *model.Result
	entered, release chan struct{}
	opens            atomic.Int32
}

func (r *blockingResultRepo) Get(context.Context, string) (*model.Result, error) { return r.row, nil }
func (r *blockingResultRepo) Open(context.Context, *model.Result) (io.ReadSeekCloser, error) {
	if r.opens.Add(1) != 1 {
		return nil, errors.New("unexpected concurrent result read")
	}
	close(r.entered)
	<-r.release
	return &budgetReadCloser{Reader: strings.NewReader("saved evidence")}, nil
}

type budgetReadCloser struct{ *strings.Reader }

func (*budgetReadCloser) Close() error { return nil }

func TestConcurrentReadersReserveReturnedBytesAndHonorCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		id := uuid.NewString()
		hash := sha256.Sum256([]byte("saved evidence"))
		repo := &blockingResultRepo{row: &model.Result{ID: id, SessionID: "session", UserID: 7, StorageStatus: "ready", CaptureStatus: "complete", CapturedBytes: 14, SHA256: hex.EncodeToString(hash[:])}, entered: make(chan struct{}), release: make(chan struct{})}
		u := New(repo, budgetSessions{}, []byte("test"), nil)
		reader := &readerTool{u: u, scope: Scope{SessionID: "session", UserID: 7}, budget: &readBudget{returned: 28000}}
		args := `{"result_id":"` + id + `"}`
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := reader.InvokableRun(ctx, args); done <- err }()
		<-repo.entered
		_, err := reader.InvokableRun(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "budget exhausted") {
			t.Errorf("concurrent return budget=%v", err)
		}
		if cancelRequest {
			cancel()
		}
		close(repo.release)
		err = <-done
		cancel()
		if cancelRequest && !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
		if !cancelRequest && err != nil {
			t.Fatal(err)
		}
	}
}
