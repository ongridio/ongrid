package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestIsTransientTxErr(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	dupKey := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"deadlock", deadlock, true},
		{"deadlock wrapped by biz layer", fmt.Errorf("link edge to host: %w", deadlock), true},
		{"lock wait timeout", lockWait, true},
		{"duplicate key is not transient", dupKey, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isTransientTxErr(c.err); got != c.want {
				t.Fatalf("isTransientTxErr = %v, want %v", got, c.want)
			}
		})
	}
}

// 前两次死锁、第三次成功：应重试并且拿到 nil。
func TestRetryTransientTx_RetriesThenSucceeds(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	calls := 0
	err := retryTransientTx(context.Background(), func() error {
		calls++
		if calls < 3 {
			return deadlock
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryTransientTx = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

// 一直死锁：达到上限后返回错误，且必须仍能用 errors.As 取到原始错误。
func TestRetryTransientTx_ExhaustsAttempts(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "deadlock"}
	calls := 0
	err := retryTransientTx(context.Background(), func() error {
		calls++
		return deadlock
	})
	if err == nil {
		t.Fatal("retryTransientTx = nil, want error after exhausting attempts")
	}
	if calls != txRetryAttempts {
		t.Fatalf("calls = %d, want %d", calls, txRetryAttempts)
	}
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1213 {
		t.Fatalf("error chain lost the MySQL error: %v", err)
	}
}

// 非事务冲突错误必须立刻返回，不做无意义重试。
func TestRetryTransientTx_StopsOnNonTransient(t *testing.T) {
	sentinel := errors.New("constraint violation")
	calls := 0
	err := retryTransientTx(context.Background(), func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

// ctx 取消后不应继续退避等待。
func TestRetryTransientTx_HonoursContext(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1205, Message: "lock wait timeout"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := retryTransientTx(ctx, func() error { return deadlock })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > txRetryBaseDelay {
		t.Fatalf("returned after %v, expected prompt cancellation", elapsed)
	}
}
