package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/manager/biz/aiops/tools/basetool"
	pcapbiz "github.com/ongridio/ongrid/internal/manager/biz/packetcapture"
	pcapmodel "github.com/ongridio/ongrid/internal/manager/model/packetcapture"
	"github.com/ongridio/ongrid/internal/pkg/tunnel"
)

type fakePacketCaptureCreator struct {
	in           pcapbiz.CreateInput
	sessionIn    pcapbiz.CreateSessionInput
	refresh      func(*pcapmodel.Capture) *pcapmodel.Capture
	emptySession bool
}

func fakePacketCaptureOperation(context.Context, PacketCaptureOperationInput) (PacketCaptureOperation, error) {
	return PacketCaptureOperation{ID: "operation-test", State: "running", Summary: "1 capture member(s) are being collected"}, nil
}

func (f *fakePacketCaptureCreator) Create(_ context.Context, in pcapbiz.CreateInput) (*pcapbiz.CreateOutput, error) {
	f.in = in
	return &pcapbiz.CreateOutput{
		Capture: &pcapmodel.Capture{
			ID:            12,
			DeviceID:      in.DeviceID,
			InterfaceName: in.Interface,
			State:         pcapmodel.StateCapturing,
		},
		Edge: tunnel.PacketCaptureTask{ID: "pcap-12", State: "running"},
	}, nil
}

func (f *fakePacketCaptureCreator) CreateSession(ctx context.Context, in pcapbiz.CreateSessionInput) (*pcapbiz.SessionOutput, error) {
	f.sessionIn = in
	if f.emptySession {
		return &pcapbiz.SessionOutput{
			Session:      &pcapmodel.Session{ID: 4, PublicID: "pcap-session-empty", State: pcapmodel.SessionStateFailed},
			Captures:     []*pcapmodel.Capture{},
			MemberErrors: []string{"device 24: packet capture: dispatch edge: busy"},
		}, nil
	}
	if len(in.Targets) == 0 {
		return nil, nil
	}
	created, err := f.Create(ctx, pcapbiz.CreateInput{
		DeviceID: in.Targets[0].DeviceID, Interface: in.Targets[0].Interface,
		Filter: in.Filter, DurationSeconds: in.DurationSeconds, Source: in.Source, CreatedBy: in.CreatedBy,
	})
	if err != nil {
		return nil, err
	}
	created.Capture.SessionID = 4
	return &pcapbiz.SessionOutput{Session: &pcapmodel.Session{ID: 4, PublicID: "pcap-session-test"}, Captures: []*pcapmodel.Capture{created.Capture}}, nil
}

func (f *fakePacketCaptureCreator) Refresh(_ context.Context, id uint64) (*pcapmodel.Capture, error) {
	capture := &pcapmodel.Capture{ID: id, State: pcapmodel.StateReady, ArtifactID: "pcap-11111111-1111-1111-1111-111111111111", ParsedJSON: `{"packets":[{"number":1,"source":"10.0.0.1","destination":"10.0.0.2","protocol":"TCP"}]}`}
	if f.refresh != nil {
		capture = f.refresh(capture)
	}
	return capture, nil
}

func (f *fakePacketCaptureCreator) GetSession(_ context.Context, id string) (*pcapbiz.SessionDetail, error) {
	return &pcapbiz.SessionDetail{Session: &pcapmodel.Session{PublicID: id}, Analysis: pcapbiz.SessionAnalysis{}}, nil
}

