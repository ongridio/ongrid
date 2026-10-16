package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	managerbizaiops "github.com/ongridio/ongrid/internal/manager/biz/aiops"
	aiopstools "github.com/ongridio/ongrid/internal/manager/biz/aiops/tools"
	manageraiopsmodel "github.com/ongridio/ongrid/internal/manager/model/aiops"
	managerapprovalmodel "github.com/ongridio/ongrid/internal/manager/model/approval"
	managersvcaiops "github.com/ongridio/ongrid/internal/manager/service/aiops"
)

type fakeK8sActionProposalReader struct {
	rows []*manageraiopsmodel.MutatingProposal
}

func (f fakeK8sActionProposalReader) ListMutatingProposals(_ context.Context, filter managerbizaiops.MutatingProposalFilter) ([]*manageraiopsmodel.MutatingProposal, error) {
	if filter.Offset >= len(f.rows) {
		return nil, nil
	}
	end := filter.Offset + filter.Limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	return f.rows[filter.Offset:end], nil
}

type fakeK8sActionApprovalReader struct {
	rows []*managerapprovalmodel.Approval
}

func (f fakeK8sActionApprovalReader) ListKind(_ context.Context, _ string, limit, offset int) ([]*managerapprovalmodel.Approval, error) {
	if offset >= len(f.rows) {
		return nil, nil
	}
	end := offset + limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	return f.rows[offset:end], nil
}

func TestK8sActionAuditReaderMergesAndSanitizesBothApprovalPaths(t *testing.T) {
	graphTime := time.Date(2026, time.August, 6, 8, 20, 0, 0, time.UTC)
	humanTime := graphTime.Add(time.Minute)
	executedAt := humanTime.Add(10 * time.Second)
	reason := "operator approved"
	graphArgs := `{"cluster_id":48,"action":"scale","kind":"Deployment","namespace":"ongrid-system","name":"gateway","replicas":3,"preflight_token":"graph-secret"}`
	humanPayload, err := json.Marshal(managersvcaiops.K8sActionPayload{
		Args: aiopstools.ExecuteK8sActionArgs{
			ClusterID: 48, Action: "scale", Kind: "Deployment", Namespace: "ongrid-system",
			Name: "metrics-scraper", Replicas: intPtr(2), PreflightToken: "human-secret",
		},
		UserID: 7, SessionID: "human-session",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	otherPayload, err := json.Marshal(managersvcaiops.K8sActionPayload{Args: aiopstools.ExecuteK8sActionArgs{
		ClusterID: 49, Action: "cordon", Name: "worker-1", PreflightToken: "other-secret",
	}})
	if err != nil {
		t.Fatalf("marshal other payload: %v", err)
	}

	reader := ActionAuditReader{
		proposals: fakeK8sActionProposalReader{rows: []*manageraiopsmodel.MutatingProposal{{
			ID: "graph-1", SessionID: "graph-session", ToolName: aiopstools.ToolNameExecuteK8sAction,
			ArgsJSON: graphArgs, ToolClass: "write", ReviewerAgent: "reviewer",
			Decision: manageraiopsmodel.DecisionApprove, OperatorUserID: 6,
			CreatedAt: graphTime, DecidedAt: &graphTime, ExecutedAt: &graphTime,
		}}},
		approvals: fakeK8sActionApprovalReader{rows: []*managerapprovalmodel.Approval{
			{
				ID: "human-1", Kind: aiopstools.ToolNameExecuteK8sAction, PayloadJSON: string(humanPayload),
				SessionID: "human-session", Status: managerapprovalmodel.StatusExecuted,
				ProposedBy: 7, ApprovedBy: uint64Ptr(8), Reason: &reason,
				CreatedAt: humanTime, DecidedAt: &humanTime, ExecutedAt: &executedAt,
			},
			{
				ID: "other-cluster", Kind: aiopstools.ToolNameExecuteK8sAction,
				PayloadJSON: string(otherPayload), Status: managerapprovalmodel.StatusPending,
				CreatedAt: humanTime.Add(time.Minute),
			},
		}},
	}

	items, total, err := reader.ListK8sActionAudits(context.Background(), 48, 10, 0)
	if err != nil {
		t.Fatalf("ListK8sActionAudits: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("items/total = %d/%d, want 2/2", len(items), total)
	}
	if items[0].ID != "human-1" || items[0].ApprovalMode != "human" || items[0].Status != "executed" {
		t.Fatalf("human record = %+v", items[0])
	}
	if items[1].ID != "graph-1" || items[1].ApprovalMode != "review_gate" || items[1].Status != "executed" {
		t.Fatalf("graph record = %+v", items[1])
	}
	for _, item := range items {
		if strings.Contains(item.ArgsJSON, "preflight_token") || strings.Contains(item.ArgsJSON, "secret") {
			t.Fatalf("record leaks preflight credential: %s", item.ArgsJSON)
		}
	}
}

func intPtr(value int) *int { return &value }

func uint64Ptr(value uint64) *uint64 { return &value }
