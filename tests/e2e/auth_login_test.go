//go:build e2e

// Catalog: B1 — admin 登录 → 拿到 access + refresh JWT → 用 access 调
//          /v1/self 返回当前用户。这是整个 e2e 套件的起点：如果它跑通，
//          说明 testenv.Start 真的把 MySQL + manager binary + 默认 admin
//          引导起来了，后续所有 test 都站在这之上。
package e2e

import (
	"testing"

	"github.com/ongridio/ongrid/tests/e2e/testenv"
)

func TestAuth_LoginAndSelf_B1(t *testing.T) {
	env := testenv.Start(t)

	pair := env.LoginAdmin()
	if pair.AccessToken == "" {
		t.Fatalf("LoginAdmin returned empty access_token")
	}

	status, body, err := env.DoJSON("GET", "/api/v1/self", nil, pair.AccessToken)
	if err != nil {
		t.Fatalf("/v1/self: %v", err)
	}
	if status != 200 {
		t.Fatalf("/v1/self: status=%d body=%v", status, body)
	}
	if email, _ := body["email"].(string); email != env.AdminEmail {
		t.Fatalf("/v1/self: email=%q want=%q (body=%v)", email, env.AdminEmail, body)
	}

	// Negative path: wrong-bearer must 401 (the auth middleware rejects
	// before the handler runs).
	bad, _, err := env.DoJSON("GET", "/api/v1/self", nil, "not-a-real-jwt")
	if err != nil {
		t.Fatalf("/v1/self (bad token): %v", err)
	}
	if bad != 401 {
		t.Fatalf("/v1/self with bad token: status=%d want 401", bad)
	}
}