func TestGetPacketCaptureSessionToolReturnsSessionAnalysis(t *testing.T) {
	tool := NewGetPacketCaptureSessionTool(&fakePacketCaptureCreator{})
	out, err := tool.InvokableRun(context.Background(), `{"session_id":"pcap-session-123"}`)
	if err != nil || !strings.Contains(out, "pcap-session-123") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestCapturePCAPToolInfoIsReadSpecialty(t *testing.T) {
	tool := NewCapturePCAPTool(&fakePacketCaptureCreator{}, nil, fakePacketCaptureOperation)
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != ToolNameCapturePCAP || info.Class != "read" {
		t.Fatalf("info = name:%s class:%s", info.Name, info.Class)
	}
	if toolTier(tool) != "specialty" {
		t.Fatalf("%s should be specialty", ToolNameCapturePCAP)
	}
}

func TestCapturePCAPToolInvokesUsecase(t *testing.T) {
	creator := &fakePacketCaptureCreator{}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)

	out, err := tool.InvokableRun(context.Background(), `{
		"device_id": 24,
		"interface": "eth0",
		"filter": "tcp and port 443",
		"duration_seconds": 20,
		"reason": "debug checkout timeout"
	}`, basetool.WithUserText("confirm device_id=24"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if creator.in.Source != pcapbiz.SourceChat || creator.in.DeviceID != 24 || creator.in.Interface != "eth0" {
		t.Fatalf("input = %+v", creator.in)
	}
	var decoded struct {
		Session struct {
			PublicID string `json:"public_id"`
			Title    string `json:"title"`
		} `json:"session"`
		PendingAnalysis struct {
			State   string `json:"state"`
			Message string `json:"message"`
		} `json:"pending_analysis"`
		Result struct {
			Capture struct {
				ID uint64 `json:"id"`
			} `json:"capture"`
			Waited   bool `json:"waited"`
			Artifact struct {
				ID           string `json:"id"`
				FirstPackets []struct {
					Source string `json:"source"`
				} `json:"first_packets"`
			} `json:"artifact"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if decoded.Session.PublicID == "" || decoded.Result.Capture.ID != 12 || decoded.Result.Waited || decoded.Result.Artifact.ID != "" {
		t.Fatalf("output = %s", out)
	}
	if decoded.PendingAnalysis.State != "collecting" || !strings.Contains(decoded.PendingAnalysis.Message, "durable operation") {
		t.Fatalf("pending_analysis = %+v", decoded.PendingAnalysis)
	}
}

func TestCapturePCAPToolReturnsStructuredEmptySessionFailure(t *testing.T) {
	creator := &fakePacketCaptureCreator{emptySession: true}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)

	out, err := tool.InvokableRun(context.Background(), `{
		"device_id": 24,
		"interface": "eth0",
		"filter": "tcp and port 443"
	}`, basetool.WithUserText("confirm device_id=24"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var decoded struct {
		Status       string   `json:"status"`
		Error        string   `json:"error"`
		MemberErrors []string `json:"member_errors"`
		Session      struct {
			PublicID string `json:"public_id"`
		} `json:"session"`
		Links map[string]string `json:"links"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if decoded.Status != "failed" || decoded.Session.PublicID != "pcap-session-empty" || len(decoded.MemberErrors) != 1 || decoded.Links["detail"] == "" {
		t.Fatalf("output = %s", out)
	}
	if !strings.Contains(decoded.MemberErrors[0], "dispatch edge") {
		t.Fatalf("member errors = %+v", decoded.MemberErrors)
	}
}

func TestCapturePCAPToolRequiresExplicitOrStructuredTargetConfirmation(t *testing.T) {
	creator := &fakePacketCaptureCreator{}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)
	args := `{"device_id":24,"interface":"eth0"}`
	if _, err := tool.InvokableRun(context.Background(), args); err == nil {
		t.Fatal("capture without user confirmation unexpectedly succeeded")
	}
	if _, err := tool.InvokableRun(context.Background(), args, basetool.WithConfirmedDeviceIDs([]uint64{24})); err != nil {
		t.Fatalf("structured device selection should permit capture: %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), args, basetool.WithHumanApproval(true)); err != nil {
		t.Fatalf("shared approval gate should permit its frozen target: %v", err)
	}
}

func TestCapturePCAPToolCreatesRepeatedMembersInOneSession(t *testing.T) {
	creator := &fakePacketCaptureCreator{}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)

	_, err := tool.InvokableRun(context.Background(), `{
		"device_id": 24,
		"interface": "eth0",
		"repeat_count": 2,
		"title": "HTTPS investigation"
	}`, basetool.WithUserText("confirm device_id=24"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if len(creator.sessionIn.Targets) != 2 || creator.sessionIn.Title != "HTTPS investigation" {
		t.Fatalf("session input = %+v", creator.sessionIn)
	}
	for _, target := range creator.sessionIn.Targets {
		if target.DeviceID != 24 || target.Interface != "eth0" {
			t.Fatalf("target = %+v", target)
		}
	}
	if creator.sessionIn.Targets[1].StartAfterSeconds != 30 {
		t.Fatalf("second round starts after %d seconds", creator.sessionIn.Targets[1].StartAfterSeconds)
	}
}

func TestCapturePCAPToolAcceptsSessionNameAlias(t *testing.T) {
	creator := &fakePacketCaptureCreator{}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)

	_, err := tool.InvokableRun(context.Background(), `{
		"device_id": 24,
		"interface": "eth0",
		"session_name": "HTTPS 排障抓包"
	}`, basetool.WithUserText("confirm device_id=24"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if creator.sessionIn.Title != "HTTPS 排障抓包" {
		t.Fatalf("session title = %q", creator.sessionIn.Title)
	}
}

func TestCapturePCAPToolUsesWorkflowSourceFromContext(t *testing.T) {
	creator := &fakePacketCaptureCreator{}
	tool := NewCapturePCAPTool(creator, nil, fakePacketCaptureOperation)
	ctx := basetool.WithArtifactSource(context.Background(), basetool.ArtifactSourceWorkflow)

	_, err := tool.InvokableRun(ctx, `{"device_id":24,"interface":"eth0"}`, basetool.WithUserText("confirm device_id=24"))
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	if creator.in.Source != pcapbiz.SourceWorkflow {
		t.Fatalf("source = %q, want workflow", creator.in.Source)
	}
}
