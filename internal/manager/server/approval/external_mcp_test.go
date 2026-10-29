package approval

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	biz "github.com/ongridio/ongrid/internal/manager/biz/externalmcp"
	model "github.com/ongridio/ongrid/internal/manager/model/approval"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/ongridio/ongrid/internal/pkg/integrationauth"
	"github.com/ongridio/ongrid/internal/pkg/secretbox"
	"github.com/ongridio/ongrid/internal/pkg/tenantctx"
	"github.com/stretchr/testify/require"
)

func TestApprovalReviewIsRedactedAdminOnlyAndNotPersisted(t *testing.T) {
	t.Setenv("ONGRID_SECRET_KEY", "test-only-approval-key")
	ciphertext, err := secretbox.Encrypt(`{"arguments":{"action":"post_users","body":{"display_name":"Review this change","password":"private-fixture","api_key":"private-fixture","token":"private-fixture","dsn":"private-fixture"}}}`)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]any{"operation_id": "fixture", "arguments": map[string]any{"body": "[ENCRYPTED]"}, "encrypted_request": ciphertext})
	require.NoError(t, err)
	a := &model.Approval{Kind: biz.ApprovalKind, PayloadJSON: string(payload)}
	owner := tenantctx.Tenant{UserID: 42, Role: "admin"}
	r := httptest.NewRequest("GET", "/v1/approvals/fixture", nil).WithContext(tenantctx.With(context.Background(), owner))
	view, err := presentApproval(r, a)
	require.NoError(t, err)
	require.Contains(t, view.PayloadJSON, "Review this change")
	require.NotContains(t, view.PayloadJSON, "private-fixture")
	require.NotContains(t, view.PayloadJSON, "encrypted_request")
	require.Equal(t, string(payload), a.PayloadJSON, "database model must remain encrypted")
	r = r.WithContext(integrationauth.With(context.Background(), integrationauth.Identity{Owner: owner, Scopes: []string{"approvals:read"}}))
	view, err = presentApproval(r, a)
	require.NoError(t, err)
	require.NotContains(t, view.PayloadJSON, "Review this change")
	require.NotContains(t, view.PayloadJSON, "encrypted_request")
	r = r.WithContext(tenantctx.With(context.Background(), tenantctx.Tenant{Role: "viewer"}))
	_, err = presentApproval(r, a)
	require.ErrorIs(t, err, errs.ErrForbidden)
}
