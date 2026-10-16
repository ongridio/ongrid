package aiops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	managerbizpacketcapture "github.com/ongridio/ongrid/internal/manager/biz/packetcapture"
	manageraiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	managerpacketcapturemodel "github.com/ongridio/ongrid/internal/manager/model/packetcapture"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

func PacketCaptureOperationCreator(operationUC *managerbizaiops.OperationUsecase) func(context.Context, aiopstools.PacketCaptureOperationInput) (aiopstools.PacketCaptureOperation, error) {
	return func(ctx context.Context, in aiopstools.PacketCaptureOperationInput) (aiopstools.PacketCaptureOperation, error) {
		op, err := operationUC.Create(ctx, managerbizaiops.CreateOperationInput{
			ChatSessionID: in.ChatSessionID,
			CreatedBy:     in.CreatedBy,
			Kind:          "packet_capture_session",
			Title:         in.Title,
			Summary:       fmt.Sprintf("%d capture member(s) are being collected", in.MemberCount),
			Input:         map[string]any{"packet_capture_session_id": in.SessionID},
			Actions:       []managerbizaiops.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: true}},
			DetailURL:     "/artifacts/packet-sessions/" + in.SessionID,
		})
		if err != nil {
			return aiopstools.PacketCaptureOperation{}, err
		}
		if err := operationUC.Transition(ctx, op.ID, []string{manageraiopsmodel.OperationStateCreated}, manageraiopsmodel.OperationStateRunning, op.Summary, []managerbizaiops.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: true}}, "created", map[string]any{"packet_capture_session_id": in.SessionID}); err != nil {
			return aiopstools.PacketCaptureOperation{}, err
		}
		return aiopstools.PacketCaptureOperation{ID: op.ID, State: manageraiopsmodel.OperationStateRunning, Summary: op.Summary}, nil
	}
}

func PacketCaptureOperationAction(operationUC *managerbizaiops.OperationUsecase, packetCaptureUC *managerbizpacketcapture.Usecase) func(context.Context, *manageraiopsmodel.Operation, string) (*manageraiopsmodel.Operation, error) {
	return func(ctx context.Context, operation *manageraiopsmodel.Operation, action string) (*manageraiopsmodel.Operation, error) {
		if operation == nil || action != "cancel" || operation.Kind != "packet_capture_session" {
			return nil, errs.ErrNotFound
		}
		var input struct {
			SessionID string `json:"packet_capture_session_id"`
		}
		if err := json.Unmarshal([]byte(operation.InputJSON), &input); err != nil {
			return nil, fmt.Errorf("decode operation input: %w", err)
		}
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, errs.ErrInvalid
		}
		actions := []managerbizaiops.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: false}}
		if err := operationUC.Transition(ctx, operation.ID, []string{manageraiopsmodel.OperationStateCreated, manageraiopsmodel.OperationStateQueued, manageraiopsmodel.OperationStateRunning}, manageraiopsmodel.OperationStateCanceling, "Cancellation requested", actions, "cancel_requested", map[string]any{"packet_capture_session_id": input.SessionID}); err != nil && !errors.Is(err, errs.ErrConflict) {
			return nil, err
		}
		detail, err := packetCaptureUC.CancelSession(ctx, input.SessionID)
		if err != nil {
			return nil, err
		}
		nextState := manageraiopsmodel.OperationStateCancelled
		summary := "Capture session stopped"
		nextActions := []managerbizaiops.OperationAction(nil)
		eventType := "cancelled"
		if detail != nil && detail.Session != nil {
			switch detail.Session.State {
			case managerpacketcapturemodel.SessionStateReady:
				nextState = manageraiopsmodel.OperationStateSucceeded
				summary = fmt.Sprintf("%d/%d capture artifact(s) available", detail.Analysis.Summary.ReadyCount, detail.Analysis.Summary.CaptureCount)
				eventType = "succeeded_after_cancel"
			case managerpacketcapturemodel.SessionStatePartial:
				nextState = manageraiopsmodel.OperationStateSucceeded
				summary = fmt.Sprintf("Capture session partially complete; %d/%d artifact(s) available", detail.Analysis.Summary.ReadyCount, detail.Analysis.Summary.CaptureCount)
				eventType = "partial_after_cancel"
			case managerpacketcapturemodel.SessionStateCollecting:
				nextState = manageraiopsmodel.OperationStateRunning
				summary = "Cancellation could not be confirmed; capture session is still collecting"
				nextActions = []managerbizaiops.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: true}}
				eventType = "cancel_failed"
			case managerpacketcapturemodel.SessionStateFailed:
				nextState = manageraiopsmodel.OperationStateFailed
				summary = "Capture session failed while cancellation was requested"
				eventType = "failed_after_cancel"
			}
		}
		if err := operationUC.Transition(ctx, operation.ID,
			[]string{manageraiopsmodel.OperationStateCanceling}, nextState,
			summary, nextActions, eventType, map[string]any{"packet_capture_session_id": input.SessionID}); err != nil && !errors.Is(err, errs.ErrConflict) {
			return nil, err
		}
		return operationUC.GetOwned(ctx, operation.ID, operation.CreatedBy, true)
	}
}

