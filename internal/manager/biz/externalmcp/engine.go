package externalmcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/ongridio/ongrid/internal/manager/biz/approval"
	operation "github.com/ongridio/ongrid/internal/manager/model/aiops"
	approvalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/auditctx"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
)

const Source = "external_mcp"
const ApprovalKind = "external_mcp"

var ErrVersionConflict = errors.New("RESOURCE_VERSION_CONFLICT")
var ErrIdempotencyConflict = errors.New("IDEMPOTENCY_CONFLICT")
var ErrAuditUnavailable = errors.New("AUDIT_UNAVAILABLE")
var ErrResultTooLarge = errors.New("RESULT_TOO_LARGE")

type Authenticator interface {
	Authenticate(context.Context, string) (integrationauth.Identity, error)
	Revalidate(context.Context, integrationauth.Identity) (integrationauth.Identity, error)
}
type Auditor interface {
	Record(context.Context, auditctx.Event) error
}
type Store interface {
	Transaction(context.Context, func(context.Context) error) error
	Accept(context.Context, *operation.Operation) (*operation.Operation, bool, error)
	Get(context.Context, string) (*operation.Operation, error)
	Update(context.Context, string, []string, map[string]any) error
	Claim(context.Context, *operation.Operation, Target) (bool, error)
	Release(context.Context, string) error
	Pending(context.Context) ([]*operation.Operation, error)
	Sweep(context.Context, time.Time) error
	LockTarget(context.Context, Target) error
}

type Engine struct {
	Catalog       map[string]*Tool
	Auth          Authenticator
	Audit         Auditor
	Store         Store
	Approvals     *approval.Usecase
	WritesEnabled bool
	BaseURL       string
	Log           *slog.Logger
}

// Result contains only projected resource data. It never contains a token.
type Result struct {
	SchemaVersion string    `json:"schema_version"`
	RequestID     string    `json:"request_id"`
	Status        string    `json:"status,omitempty"`
	ObservedAt    time.Time `json:"observed_at"`
	Source        string    `json:"source"`
	Scope         string    `json:"authorization_scope"`
	Data          any       `json:"data,omitempty"`
	Completeness  string    `json:"completeness,omitempty"`
	Truncated     *bool     `json:"truncated"`
	Warnings      []string  `json:"warnings,omitempty"`
}

