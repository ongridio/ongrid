package toolresult

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	session "github.com/ongridio/ongrid/internal/manager/model/aiops"
	model "github.com/ongridio/ongrid/internal/manager/model/toolresult"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/resultfile"
	"github.com/ongridio/ongrid/internal/pkg/toolcapture"
)

// Byte counting is deliberately conservative without a provider tokenizer.
const ViewLimit = 4000
const ScanLimit = 8 << 20

type Repo interface {
	CheckWritable(context.Context) error
	Save(context.Context, *model.Result, []byte, []model.Reference) error
	Get(context.Context, string) (*model.Result, error)
	FindExecution(context.Context, string) (*model.Result, error)
	Open(context.Context, *model.Result) (io.ReadSeekCloser, error)
	Index(context.Context, *model.Result) (resultfile.Index, error)
	AddReference(context.Context, model.Reference) error
}
type Sessions interface {
	GetSession(context.Context, string) (*session.Session, error)
}
type Scope struct {
	SessionID string
	UserID    uint64
}
type Usecase struct {
	captureEnabled bool
	repo           Repo
	sessions       Sessions
	key            []byte
	log            *slog.Logger
	analyzer       Analyzer
	analysisSlots  chan struct{}
}

func New(repo Repo, sessions Sessions, signingKey []byte, log *slog.Logger) *Usecase {
	if log == nil {
		log = slog.Default()
	}
	return &Usecase{captureEnabled: true, repo: repo, sessions: sessions, key: append([]byte(nil), signingKey...), log: log}
}

func (u *Usecase) Preflight(ctx context.Context, scope Scope) error {
	if _, err := u.root(ctx, scope); err != nil {
		return err
	}
	return u.repo.CheckWritable(ctx)
}

func (u *Usecase) root(ctx context.Context, scope Scope) (string, error) {
	if scope.SessionID == "" {
		return "", errs.ErrForbidden
	}
	id := scope.SessionID
	for i := 0; i < 32; i++ {
		s, err := u.sessions.GetSession(ctx, id)
		if err != nil {
			return "", err
		}
		if s.UserID != scope.UserID {
			return "", errs.ErrForbidden
		}
		if s.ParentSessionID == nil || *s.ParentSessionID == "" {
			return s.ID, nil
		}
		id = *s.ParentSessionID
	}
	return "", errs.ErrForbidden
}

func (u *Usecase) Metadata(ctx context.Context, scope Scope, id string) (*model.Result, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, errs.ErrInvalid
	}
	root, err := u.root(ctx, scope)
	if err != nil {
		return nil, err
	}
	r, err := u.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.UserID != scope.UserID {
		return nil, errs.ErrNotFound
	}
	owner, err := u.root(ctx, Scope{r.SessionID, r.UserID})
	if err != nil || owner != root {
		return nil, errs.ErrNotFound
	}
	return r, nil
}

func (u *Usecase) Open(ctx context.Context, scope Scope, id string) (*model.Result, io.ReadSeekCloser, error) {
	r, err := u.Metadata(ctx, scope, id)
	if err != nil {
		return nil, nil, err
	}
	f, err := u.repo.Open(ctx, r)
	return r, f, err
}

