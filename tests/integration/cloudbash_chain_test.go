// Package integration holds cross-package chain tests that wire several biz
// subsystems together against an in-memory DB — the deterministic core of a
// feature, without HTTP / the LLM / external services.
//
// cloudbash_chain_test verifies the HLD-017 execution chain end to end:
//
//	credential vault (encrypted) → approval inbox (propose) → human approve
//	→ registered executor resolves the credential, injects it into the
//	Runner sandbox, runs the command → result captured with the secret
//	actually present in the child env.
//
// This is the chain we keep deploying and eyeballing; the test pins it so a
// regression (decrypt, injection, executor wiring, runner env) fails here
// instead of in a chat session.
package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	bizapproval "github.com/ongridio/ongrid/internal/manager/biz/approval"
	bizsecret "github.com/ongridio/ongrid/internal/manager/biz/secret"
	approvalstore "github.com/ongridio/ongrid/internal/manager/data/approval/store"
	secretstore "github.com/ongridio/ongrid/internal/manager/data/secret/store"
	"github.com/ongridio/ongrid/internal/pkg/runner"
)

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := secretstore.Migrate(db); err != nil {
		t.Fatalf("migrate secrets: %v", err)
	}
	if err := approvalstore.Migrate(db); err != nil {
		t.Fatalf("migrate approvals: %v", err)
	}
	return db
}

func TestCloudBashChain_CredentialInjectedOnApprove(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "integration-test-key-please-rotate")
	ctx := context.Background()
	db := openDB(t)

	secretUC := bizsecret.NewUsecase(secretstore.NewRepo(db))
	approvalUC := bizapproval.NewUsecase(approvalstore.NewRepo(db), nil)

	approvalUC.RequireAuthorization(bizapproval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))
	sh := runner.NewShellRunner()

	// Register the SAME executor cmd/main.go wires for cloud_bash: resolve
	// the bound credential → inject into the Runner → run.
	approvalUC.RegisterExecutor("cloud_bash", func(ctx context.Context, payloadJSON string) (string, error) {
		var p struct {
			Command    string `json:"command"`
			Credential string `json:"credential"`
		}
		if err := json.Unmarshal([]byte(payloadJSON), &p); err != nil {
			return "", err
		}
		env := map[string]string{}
		if p.Credential != "" {
			injected, _, err := secretUC.ResolveInjection(ctx, p.Credential)
			if err != nil {
				return "", err
			}
			env = injected
		}
		res, err := sh.Run(ctx, runner.Spec{Script: p.Command, Env: env})
		if err != nil {
			return "", err
		}
		out, _ := json.Marshal(map[string]any{"stdout": res.Stdout, "exit_code": res.ExitCode})
		return string(out), nil
	})

	// 1. Store a custom credential — fields inject as same-named env vars.
	if _, err := secretUC.Create(ctx, "test", "custom", "", map[string]string{"MYKEY": "hello123"}); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	// List is redacted: field keys only, no value.
	views, err := secretUC.List(ctx)
	if err != nil || len(views) != 1 || len(views[0].FieldKeys) != 1 || views[0].FieldKeys[0] != "MYKEY" {
		t.Fatalf("list credential = %+v, %v", views, err)
	}

	// 2. Propose a cloud_bash command bound to that credential.
	a, err := approvalUC.Propose(ctx, bizapproval.ProposeInput{
		Kind:       "cloud_bash",
		Title:      "echo injected=$MYKEY",
		Payload:    map[string]string{"command": "echo injected=$MYKEY", "credential": "test"},
		Source:     "agent",
		ProposedBy: 7,
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	// 3. Before approval: nothing ran (pending).
	pending, _ := approvalUC.List(ctx, "pending", 0)
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}

	// 4. Approve → executor runs with the credential injected.
	out, err := approvalUC.Approve(ctx, 99, a.ID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if out.Status != "executed" {
		t.Fatalf("status = %q, want executed; result=%v", out.Status, out.ResultJSON)
	}
	if out.ResultJSON == nil || !strings.Contains(*out.ResultJSON, "injected=hello123") {
		t.Fatalf("result missing injected secret: %v", out.ResultJSON)
	}
}

func TestCloudBashChain_RejectDoesNotRun(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "integration-test-key")
	ctx := context.Background()
	db := openDB(t)
	approvalUC := bizapproval.NewUsecase(approvalstore.NewRepo(db), nil)

	approvalUC.RequireAuthorization(bizapproval.AuthorizerFunc(func(context.Context, uint64) error { return nil }))

	ran := false
	approvalUC.RegisterExecutor("cloud_bash", func(context.Context, string) (string, error) {
		ran = true
		return "{}", nil
	})
	a, err := approvalUC.Propose(ctx, bizapproval.ProposeInput{Kind: "cloud_bash", Title: "rm -rf /", Payload: map[string]string{}})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if err := approvalUC.Reject(ctx, 1, a.ID, "nope"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if ran {
		t.Fatal("executor ran on a rejected proposal")
	}
	got, _ := approvalUC.Get(ctx, a.ID)
	if got.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", got.Status)
	}
}
