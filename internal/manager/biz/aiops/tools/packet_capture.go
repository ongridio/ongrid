package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	pcapbiz "github.com/ongridio/ongrid/internal/manager/biz/packetcapture"
	pcapmodel "github.com/ongridio/ongrid/internal/manager/model/packetcapture"
)

const (
	ToolNameCapturePCAP    = "capture_pcap"
	ToolNameGetPCAPSession = "get_packet_capture_session"
)

const capturePCAPDescription = "Start a bounded packet capture session on one or more host devices. Use repeat_count to create multiple capture members for each target in the same session. The tool returns a durable, cancelable operation as soon as every member is accepted by its edge; it does not wait for packets to finish. If the session is still collecting, present the operation/session id and detail link instead of treating zero PCAPs as a final analysis. Captures are audited and must be explicitly requested by the operator."

const capturePCAPWhenToUse = "Use only when the user explicitly asks to capture packets or diagnose live network traffic on a specific host device/interface. Do not use for normal metric/log/trace questions. Prefer query_logql/query_traceql/query_promql first unless the user asks for raw packets. After a successful start, do not immediately call get_packet_capture_session for final conclusions unless the user explicitly asks for current status or provides an already-completed pcap-session id."

var CapturePCAPSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "device_id": {"type": "integer", "description": "Host device id to capture on."},
    "interface": {"type": "string", "description": "Network interface name on the host, for example eth0."},
	"targets": {"type": "array", "minItems": 1, "description": "Capture targets. Use one item for one host; multiple items may be on the same or different edges.", "items": {"type": "object", "properties": {"device_id": {"type": "integer"}, "interface": {"type": "string"}}, "required": ["device_id", "interface"]}},
	"repeat_count": {"type": "integer", "minimum": 1, "maximum": 10, "default": 1, "description": "Number of sequential capture rounds per target in the same session. A round captures all targets concurrently; later rounds start after the prior round duration."},
    "filter": {"type": "string", "description": "Simple filter grammar: tcp, udp, icmp, icmp6, host <IP>, port <N>, joined by 'and'. Example: tcp and port 443."},
    "duration_seconds": {"type": "integer", "minimum": 1, "maximum": 300, "default": 30},
    "max_bytes": {"type": "integer", "minimum": 1, "maximum": 268435456, "default": 67108864},
    "max_packets": {"type": "integer", "minimum": 1, "maximum": 500000, "default": 100000},
    "snaplen": {"type": "integer", "minimum": 64, "maximum": 65535, "default": 1514},
	"promiscuous": {"type": "boolean", "default": false},
	"title": {"type": "string", "description": "Short investigation name shown on the packet capture session."},
	"session_name": {"type": "string", "description": "Alias for title; accepted for models that phrase the task as a named session."},
	"task_name": {"type": "string", "description": "Alias for title; accepted for models that phrase the task as a named task."},
	"reason": {"type": "string", "description": "Why this capture is requested. Stored on the capture record."}
  },
	"anyOf": [{"required": ["device_id", "interface"]}, {"required": ["targets"]}]
}`)

type PacketCaptureCreator interface {
	Create(ctx context.Context, in pcapbiz.CreateInput) (*pcapbiz.CreateOutput, error)
	Refresh(ctx context.Context, id uint64) (*pcapmodel.Capture, error)
	GetSession(ctx context.Context, publicID string) (*pcapbiz.SessionDetail, error)
}

var GetPacketCaptureSessionSchema = json.RawMessage(`{
  "type":"object",
  "properties":{"session_id":{"type":"string","description":"Opaque packet capture session id, pcap-session-..."}},
  "required":["session_id"]
}`)

type GetPacketCaptureSessionTool struct{ uc PacketCaptureCreator }

func NewGetPacketCaptureSessionTool(uc PacketCaptureCreator) *GetPacketCaptureSessionTool {
	return &GetPacketCaptureSessionTool{uc: uc}
}

func (t *GetPacketCaptureSessionTool) Info(context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{Name: ToolNameGetPCAPSession, Description: "Read a coordinated multi-edge packet capture session for diagnosis. Returns member capture status, normalized cross-edge flows, and merged packet metadata timeline; never returns raw PCAP payloads.", WhenToUse: "Use when the user asks to analyze, compare, or explain a packet capture session. Call this before making network-loss or latency claims.", Parameters: GetPacketCaptureSessionSchema, Class: "read"}, nil
}

func (t *GetPacketCaptureSessionTool) InvokableRun(ctx context.Context, argsJSON string, _ ...basetool.InvokeOption) (string, error) {
	if t.uc == nil {
		return "", fmt.Errorf("%s: packet capture usecase not configured", ToolNameGetPCAPSession)
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameGetPCAPSession, err)
	}
	detail, err := t.uc.GetSession(ctx, strings.TrimSpace(in.SessionID))
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(map[string]any{"session": detail.Session, "captures": detail.Captures, "analysis": detail.Analysis})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameGetPCAPSession, err)
	}
	return string(body), nil
}

type PacketCaptureSessionCreator interface {
	CreateSession(ctx context.Context, in pcapbiz.CreateSessionInput) (*pcapbiz.SessionOutput, error)
}

// PacketCaptureOperationCreator keeps the tool package independent from the
// durable Operation implementation while making the long-running commitment
// explicit at the tool boundary.
type PacketCaptureOperationCreator func(context.Context, PacketCaptureOperationInput) (PacketCaptureOperation, error)

type PacketCaptureOperationInput struct {
	ChatSessionID string
	CreatedBy     uint64
	Title         string
	SessionID     string
	MemberCount   int
}

type PacketCaptureOperation struct {
	ID      string
	State   string
	Summary string
}

type PacketCaptureOperationBinder interface {
	BindSessionOperation(ctx context.Context, publicID, operationID string) error
}

type CapturePCAPTool struct {
	uc              PacketCaptureCreator
	operationCreate PacketCaptureOperationCreator
	log             *slog.Logger
}

type capturePCAPArgs struct {
	DeviceID        uint64 `json:"device_id"`
	Interface       string `json:"interface"`
	Filter          string `json:"filter"`
	DurationSeconds int    `json:"duration_seconds"`
	MaxBytes        int64  `json:"max_bytes"`
	MaxPackets      int    `json:"max_packets"`
	Snaplen         int    `json:"snaplen"`
	Promiscuous     bool   `json:"promiscuous"`
	RepeatCount     int    `json:"repeat_count"`
	Title           string `json:"title"`
	SessionName     string `json:"session_name"`
	TaskName        string `json:"task_name"`
	Reason          string `json:"reason"`
	Targets         []struct {
		DeviceID  uint64 `json:"device_id"`
		Interface string `json:"interface"`
	} `json:"targets"`
}

func NewCapturePCAPTool(uc PacketCaptureCreator, log *slog.Logger, operationCreate ...PacketCaptureOperationCreator) *CapturePCAPTool {
	if log == nil {
		log = slog.Default()
	}
	tool := &CapturePCAPTool{uc: uc, log: log}
	if len(operationCreate) != 0 {
		tool.operationCreate = operationCreate[0]
	}
	return tool
}

func (t *CapturePCAPTool) Info(_ context.Context) (*basetool.ToolInfo, error) {
	return &basetool.ToolInfo{
		Name:        ToolNameCapturePCAP,
		Description: capturePCAPDescription,
		WhenToUse:   capturePCAPWhenToUse,
		Parameters:  CapturePCAPSchema,
		// Capturing observes network traffic and creates a bounded evidence
		// artifact; it does not change the managed host configuration. Keeping
		// it read-class avoids applying the SOP gate intended for restart and
		// configuration changes to an explicit, time-bounded diagnostic request.
		Class:        "read",
		Confirmation: basetool.ConfirmationRequired,
	}, nil
}

func (t *CapturePCAPTool) InvokableRun(ctx context.Context, argsJSON string, opts ...basetool.InvokeOption) (string, error) {
	if t.uc == nil {
		return "", fmt.Errorf("%s: packet capture usecase not configured", ToolNameCapturePCAP)
	}
	var in capturePCAPArgs
	if err := json.Unmarshal([]byte(argsJSON), &in); err != nil {
		return "", fmt.Errorf("%s: bad args: %w", ToolNameCapturePCAP, err)
	}
	resolved := basetool.ResolveOptions(opts)
	source := pcapbiz.SourceChat
	switch basetool.ArtifactSourceFromContext(ctx) {
	case basetool.ArtifactSourceWorkflow:
		source = pcapbiz.SourceWorkflow
	case basetool.ArtifactSourceChat:
		source = pcapbiz.SourceChat
	}
	sessionCreator, ok := t.uc.(PacketCaptureSessionCreator)
	if !ok {
		return "", fmt.Errorf("%s: packet capture sessions are not configured", ToolNameCapturePCAP)
	}
	targets := make([]pcapbiz.SessionTarget, 0, len(in.Targets)+1)
	for _, target := range in.Targets {
		targets = append(targets, pcapbiz.SessionTarget{DeviceID: target.DeviceID, Interface: strings.TrimSpace(target.Interface)})
	}
	if len(targets) == 0 {
		targets = append(targets, pcapbiz.SessionTarget{DeviceID: in.DeviceID, Interface: strings.TrimSpace(in.Interface)})
	}
	for _, target := range targets {
		if !resolved.HumanApproved && !captureTargetExplicitlyConfirmed(resolved.UserText, resolved.ConfirmedDeviceIDs, target.DeviceID) {
			return "", fmt.Errorf("%s: device_id=%d was not explicitly confirmed by the user; ask the user to select or confirm the target device before capturing", ToolNameCapturePCAP, target.DeviceID)
		}
	}
	repeatCount := in.RepeatCount
	if repeatCount == 0 {
		repeatCount = 1
	}
	if repeatCount < 1 || repeatCount > 10 {
		return "", fmt.Errorf("%s: repeat_count must be between 1 and 10", ToolNameCapturePCAP)
	}
	if repeatCount > 1 {
		original := targets
		targets = make([]pcapbiz.SessionTarget, 0, len(original)*repeatCount)
		interval := in.DurationSeconds
		if interval == 0 {
			interval = 30
		}
		for round := range repeatCount {
			for _, target := range original {
				target.StartAfterSeconds = round * interval
				targets = append(targets, target)
			}
		}
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = strings.TrimSpace(in.SessionName)
	}
	if title == "" {
		title = strings.TrimSpace(in.TaskName)
	}
	if title == "" {
		title = "Packet capture"
	}
	out, err := sessionCreator.CreateSession(ctx, pcapbiz.CreateSessionInput{Targets: targets, Filter: strings.TrimSpace(in.Filter), DurationSeconds: in.DurationSeconds, MaxBytes: in.MaxBytes, MaxPackets: in.MaxPackets, Snaplen: in.Snaplen, Promiscuous: in.Promiscuous, Title: title, Description: strings.TrimSpace(in.Reason), Source: source, CreatedBy: resolved.UserID, ChatSessionID: basetool.SessionIDFromContext(ctx)})
	if err != nil {
		return "", err
	}
	if len(out.Captures) == 0 {
		body, marshalErr := json.Marshal(map[string]any{
			"status":        "failed",
			"error":         "packet capture session has no created members",
			"session":       out.Session,
			"captures":      out.Captures,
			"member_errors": out.MemberErrors,
			"links":         map[string]string{"detail": "/artifacts/packet-sessions/" + out.Session.PublicID},
		})
		if marshalErr != nil {
			return "", fmt.Errorf("%s: marshal empty-session response: %w", ToolNameCapturePCAP, marshalErr)
		}
		return string(body), nil
	}
	if t.operationCreate == nil {
		return "", fmt.Errorf("%s: operation runtime is not configured", ToolNameCapturePCAP)
	}
	op, err := t.operationCreate(ctx, PacketCaptureOperationInput{
		ChatSessionID: basetool.SessionIDFromContext(ctx),
		CreatedBy:     resolved.UserID,
		Title:         out.Session.Title,
		SessionID:     out.Session.PublicID,
		MemberCount:   len(out.Captures),
	})
	if err != nil {
		return "", fmt.Errorf("%s: create operation: %w", ToolNameCapturePCAP, err)
	}
	if binder, ok := t.uc.(PacketCaptureOperationBinder); ok {
		if err := binder.BindSessionOperation(ctx, out.Session.PublicID, op.ID); err != nil {
			return "", fmt.Errorf("%s: bind session operation: %w", ToolNameCapturePCAP, err)
		}
	}
	operation := basetool.Operation{
		Kind:    "packet_capture_session",
		ID:      op.ID,
		State:   op.State,
		Title:   out.Session.Title,
		Summary: op.Summary,
		Links:   map[string]string{"detail": "/artifacts/packet-sessions/" + out.Session.PublicID},
		Actions: []basetool.OperationAction{{Kind: "cancel", Label: "Stop", Enabled: true}},
	}
	body, err := json.Marshal(map[string]any{
		"operation":     operation,
		"session":       out.Session,
		"captures":      out.Captures,
		"member_errors": out.MemberErrors,
		"pending_analysis": map[string]any{
			"state":   "collecting",
			"message": "Packet capture has started as a durable operation. Final packet counts, endpoints, flows, and analysis are available only after the session reaches ready/partial/failed/cancelled.",
		},
		"result": capturePCAPResult(out.Captures[0], nil, false),
	})
	if err != nil {
		return "", fmt.Errorf("%s: marshal response: %w", ToolNameCapturePCAP, err)
	}
	return string(body), nil
}

// captureTargetExplicitlyConfirmed is a hard execution gate, not a prompt
// hint. A model may discover a similarly named device while resolving a
// request, but it must not turn that candidate into a packet capture unless
// the current user turn explicitly confirms the numeric device identifier.
func captureTargetExplicitlyConfirmed(userText string, confirmedDeviceIDs []uint64, deviceID uint64) bool {
	if deviceID == 0 {
		return false
	}
	for _, confirmed := range confirmedDeviceIDs {
		if confirmed == deviceID {
			return true
		}
	}
	quotedID := regexp.QuoteMeta(strconv.FormatUint(deviceID, 10))
	return regexp.MustCompile(`(?i)\bdevice_id\s*[=:]\s*` + quotedID + `\b`).MatchString(userText)
}

func capturePCAPResult(capture *pcapmodel.Capture, edge any, waited bool) map[string]any {
	result := map[string]any{
		"capture": capture,
		"edge":    edge,
		"waited":  waited,
	}
	if capture == nil || capture.State != pcapmodel.StateReady || capture.ParsedJSON == "" {
		return result
	}
	var parsed struct {
		ArtifactID string `json:"artifact_id"`
		Packets    []struct {
			Number      any    `json:"number"`
			Source      string `json:"source"`
			Destination string `json:"destination"`
			Protocol    string `json:"protocol"`
		} `json:"packets"`
	}
	if err := json.Unmarshal([]byte(capture.ParsedJSON), &parsed); err != nil {
		return result
	}
	preview := parsed.Packets
	if len(preview) > 3 {
		preview = preview[:3]
	}
	result["artifact"] = map[string]any{
		"id":               capture.ArtifactID,
		"captured_packets": capture.CapturedPackets,
		"captured_bytes":   capture.CapturedBytes,
		"first_packets":    preview,
	}
	return result
}

var _ basetool.BaseTool = (*CapturePCAPTool)(nil)
var _ basetool.BaseTool = (*GetPacketCaptureSessionTool)(nil)
