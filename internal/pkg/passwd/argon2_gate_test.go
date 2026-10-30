package passwd

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Verify 必须拒绝超出支持范围的 Argon2 参数，绝不能照着数据库里的
// m=... 直接分配内存。2026-10-30 的线上事故就是这类"参数来自存储、
// 无上限校验"的路径把 RSS 推到 32GiB 上限。
func TestVerifyRejectsOutOfEnvelopeParameters(t *testing.T) {
	encoded, err := Hash("s3cret")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		t.Fatalf("unexpected PHC layout: %q", encoded)
	}

	cases := []struct {
		name   string
		params string
	}{
		{"memory above envelope", "m=4294967295,t=1,p=4"},
		{"memory above 4x default", fmt.Sprintf("m=%d,t=1,p=4", maxArgonMemory+1)},
		{"zero memory", "m=0,t=1,p=4"},
		{"time above envelope", "m=65536,t=99,p=4"},
		{"threads above envelope", "m=65536,t=1,p=99"},
		{"zero threads", "m=65536,t=1,p=0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tampered := strings.Join([]string{"", parts[1], parts[2], c.params, parts[4], parts[5]}, "$")
			start := time.Now()
			if Verify("s3cret", tampered) {
				t.Fatal("Verify accepted a hash outside the supported envelope")
			}
			// 拒绝必须是"读参数就返回"，不能先分配再失败。
			if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
				t.Fatalf("rejection took %v, expected an early return", elapsed)
			}
		})
	}
}

// Verify 的并发必须被闸门限住：占满所有槽位后，新的 Verify 应当阻塞，
// 释放槽位后才能完成。这保证峰值内存是 槽位数 × 每次分配，而不是
// 请求并发数 × 每次分配。
func TestVerifyConcurrencyIsBounded(t *testing.T) {
	encoded, err := Hash("s3cret")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	for i := 0; i < argonVerifySlots; i++ {
		argonVerifyGate <- struct{}{}
	}
	// 无论断言如何结束，都要把槽位还回去，避免污染同包其它测试。
	defer func() {
		for len(argonVerifyGate) > 0 {
			<-argonVerifyGate
		}
	}()

	done := make(chan bool, 1)
	go func() { done <- Verify("s3cret", encoded) }()

	select {
	case <-done:
		t.Fatal("Verify returned while every slot was taken; the gate is not enforced")
	case <-time.After(150 * time.Millisecond):
		// 期望：被阻塞
	}

	for i := 0; i < argonVerifySlots; i++ {
		<-argonVerifyGate
	}

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("Verify returned false after the slot was released")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Verify did not finish after releasing a slot")
	}
}
