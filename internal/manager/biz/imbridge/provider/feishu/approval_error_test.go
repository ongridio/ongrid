package feishu

import (
	"fmt"
	bridge "github.com/ongridio/ongrid/internal/manager/biz/imbridge"
	"github.com/ongridio/ongrid/internal/pkg/errs"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestApprovalCallbackErrorDistinguishesNextSteps(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{bridge.ErrIdentityUnbound, "生成绑定码"},
		{errs.ErrForbidden, "审批权限"},
		{errs.ErrNotFound, "卡片已失效"},
		{errs.ErrConflict, "审批已处理"},
		{fmt.Errorf("private database details"), "稍后重试"},
	} {
		text := approvalCallbackError(fmt.Errorf("wrapped: %w", tc.err))
		require.Contains(t, text, tc.want)
		require.NotContains(t, text, "/whoami")
		require.NotContains(t, text, "private database")
	}
}
