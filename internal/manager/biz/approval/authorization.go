package approval

import (
	"context"
	"github.com/ongridio/ongrid/internal/pkg/errs"
)

// Authorizer 必须查询当前账号状态，不能信任长期有效的 IM 身份或旧 JWT 角色。
type Authorizer interface {
	AuthorizeApproval(context.Context, uint64) error
}

// RequireAuthorization 在装配阶段注入 Web 与 IM 共用的实时权限检查。
// 不允许运行期间替换，避免与审批并发读写。
func (u *Usecase) RequireAuthorization(a Authorizer) { u.authorizer = a }

func (u *Usecase) AuthorizeApproval(ctx context.Context, userID uint64) error {
	if userID == 0 || u.authorizer == nil {
		return errs.ErrForbidden
	}
	return u.authorizer.AuthorizeApproval(ctx, userID)
}

// AuthorizerFunc 用于消费方适配 IAM 的权限检查。
type AuthorizerFunc func(context.Context, uint64) error

func (f AuthorizerFunc) AuthorizeApproval(ctx context.Context, id uint64) error { return f(ctx, id) }