type View struct {
	Kind            string `json:"kind"`
	ResultID        string `json:"result_id,omitempty"`
	Status          string `json:"status"`
	ExecutionStatus string `json:"execution_status"`
	CaptureStatus   string `json:"capture_status"`
	StorageStatus   string `json:"storage_status"`
	ViewStatus      string `json:"view_status"`
	CapturedBytes   int64  `json:"captured_bytes"`
	Preview         string `json:"preview,omitempty"`
	Instruction     string `json:"instruction,omitempty"`
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
func encodeView(v View) string {
	for {
		b, err := json.Marshal(v)
		if err != nil {
			return `{"status":"failed","error":"result view encoding failed"}`
		}
		if len(b) <= ViewLimit || len(v.Preview) == 0 {
			return string(b)
		}
		v.Preview = prefix(v.Preview, len(v.Preview)/2)
	}
}

// Capture runs after execution. Storage failures are data, never a retryable
// tool error: an external write may already have succeeded.
func (u *Usecase) Capture(ctx context.Context, scope Scope, executionID, tool, output string, runErr error, refs []model.Reference) string {
	raw, linked, partial := toolcapture.Get(ctx)
	if linked != "" {
		return output
	} // server-linked approval result; reads still reauthorize its ID
	status, _ := basetool.ResultOutcome(output)
	if runErr != nil {
		status = "error"
	}
	if raw == nil {
		raw = []byte(output)
		if runErr != nil {
			b, err := json.Marshal(struct {
				Output string `json:"output,omitempty"`
				Error  string `json:"error"`
			}{output, runErr.Error()})
			if err != nil {
				return encodeView(View{Kind: "tool_result", Status: status, ExecutionStatus: status, CaptureStatus: "failed", StorageStatus: "failed", ViewStatus: "unavailable"})
			}
			raw = b
		}
	}
	executionStatus := status
	if partial && runErr != nil {
		executionStatus = "unknown"
	}
	row := &model.Result{ID: uuid.NewString(), SessionID: scope.SessionID, UserID: scope.UserID, ToolName: tool, ExecutionStatus: executionStatus, CaptureStatus: "complete", StorageStatus: "ready", ContentType: "text/plain", ExpectedBytes: int64(len(raw)), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour)}
	key := sha256.Sum256([]byte(scope.SessionID + "\x00" + executionID))
	row.ExecutionKey = hex.EncodeToString(key[:])
	if json.Valid(raw) {
		row.ContentType = "application/json"
	}
	if partial || len(raw) > resultfile.MaxSize {
		row.CaptureStatus = "partial"
		row.CaptureReason = "source_or_capture_limit"
	}
	if len(raw) > resultfile.MaxSize {
		raw = raw[:resultfile.MaxSize]
	}
	// Preserve evidence even if the originating request was cancelled.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err := u.repo.Save(writeCtx, row, raw, refs)
	instruction := "Use query_tool_result to select evidence, or read_tool_result for a bounded page. Preview is only an excerpt; do not re-run the original tool to retrieve more data."
	if u.analyzer != nil && u.captureEnabled {
		instruction += " For whole-dataset questions, custom calculations, joins or nested formats, use analyze_tool_results to evaluate jq expressions over the complete saved inputs."
	}
	v := View{Kind: "tool_result", ResultID: row.ID, Status: status, ExecutionStatus: executionStatus, CaptureStatus: row.CaptureStatus, StorageStatus: "ready", ViewStatus: "excerpt", CapturedBytes: int64(len(raw)), Preview: prefix(string(raw), 2048), Instruction: instruction}
	if err != nil {
		u.log.Error("tool result persistence failed", slog.String("result_id", row.ID), slog.Any("err", err))
		v.StorageStatus = "failed"
		if errors.Is(err, model.ErrPending) {
			v.StorageStatus = "pending"
			toolcapture.Link(ctx, row.ID)
		}
		v.Instruction = "Execution has already finished. Result storage failed or awaits recovery. Do not repeat the original action."
		return encodeView(v)
	}
	toolcapture.Link(ctx, row.ID)
	// Keep control envelopes and small legacy results byte-for-byte intact.
	if ((len(output) <= ViewLimit && len(raw) <= ViewLimit) || tool == "draft_config_change") && runErr == nil && row.CaptureStatus == "complete" {
		return output
	}
	return encodeView(v)
}

type cursor struct {
	ID     string `json:"i"`
	Hash   string `json:"h"`
	Query  string `json:"q"`
	Offset int64  `json:"o"`
	Expiry int64  `json:"e"`
}

func (u *Usecase) sign(c cursor) string {
	b, _ := json.Marshal(c) // fixed scalar fields cannot fail to marshal
	m := hmac.New(sha256.New, u.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, m.Sum(nil)...))
}
func (u *Usecase) resume(value string, row *model.Result, fingerprint string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) > 2048 {
		return 0, errs.ErrInvalid
	}
	b, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(b) < sha256.Size {
		return 0, errs.ErrInvalid
	}
	body, sig := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	m := hmac.New(sha256.New, u.key)
	m.Write(body)
	var c cursor
	if !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(body, &c) != nil || c.ID != row.ID || c.Hash != row.SHA256 || c.Query != fingerprint || c.Offset < 0 || time.Now().Unix() > c.Expiry {
		return 0, errs.ErrInvalid
	}
	return c.Offset, nil
}
func (u *Usecase) continuation(row *model.Result, fingerprint string, offset int64) string {
	return u.sign(cursor{row.ID, row.SHA256, fingerprint, offset, time.Now().Add(time.Hour).Unix()})
}

// ConfirmedReference uses a server-created association, never a result_id
// parsed out of an untrusted tool response.
func (u *Usecase) Reference(ctx context.Context, ref model.Reference) error {
	if ref.ResultID == "" {
		return fmt.Errorf("%w: missing result", errs.ErrInvalid)
	}
	return u.repo.AddReference(ctx, ref)
}

// SetCaptureEnabled is called only during startup; existing evidence remains readable.
func (u *Usecase) SetCaptureEnabled(enabled bool) { u.captureEnabled = enabled }
func (u *Usecase) CaptureEnabled() bool           { return u.captureEnabled }