type OperationResult struct {
	OperationID  string          `json:"operation_id"`
	State        string          `json:"state"`
	Tool         string          `json:"tool"`
	Applied      *bool           `json:"applied"`
	Verification string          `json:"verification"`
	Data         json.RawMessage `json:"data,omitempty"`
	ApprovalID   string          `json:"approval_id,omitempty"`
	ApprovalURL  string          `json:"approval_url,omitempty"`
	NextAction   string          `json:"next_action,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	Retryable    bool            `json:"retryable"`
}

type storedInput struct {
	Arguments Arguments `json:"arguments"`
	Target    Target    `json:"target"`
}

func (e *Engine) Call(ctx context.Context, identity integrationauth.Identity, name string, raw json.RawMessage) (any, error) {
	started := time.Now()
	requestID := uuid.NewString()
	tool, ok := e.Catalog[name]
	if !ok || !tool.Visible(identity) || (tool.Mutation && !e.WritesEnabled) {
		e.Reject(ctx, identity, "ACCESS_DENIED")
		return nil, errs.ErrForbidden
	}
	args, err := tool.Parse(raw)
	if err != nil {
		e.Reject(ctx, identity, ErrorCode(err))
		return nil, err
	}
	if !tool.Authorize(identity, args) {
		e.Reject(ctx, identity, "ACCESS_DENIED")
		return nil, errs.ErrForbidden
	}
	ctx = integrationauth.With(ctx, identity)
	if tool.Mutation && !Bool(args, "dry_run") {
		return e.submit(ctx, identity, tool, args)
	}
	if tool.ValidateNew != nil {
		if err := tool.ValidateNew(args); err != nil {
			e.Reject(ctx, identity, ErrorCode(err))
			return nil, err
		}
	}
	if err := e.record(ctx, identity, name, requestID, "started", ""); err != nil {
		return nil, ErrAuditUnavailable
	}
	queryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, runErr := tool.Run(queryCtx, args)
	if runErr == nil && tool.Mutation {
		policy, err := tool.Policy(args)
		if err != nil {
			runErr = err
		} else if preview, ok := data.(map[string]any); ok {
			preview["approval_required"] = !policy.LowRisk
		}
	}
	if runErr != nil {
		data = nil
	}
	result := Result{SchemaVersion: "1", RequestID: requestID, Status: "ok", ObservedAt: time.Now().UTC(), Source: "ongrid." + name, Scope: "deployment", Data: data, Completeness: "unknown"}
	annotateResult(&result, data)
	if name == "query_devices" || name == "query_incidents" {
		result.Warnings = append(result.Warnings, "CANDIDATE_WINDOW_LIMITED")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(encoded) > 120*1024 {
		runErr = ErrResultTooLarge
	}
	if _, err := e.Auth.Revalidate(ctx, identity); err != nil {
		return nil, err
	}
	auditCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer done()
	digest := sha256.Sum256(raw)
	if err := e.record(auditCtx, identity, name, requestID, "completed", ErrorCode(runErr), map[string]string{"duration_ms": strconv.FormatInt(time.Since(started).Milliseconds(), 10), "response_bytes": strconv.Itoa(len(encoded)), "arguments_sha256": hex.EncodeToString(digest[:]), "completeness": result.Completeness}); err != nil {
		return nil, ErrAuditUnavailable
	}
	if runErr != nil {
		return nil, runErr
	}
	return result, nil
}

func (e *Engine) record(ctx context.Context, i integrationauth.Identity, tool, id, phase, code string, extra ...map[string]string) error {
	status := auditctx.StatusSuccess
	if code != "" {
		status = auditctx.StatusFailure
	}
	var owner *uint64
	if i.Owner.UserID != 0 {
		owner = &i.Owner.UserID
	}
	payload := map[string]string{"tool": tool, "phase": phase}
	for _, fields := range extra {
		maps.Copy(payload, fields)
	}
	return e.Audit.Record(ctx, auditctx.Event{UserID: owner, UserEmail: i.Owner.Email, Role: i.Owner.Role, Action: auditctx.ActionMCPCall, ResourceType: "external_mcp", ResourceID: i.TokenID, RequestID: id, Status: status, ErrorCode: code, Payload: payload})
}

// Reject never records headers, raw arguments or unverified token identifiers.
func (e *Engine) Reject(ctx context.Context, i integrationauth.Identity, code string) {
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := e.record(auditCtx, i, "", uuid.NewString(), "rejected", code); err != nil {
		e.Log.ErrorContext(auditCtx, "external MCP rejection audit failed", "code", code, "error", err)
	}
}

func (e *Engine) submit(ctx context.Context, identity integrationauth.Identity, tool *Tool, args Arguments) (any, error) {
	if !e.WritesEnabled {
		return nil, errs.ErrForbidden
	}
	policy, err := tool.Policy(args)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(storedInput{Arguments: args, Target: policy.Target})
	if err != nil {
		return nil, err
	}
	maxInput := 48 * 1024
	if tool.Sensitive {
		// Leave room for GCM/base64 in MySQL TEXT.
		maxInput = 44 * 1024
	}
	if len(encoded) > maxInput {
		return nil, errs.ErrInvalid
	}
	digest := sha256.Sum256(append([]byte(tool.Name+":"), encoded...))
	inputJSON := string(encoded)
	if tool.Sensitive {
		if secretbox.KeyIsWeak() {
			return nil, errs.ErrNotWiredYet
		}
		inputJSON, err = secretbox.Encrypt(inputJSON)
		if err != nil {
			return nil, err
		}
	}
	// Hash keys so MySQL's default case-insensitive collation cannot merge them.
	keyHash := sha256.Sum256([]byte(String(args, "idempotency_key")))
	key := hex.EncodeToString(keyHash[:])
	op := &operation.Operation{Source: Source, TokenID: &identity.TokenID, IdempotencyKey: &key, RequestDigest: hex.EncodeToString(digest[:]), CreatedBy: identity.Owner.UserID, Kind: tool.Name, State: operation.OperationStateQueued, Title: tool.Name, Summary: policy.Target.Key, InputJSON: inputJSON, ActionsJSON: "[]"}
	if !policy.LowRisk {
		op.State = operation.OperationStateAwaitingApproval
	}
	var created bool
	err = e.Store.Transaction(ctx, func(tx context.Context) error {
		var err error
		op, created, err = e.Store.Accept(tx, op)
		if err != nil {
			return err
		}
		if !created {
			if op.RequestDigest != hex.EncodeToString(digest[:]) {
				return ErrIdempotencyConflict
			}
			return nil
		}
		if tool.ValidateNew != nil {
			if err := tool.ValidateNew(args); err != nil {
				return err
			}
		}
		if err := e.record(tx, identity, tool.Name, op.ID, "accepted", "", map[string]string{"target": policy.Target.Key, "request_digest": op.RequestDigest, "action": String(args, "action"), "expected_revision": String(args, "expected_revision")}); err != nil {
			return ErrAuditUnavailable
		}
		if policy.LowRisk {
			return nil
		}
		payload := map[string]any{"operation_id": op.ID, "request_digest": op.RequestDigest, "tool": tool.Name, "target": policy.Target, "arguments": args}
		if tool.Sensitive {
			payload["arguments"] = "[ENCRYPTED]"
			payload["encrypted_request"] = inputJSON
		}
		a, err := e.Approvals.Propose(tx, approval.ProposeInput{Kind: ApprovalKind, Title: tool.Name, Summary: policy.Target.Key, Source: Source, ProposedBy: identity.Owner.UserID, Payload: payload})
		if err != nil {
			return err
		}
		op.ApprovalID = a.ID
		return e.Store.Update(tx, op.ID, []string{op.State}, map[string]any{"approval_id": a.ID})
	})
	if err != nil {
		if errors.Is(err, errs.ErrInvalid) {
			e.Reject(ctx, identity, ErrorCode(err))
		}
		return nil, err
	}
	if _, err := e.Auth.Revalidate(ctx, identity); err != nil {
		return nil, err
	}
	if created && policy.LowRisk && !policy.Remote {
		if err := e.execute(ctx, op); err != nil {
			return nil, err
		}
		op, err = e.Store.Get(ctx, op.ID)
		if err != nil {
			return nil, err
		}
	}
	return e.operationResult(op), nil
}

func (e *Engine) owned(ctx context.Context, identity integrationauth.Identity, id string) (*operation.Operation, error) {
	op, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if op.Source != Source || op.CreatedBy != identity.Owner.UserID || op.TokenID == nil || *op.TokenID != identity.TokenID {
		return nil, errs.ErrNotFound
	}
	var in storedInput
	input, decryptErr := secretbox.Decrypt(op.InputJSON)
	if decryptErr != nil || json.Unmarshal([]byte(input), &in) != nil {
		return nil, errs.ErrInvalid
	}
	tool := e.Catalog[op.Kind]
	if tool == nil || !tool.Authorize(identity, in.Arguments) {
		return nil, errs.ErrForbidden
	}
	return op, nil
}

func (e *Engine) GetOperation(ctx context.Context, identity integrationauth.Identity, id string) (any, error) {
	op, err := e.owned(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	return e.operationResult(op), nil
}

func (e *Engine) CancelOperation(ctx context.Context, identity integrationauth.Identity, id string) (any, error) {
	op, err := e.owned(ctx, identity, id)
	if err != nil {
		return nil, err
	}
	if !slices.Contains([]string{operation.OperationStateQueued, operation.OperationStateAwaitingApproval}, op.State) {
		return e.operationResult(op), nil
	}
	err = e.Store.Transaction(ctx, func(tx context.Context) error {
		if err := e.record(tx, identity, op.Kind, op.ID, "cancelled", ""); err != nil {
			return ErrAuditUnavailable
		}
		if err := e.Store.Update(tx, id, []string{operation.OperationStateQueued, operation.OperationStateAwaitingApproval}, map[string]any{"state": operation.OperationStateCancelled, "terminal_at": time.Now().UTC()}); err != nil {
			return err
		}
		if op.ApprovalID != "" {
			return e.Approvals.InvalidatePending(tx, op.ApprovalID, "Operation cancelled by its caller")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	op.State = operation.OperationStateCancelled
	return e.operationResult(op), nil
}

// Approve queues the immutable request after a console administrator confirms it.
func (e *Engine) Approve(ctx context.Context, payload string) (string, error) {
	var in struct {
		OperationID string `json:"operation_id"`
		Digest      string `json:"request_digest"`
	}
	if json.Unmarshal([]byte(payload), &in) != nil {
		return "", errs.ErrInvalid
	}
	op, err := e.Store.Get(ctx, in.OperationID)
	if err != nil {
		return "", err
	}
	if op.Source != Source || op.RequestDigest != in.Digest || op.ApprovalID == "" {
		return "", errs.ErrForbidden
	}
	a, err := e.Approvals.Get(ctx, op.ApprovalID)
	if err != nil {
		return "", err
	}
	if a.Status != approvalmodel.StatusApproved || a.ApprovedBy == nil {
		return "", errs.ErrForbidden
	}
	if !e.WritesEnabled {
		return "", errs.ErrForbidden
	}
	_, args, err := e.executionIdentity(ctx, op)
	if err != nil {
		return "", err
	}
	policy, err := e.Catalog[op.Kind].Policy(args)
	if err != nil {
		return "", err
	}
	queued := e.Store.Update(ctx, op.ID, []string{operation.OperationStateAwaitingApproval}, map[string]any{"state": operation.OperationStateQueued})
	if queued != nil && !errors.Is(queued, errs.ErrConflict) {
		return "", queued
	}
	if queued != nil {
		current, err := e.Store.Get(ctx, op.ID)
		if err != nil {
			return "", err
		}
		if current.State == operation.OperationStateCancelled || current.State == operation.OperationStateAwaitingApproval {
			return "", errs.ErrConflict
		}
	}
	op.State = operation.OperationStateQueued
	if queued == nil && !policy.Remote {
		if err := e.execute(ctx, op); err != nil {
			return "", err
		}
	}
	current, err := e.Store.Get(ctx, op.ID)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(e.operationResult(current))
	return string(result), err
}

func (e *Engine) ApprovalOperation(ctx context.Context, a *approvalmodel.Approval) (OperationResult, error) {
	var payload struct {
		OperationID string `json:"operation_id"`
		Digest      string `json:"request_digest"`
	}
	if a.Kind != ApprovalKind || json.Unmarshal([]byte(a.PayloadJSON), &payload) != nil {
		return OperationResult{}, errs.ErrInvalid
	}
	op, err := e.Store.Get(ctx, payload.OperationID)
	if err != nil {
		return OperationResult{}, err
	}
	if op.Source != Source || op.ApprovalID != a.ID || op.RequestDigest != payload.Digest {
		return OperationResult{}, errs.ErrForbidden
	}
	return e.operationResult(op), nil
}

func (e *Engine) executionIdentity(ctx context.Context, op *operation.Operation) (integrationauth.Identity, Arguments, error) {
	if op.Source != Source || op.TokenID == nil {
		return integrationauth.Identity{}, nil, errs.ErrForbidden
	}
	i, err := e.Auth.Revalidate(ctx, integrationauth.Identity{TokenID: *op.TokenID, Owner: tenantctx.Tenant{UserID: op.CreatedBy}})
	if err != nil {
		return i, nil, err
	}
	var input storedInput
	plain, decryptErr := secretbox.Decrypt(op.InputJSON)
	if decryptErr != nil || json.Unmarshal([]byte(plain), &input) != nil {
		return i, nil, errs.ErrInvalid
	}
	t := e.Catalog[op.Kind]
	if t == nil || !t.Authorize(i, input.Arguments) {
		return i, nil, errs.ErrForbidden
	}
	return i, input.Arguments, nil
}

func (e *Engine) execute(ctx context.Context, op *operation.Operation) error {
	if !e.WritesEnabled {
		return nil
	}
	i, args, err := e.executionIdentity(ctx, op)
	if err != nil {
		encoded, _ := json.Marshal(map[string]string{"error_code": ErrorCode(err)})
		return e.Store.Update(ctx, op.ID, []string{operation.OperationStateQueued}, map[string]any{"state": operation.OperationStateFailed, "result_json": string(encoded), "terminal_at": time.Now().UTC(), "audit_pending": true})
	}
	t := e.Catalog[op.Kind]
	policy, err := t.Policy(args)
	if err != nil {
		return err
	}
	claimed, err := e.Store.Claim(ctx, op, policy.Target)
	if err != nil || !claimed {
		return err
	}
	runCtx, cancel := context.WithTimeout(integrationauth.With(ctx, i), 11*time.Minute)
	defer cancel()
	var data any
	run := func(callCtx context.Context) error {
		if _, err := e.Auth.Revalidate(callCtx, i); err != nil {
			return err
		}
		if err := e.record(callCtx, i, t.Name, op.ID, "executing", ""); err != nil {
			return ErrAuditUnavailable
		}
		if !policy.Remote {
			if err := e.Store.LockTarget(callCtx, policy.Target); err != nil {
				return err
			}
		}
		var err error
		data, err = t.Run(callCtx, args)
		if err != nil {
			return err
		}
		if !policy.Remote {
			if err := e.finish(callCtx, op, operation.OperationStateSucceeded, data, nil); err != nil {
				return err
			}
			return e.completeAudit(callCtx, op)
		}
		return nil
	}
	if policy.Remote {
		err = run(runCtx)
	} else {
		err = e.Store.Transaction(runCtx, run)
	}
	finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	if err != nil {
		state := operation.OperationStateFailed
		if !policy.Remote {
			current, readErr := e.Store.Get(finishCtx, op.ID)
			if readErr != nil {
				return readErr
			}
			if current.State == operation.OperationStateSucceeded || current.State == operation.OperationStatePartial {
				return e.Store.Release(finishCtx, op.ID)
			}
		}
		if policy.Remote && !errors.Is(err, errs.ErrUnauthorized) && !errors.Is(err, errs.ErrForbidden) && !errors.Is(err, ErrAuditUnavailable) && !errors.Is(err, errs.ErrInvalid) {
			state = operation.OperationStateUnknown
		}
		if saveErr := e.finish(finishCtx, op, state, nil, err); saveErr != nil {
			return saveErr
		}
	} else if policy.Remote {
		if saveErr := e.finish(finishCtx, op, operation.OperationStateSucceeded, data, nil); saveErr != nil {
			return saveErr
		}
	}
	if err := e.completeAudit(finishCtx, op); err != nil {
		e.Log.ErrorContext(finishCtx, "external completion audit pending", "operation_id", op.ID, "error", err)
	}
	return e.Store.Release(finishCtx, op.ID)
}

func (e *Engine) finish(ctx context.Context, op *operation.Operation, state string, data any, runErr error) error {
	encoded, err := json.Marshal(map[string]any{"data": data, "error_code": ErrorCode(runErr)})
	if err != nil {
		return err
	}
	if len(encoded) > 120*1024 {
		encoded = []byte(`{"error_code":"RESULT_TOO_LARGE"}`)
		if state == operation.OperationStateSucceeded {
			state = operation.OperationStatePartial
		}
	}
	return e.Store.Update(ctx, op.ID, []string{operation.OperationStateRunning, operation.OperationStateQueued, operation.OperationStateAwaitingApproval}, map[string]any{"state": state, "result_json": string(encoded), "terminal_at": time.Now().UTC(), "lease_until": nil, "audit_pending": true})
}

func (e *Engine) operationResult(op *operation.Operation) OperationResult {
	r := OperationResult{OperationID: op.ID, State: op.State, Tool: op.Kind, Verification: "pending", ApprovalID: op.ApprovalID, NextAction: "get_operation"}
	if op.State == operation.OperationStateSucceeded {
		b := true
		r.Applied = &b
		r.Verification = "read_back"
		r.NextAction = ""
	}
	if op.State == operation.OperationStateFailed || op.State == operation.OperationStateCancelled {
		b := false
		r.Applied = &b
		r.Verification = "not_applied"
		r.NextAction = ""
	}
	if op.State == operation.OperationStateUnknown {
		r.Verification = "unknown"
		r.NextAction = "inspect_target"
	}
	if op.AuditPending {
		r.Data = nil
		r.Verification = "audit_pending"
		r.NextAction = "get_operation"
	}
	if op.State == operation.OperationStatePartial {
		b := true
		r.Applied = &b
		r.Verification = "pending"
	}
	if op.State == operation.OperationStateAwaitingApproval {
		r.NextAction = "open_approval_url"
	}
	if op.ApprovalID != "" {
		r.ApprovalURL = e.BaseURL + "/approvals/" + op.ApprovalID
	}
	if op.ResultJSON != "" {
		var saved struct {
			Data      json.RawMessage `json:"data"`
			ErrorCode string          `json:"error_code"`
		}
		if json.Unmarshal([]byte(op.ResultJSON), &saved) == nil {
			r.Data = saved.Data
			r.ErrorCode = saved.ErrorCode
		}
	}
	if op.AuditPending {
		r.Data = nil
	}
	if op.Kind == "host_restart_service" || op.Kind == "execute_k8s_action" {
		if op.State == operation.OperationStateSucceeded && !op.AuditPending {
			r.Verification = "accepted_by_remote"
		}
	}
	if tool := e.Catalog[op.Kind]; tool != nil && tool.Verification != "" && op.State == operation.OperationStateSucceeded && !op.AuditPending {
		r.Verification = tool.Verification
	}
	return r
}

func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrVersionConflict):
		return "RESOURCE_VERSION_CONFLICT"
	case errors.Is(err, ErrIdempotencyConflict):
		return "IDEMPOTENCY_CONFLICT"
	case errors.Is(err, ErrAuditUnavailable):
		return "AUDIT_UNAVAILABLE"
	case errors.Is(err, ErrResultTooLarge):
		return "RESULT_TOO_LARGE"
	case errors.Is(err, errs.ErrInvalid):
		return "INVALID_ARGUMENT"
	case errors.Is(err, errs.ErrForbidden), errors.Is(err, errs.ErrUnauthorized):
		return "ACCESS_DENIED"
	case errors.Is(err, errs.ErrNotFound):
		return "NOT_FOUND"
	case errors.Is(err, errs.ErrConflict):
		return "CONFLICT"
	case errors.Is(err, context.DeadlineExceeded):
		return "QUERY_TIMEOUT"
	default:
		return "BACKEND_UNAVAILABLE"
	}
}

func (e *Engine) completeAudit(ctx context.Context, op *operation.Operation) error {
	return e.Store.Transaction(ctx, func(tx context.Context) error {
		current, err := e.Store.Get(tx, op.ID)
		if err != nil {
			return err
		}
		if !current.AuditPending {
			return nil
		}
		i := integrationauth.Identity{TokenID: *current.TokenID, Owner: tenantctx.Tenant{UserID: current.CreatedBy}}
		result := e.operationResult(current)
		if err := e.record(tx, i, current.Kind, current.ID, "completed", result.ErrorCode, map[string]string{"state": current.State, "request_digest": current.RequestDigest}); err != nil {
			return ErrAuditUnavailable
		}
		return e.Store.Update(tx, current.ID, []string{current.State}, map[string]any{"audit_pending": false})
	})
}

// reconcile handles durable facts only. A lost execution lease never retries a write.
func (e *Engine) reconcile(ctx context.Context, op *operation.Operation) error {
	if op.AuditPending {
		return e.completeAudit(ctx, op)
	}
	if op.State == operation.OperationStateRunning {
		if err := e.finish(ctx, op, operation.OperationStateUnknown, nil, fmt.Errorf("execution lease expired")); err != nil {
			return err
		}
		return e.Store.Release(ctx, op.ID)
	}
	if op.State == operation.OperationStateAwaitingApproval {
		a, err := e.Approvals.Get(ctx, op.ApprovalID)
		if err != nil {
			return err
		}
		switch a.Status {
		case approvalmodel.StatusApproved:
			_, err = e.Approve(ctx, a.PayloadJSON)
			return err
		case approvalmodel.StatusRejected, approvalmodel.StatusFailed:
			return e.finish(ctx, op, operation.OperationStateCancelled, nil, errs.ErrForbidden)
		default:
			// Rotate untouched pending proposals so one page cannot starve ready work.
			return e.Store.Update(ctx, op.ID, []string{op.State}, map[string]any{"updated_at": time.Now().UTC()})
		}
	}
	return e.execute(ctx, op)
}

func (e *Engine) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var nextSweep time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if time.Now().After(nextSweep) {
				if err := e.Store.Sweep(ctx, time.Now().UTC()); err != nil {
					e.Log.ErrorContext(ctx, "external MCP retention", "error", err)
				}
				nextSweep = time.Now().Add(time.Hour)
			}
			ops, err := e.Store.Pending(ctx)
			if err != nil {
				e.Log.ErrorContext(ctx, "external operations poll", "error", err)
				continue
			}
			for _, op := range ops {
				if err := e.reconcile(ctx, op); err != nil {
					e.Log.ErrorContext(ctx, "external operation recovery", "operation_id", op.ID, "error", err)
				}
			}
		}
	}
}

func (e *Engine) ControlTools() []Tool {
	schema := json.RawMessage(`{"type":"object","properties":{"operation_id":{"type":"string","minLength":36,"maxLength":36}},"required":["operation_id"],"additionalProperties":false}`)
	return []Tool{
		{Name: "get_operation", Description: "Read an operation created by this PAT. Poll this ID after a timeout; unknown operations must not be replayed.", Schema: schema, Run: func(ctx context.Context, a Arguments) (any, error) {
			i, _ := integrationauth.From(ctx)
			return e.GetOperation(ctx, i, String(a, "operation_id"))
		}},
		{Name: "cancel_operation", Description: "Cancel queued or unapproved work owned by this PAT. Running work cannot be cancelled or undone.", Schema: schema, Run: func(ctx context.Context, a Arguments) (any, error) {
			i, _ := integrationauth.From(ctx)
			return e.CancelOperation(ctx, i, String(a, "operation_id"))
		}},
	}
}
