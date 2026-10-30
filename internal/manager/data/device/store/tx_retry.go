package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	// txRetryAttempts 是事务级重试的总尝试次数（含首次执行）。
	txRetryAttempts = 3
	// txRetryBaseDelay 是首次退避基准，之后按 2 倍递增并叠加抖动。
	txRetryBaseDelay = 20 * time.Millisecond
)

// MySQL 服务端错误码：服务器已回滚事务，客户端可安全重放。
const (
	errMySQLDeadlock        = 1213 // Deadlock found when trying to get lock
	errMySQLLockWaitTimeout = 1205 // Lock wait timeout exceeded
)

// isTransientTxErr 判断 err 是否属于 InnoDB 主动回滚的事务冲突。
// 这类错误只回滚当前事务，不会留下部分提交，重放是官方推荐的处置方式；
// 其它错误（约束冲突、连接断开等）不在此列，交由调用方处理。
func isTransientTxErr(err error) bool {
	var me *mysql.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	return me.Number == errMySQLDeadlock || me.Number == errMySQLLockWaitTimeout
}

// retryTransientTx 执行 fn，遇到事务冲突时按指数退避 + 抖动重试。
// 调用方必须保证 fn 可安全重复执行（幂等）。
func retryTransientTx(ctx context.Context, fn func() error) error {
	delay := txRetryBaseDelay
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || !isTransientTxErr(err) {
			return err
		}
		if attempt >= txRetryAttempts {
			return fmt.Errorf("store: transaction retry exhausted after %d attempts: %w", attempt, err)
		}
		// 抖动取 [delay/2, 1.5*delay)，避免多台 Edge 在同一时刻重试再次相撞。
		wait := delay/2 + time.Duration(rand.Int64N(int64(delay)+1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
	}
}