type operationMessageAppender interface {
	AppendMessage(context.Context, *manageraiopsmodel.Message) error
}

func RunPacketCaptureOperations(ctx context.Context, operationUC *managerbizaiops.OperationUsecase, packetCaptureUC *managerbizpacketcapture.Usecase, messages operationMessageAppender, log *slog.Logger) error {
	notify := func(ctx context.Context, event managerbizpacketcapture.CompletionEvent) error {
		if event.Session != nil && event.Session.OperationID != "" {
			state := manageraiopsmodel.OperationStateSucceeded
			if event.Session.State == managerpacketcapturemodel.SessionStateFailed {
				state = manageraiopsmodel.OperationStateFailed
			} else if event.Session.State == managerpacketcapturemodel.SessionStateCancelled {
				state = manageraiopsmodel.OperationStateCancelled
			}
			summary := fmt.Sprintf("%d/%d capture artifact(s) available", event.Analysis.Summary.ReadyCount, event.Analysis.Summary.CaptureCount)
			if _, err := operationUC.AddArtifact(ctx, event.Session.OperationID, managerbizaiops.OperationArtifactInput{
				Kind: "analysis", Title: event.Session.Title, URL: "/artifacts/packet-sessions/" + event.Session.PublicID,
				Metadata: map[string]any{"session_state": event.Session.State, "ready_count": event.Analysis.Summary.ReadyCount},
			}); err != nil && !errors.Is(err, errs.ErrConflict) {
				return fmt.Errorf("create operation artifact: %w", err)
			}
			if err := operationUC.Transition(ctx, event.Session.OperationID,
				[]string{manageraiopsmodel.OperationStateCreated, manageraiopsmodel.OperationStateQueued, manageraiopsmodel.OperationStateRunning, manageraiopsmodel.OperationStateCanceling},
				state, summary, nil, "completed", map[string]any{"packet_capture_session_id": event.Session.PublicID, "session_state": event.Session.State}); err != nil && !errors.Is(err, errs.ErrConflict) {
				return fmt.Errorf("update operation completion: %w", err)
			}
		}
		body := packetCaptureCompletionMessage(event)
		message := &manageraiopsmodel.Message{
			ID:        uuid.NewSHA1(uuid.NameSpaceURL, []byte("packet-capture-completion:"+event.Session.PublicID)).String(),
			SessionID: event.ChatSessionID,
			Role:      manageraiopsmodel.RoleAssistant,
			Content:   &body,
			CreatedAt: time.Now().UTC(),
		}
		if err := messages.AppendMessage(ctx, message); err != nil && !errors.Is(err, errs.ErrConflict) {
			return fmt.Errorf("append completion message: %w", err)
		}
		return nil
	}
	coordinator := managerbizaiops.NewOperationCoordinator(5*time.Second, log.With(slog.String("comp", "operation-coordinator")))
	if err := coordinator.Register(operationReconcilerFunc{kind: "packet_capture_session", reconcile: func(ctx context.Context) error {
		return packetCaptureUC.ReconcileActiveSessions(ctx, 50, notify)
	}}); err != nil {
		return err
	}
	return coordinator.Run(ctx)
}

func packetCaptureCompletionMessage(event managerbizpacketcapture.CompletionEvent) string {
	if event.Session == nil {
		return "抓包任务已结束。"
	}
	state := "已完成"
	if event.Session.State == "partial" {
		state = "部分完成"
	} else if event.Session.State == "cancelled" {
		state = "已停止"
	} else if event.Session.State == "failed" {
		state = "失败"
	}
	return fmt.Sprintf(
		"抓包会话“%s”%s：%d/%d 个 PCAP 已就绪，发现 %d 条关联流和 %d 个数据包事件。\n\n[打开抓包会话并分析](/artifacts/packet-sessions/%s)",
		event.Session.Title,
		state,
		event.Analysis.Summary.ReadyCount,
		event.Analysis.Summary.CaptureCount,
		event.Analysis.Summary.FlowCount,
		event.Analysis.Summary.EventCount,
		event.Session.PublicID,
	)
}

type operationReconcilerFunc struct {
	kind      string
	reconcile func(context.Context) error
}

func (r operationReconcilerFunc) Kind() string { return r.kind }

func (r operationReconcilerFunc) Reconcile(ctx context.Context) error { return r.reconcile(ctx) }
