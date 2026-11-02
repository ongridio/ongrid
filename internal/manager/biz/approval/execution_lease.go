package approval

import (
	"context"
	"time"
)

// ExecutionLeaseRepo 的条件更新同时检查 owner、状态和有效期。过期的执行上下文不能复活。
type ExecutionLeaseRepo interface {
	RenewExecutionLease(context.Context, string, string, time.Time) error
	ExpireExecutionLeases(context.Context, time.Time) error
}

func (u *Usecase) SupportsExecutionLeases() bool { _, ok := u.repo.(ExecutionLeaseRepo); return ok }
func (u *Usecase) RenewExecutionLease(ctx context.Context, id, owner string) error {
	if r, ok := u.repo.(ExecutionLeaseRepo); ok {
		return r.RenewExecutionLease(ctx, id, owner, time.Now().UTC().Add(time.Minute))
	}
	return nil
}
func (u *Usecase) RunExecutionLeaseReconciler(ctx context.Context) {
	r, ok := u.repo.(ExecutionLeaseRepo)
	if !ok {
		return
	}
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		scan, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := r.ExpireExecutionLeases(scan, time.Now().UTC())
		cancel()
		if err != nil && ctx.Err() == nil {
			u.log.Warn("approval execution lease reconciliation failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
