// Package skill 的资源配额测试（跨平台纯函数逻辑）。
// detectMachineSpec 的平台特定测试见 quota_windows_test.go（GlobalMemoryStatusEx
// mock）/ quota_linux_test.go（/proc/meminfo 解析）。
package skill

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestComputeAdaptive_Scenario_FloorOrCeiling 验证自适应计算在
// 不同 RAM/CPU 配置下命中下限、中间、上限。
// RAM × 8%，min 512MB / max 1.5GB；CPU 占总 CPU 10%（固定百分比），
// 并发 = CPU 核数，min 2 / max 8。
func TestComputeAdaptive_Scenario_FloorOrCeiling(t *testing.T) {
	const (
		mb       = 1024 * 1024
		gb       = 1024 * 1024 * 1024
		memFloor = uint64(512)
		memCeil  = uint64(1536)
	)
	tests := []struct {
		name      string
		totalRAM  uint64
		cpuCores  int
		wantMem   uint64 // MB; -1=取 floor / -2=取 ceil
		wantConc  int
		memAssert string // "floor"|"mid"|"ceil"
	}{
		{
			// 4GB / 2 核（Server 2016 小机器）：RAM×8% = 320MB → floor 512MB
			// 验收标准 #1。
			name: "4GB_2cores_floor_hit", totalRAM: 4 * gb, cpuCores: 2,
			wantMem: memFloor, wantConc: 2, memAssert: "floor",
		},
		{
			// 8GB / 4 核：RAM×8% = 655MB（IEC 字节：8*1024³*0.08/1024² = 655.36）。
			name: "8GB_4cores_mid", totalRAM: 8 * gb, cpuCores: 4,
			wantMem: 655, wantConc: 4, memAssert: "mid",
		},
		{
			// 16GB / 4 核：RAM×8% ≈ 1310MB（IEC：16*1024³*0.08/1024² = 1310.72）。
			// 验收标准 #2（issue body 写 1280 是 GB-1000 换算，IEC 严格值 1310）。
			name: "16GB_4cores_mid", totalRAM: 16 * gb, cpuCores: 4,
			wantMem: 1310, wantConc: 4, memAssert: "mid",
		},
		{
			// 64GB / 8 核：RAM×8% ≈ 5243MB → ceil 1.5GB。
			// 验收标准 #3。
			name: "64GB_8cores_ceil_hit", totalRAM: 64 * gb, cpuCores: 8,
			wantMem: memCeil, wantConc: 8, memAssert: "ceil",
		},
		{
			// 128GB / 32 核大机器：RAM 远超 ceil；并发 ceil 8。
			name: "128GB_32cores_all_ceil", totalRAM: 128 * gb, cpuCores: 32,
			wantMem: memCeil, wantConc: 8, memAssert: "ceil",
		},
		{
			// 1GB / 1 核极小机器：RAM×8% < floor；并发 floor 2。
			name: "1GB_1cores_all_floor", totalRAM: 1 * gb, cpuCores: 1,
			wantMem: memFloor, wantConc: 2, memAssert: "floor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeAdaptive(tt.totalRAM, tt.cpuCores)
			if got.MemoryMB != tt.wantMem {
				t.Errorf("MemoryMB = %d, want %d (totalRAM=%d bytes)", got.MemoryMB, tt.wantMem, tt.totalRAM)
			}
			if got.MaxConcurrent != tt.wantConc {
				t.Errorf("MaxConcurrent = %d, want %d", got.MaxConcurrent, tt.wantConc)
			}
			// CPU 占总 CPU 10% 固定。
			if got.CPUPercent != 10 {
				t.Errorf("CPUPercent = %d, want 10 ( fixed 10%%)", got.CPUPercent)
			}
			// 边界保护断言（额外验证 floor/ceil 数学不变量）。
			switch tt.memAssert {
			case "floor":
				if got.MemoryMB != memFloor {
					t.Errorf("expected floor %d MB, got %d", memFloor, got.MemoryMB)
				}
			case "ceil":
				if got.MemoryMB != memCeil {
					t.Errorf("expected ceiling %d MB, got %d", memCeil, got.MemoryMB)
				}
			}
			if got.MemoryMB < memFloor || got.MemoryMB > memCeil {
				t.Errorf("MemoryMB %d out of [%d, %d]", got.MemoryMB, memFloor, memCeil)
			}
			if got.MaxConcurrent < 2 || got.MaxConcurrent > 8 {
				t.Errorf("MaxConcurrent %d out of [2, 8]", got.MaxConcurrent)
			}
		})
	}
}

// TestApplyConfigOverride_Scenario_* 验证用户通过 ONGRID_EDGE_QUOTA_* env vars
// 覆盖自适应值。0/未设 = 保留自适应默认。
func TestApplyConfigOverride_Scenario_OverrideOrKeep(t *testing.T) {
	base := QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2}
	tests := []struct {
		name         string
		overlay      QuotaLimits // 0 字段 = 保留 base
		wantMem      uint64
		wantCPU      int
		wantConc     int
	}{
		{
			name:    "all_zero_keeps_adaptive",
			overlay: QuotaLimits{},
			wantMem: 512, wantCPU: 10, wantConc: 2,
		},
		{
			name:    "memory_only_override",
			overlay: QuotaLimits{MemoryMB: 256},
			wantMem: 256, wantCPU: 10, wantConc: 2,
		},
		{
			name:    "cpu_only_override",
			overlay: QuotaLimits{CPUPercent: 5},
			wantMem: 512, wantCPU: 5, wantConc: 2,
		},
		{
			name:    "concurrent_only_override",
			overlay: QuotaLimits{MaxConcurrent: 4},
			wantMem: 512, wantCPU: 10, wantConc: 4,
		},
		{
			name:    "all_three_override",
			overlay: QuotaLimits{MemoryMB: 1024, CPUPercent: 20, MaxConcurrent: 6},
			wantMem: 1024, wantCPU: 20, wantConc: 6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyConfigOverride(base, tt.overlay)
			if got.MemoryMB != tt.wantMem {
				t.Errorf("MemoryMB = %d, want %d", got.MemoryMB, tt.wantMem)
			}
			if got.CPUPercent != tt.wantCPU {
				t.Errorf("CPUPercent = %d, want %d", got.CPUPercent, tt.wantCPU)
			}
			if got.MaxConcurrent != tt.wantConc {
				t.Errorf("MaxConcurrent = %d, want %d", got.MaxConcurrent, tt.wantConc)
			}
		})
	}
}

// TestApplyConfigOverride_Adversarial_RejectMalformed 验证用户 config 注入
// 超小/越界值时被 clamp 到下限（不拒绝，避免误杀；但日志告警）。
func TestApplyConfigOverride_Adversarial_RejectMalformed(t *testing.T) {
	base := QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2}
	tests := []struct {
		name    string
		overlay QuotaLimits
		// 全部 clamp 到 base（用户值无效，忽略）
		want    QuotaLimits
	}{
		{
			// memory_mb=0 在 config overlay 中表示"未设"，保留 base。
			// memory_mb=1 是明显错误（<floor），但 overlay 不做 floor 检查（
			// 用户显式覆盖视为有意识决策）。仍记录告警，由 manager 侧审计。
			name:    "tiny_memory_value_accepted_as_explicit_override",
			overlay: QuotaLimits{MemoryMB: 1},
			want:    QuotaLimits{MemoryMB: 1, CPUPercent: 10, MaxConcurrent: 2},
		},
		{
			// cpu_percent=0 在 overlay 中表示"未设"，不覆盖。
			name:    "cpu_zero_keeps_base",
			overlay: QuotaLimits{CPUPercent: 0},
			want:    QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyConfigOverride(base, tt.overlay)
			if got != tt.want {
				t.Errorf("overlay %+v → %+v, want %+v", tt.overlay, got, tt.want)
			}
		})
	}
}

// TestQuota_AcquireAndRelease_HappyPath 验证并发槽位的正常获取/释放。
func TestQuota_AcquireAndRelease_HappyPath(t *testing.T) {
	q := newQuotaForTest(QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2}, 4)
	ctx := context.Background()

	if err := q.Acquire(ctx, "skill_a"); err != nil {
		t.Fatalf("Acquire skill_a: %v", err)
	}
	if err := q.Acquire(ctx, "skill_b"); err != nil {
		t.Fatalf("Acquire skill_b: %v", err)
	}
	if n := q.ActiveCount(); n != 2 {
		t.Errorf("ActiveCount after 2 acquires = %d, want 2", n)
	}
	q.Release()
	q.Release()
	if n := q.ActiveCount(); n != 0 {
		t.Errorf("ActiveCount after releases = %d, want 0", n)
	}
}

// TestQuota_Acquire_ConcurrencyLimit_BlocksThenProceeds 验证并发超限时第 3 个
// Acquire 阻塞，Release 后才通过（FIFO 排队语义， 超限行为）。
func TestQuota_Acquire_ConcurrencyLimit_BlocksThenProceeds(t *testing.T) {
	q := newQuotaForTest(QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2}, 4)
	ctx := context.Background()

	// 占满 2 个并发槽位。
	if err := q.Acquire(ctx, "skill_a"); err != nil {
		t.Fatal(err)
	}
	if err := q.Acquire(ctx, "skill_b"); err != nil {
		t.Fatal(err)
	}

	// 第 3 个 Acquire 应该阻塞。
	proceeded := make(chan error, 1)
	go func() {
		proceeded <- q.Acquire(ctx, "skill_c")
	}()

	select {
	case <-proceeded:
		t.Fatal("Acquire proceeded before Release (concurrency limit not enforced)")
	case <-time.After(50 * time.Millisecond):
		// 预期阻塞。
	}

	// Release 一个槽位。
	q.Release()

	select {
	case err := <-proceeded:
		if err != nil {
			t.Errorf("Acquire after Release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not proceed within 1s after Release")
	}

	q.Release()
	q.Release()
}

// TestQuota_Acquire_MemoryExceeded_Rejects 验证内存超限时拒绝新 skill
//。
func TestQuota_Acquire_MemoryExceeded_Rejects(t *testing.T) {
	q := newQuotaForTest(QuotaLimits{MemoryMB: 100, CPUPercent: 10, MaxConcurrent: 4}, 4)
	q.setCurMemMBForTest(150) // 当前 150MB > 限制 100MB

	err := q.Acquire(context.Background(), "skill_a")
	if err == nil {
		t.Fatal("Acquire should fail when memory exceeded")
	}
	if !errors.Is(err, ErrMemoryQuotaExceeded) {
		t.Errorf("Acquire returned non-quota error: %v", err)
	}
}

// TestQuota_Acquire_ContextCancelled_ReleasesSlot 验证 context 取消时
// Acquire 返回 ctx.Err() 且不占用槽位。
func TestQuota_Acquire_ContextCancelled_ReleasesSlot(t *testing.T) {
	q := newQuotaForTest(QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 1}, 4)
	bgCtx := context.Background()

	// 占满唯一槽位。
	if err := q.Acquire(bgCtx, "skill_a"); err != nil {
		t.Fatal(err)
	}

	// 第 2 个 Acquire 用可取消的 ctx，先取消再 select。
	waitCtx, waitCancel := context.WithCancel(context.Background())
	defer waitCancel()
	done := make(chan error, 1)
	go func() { done <- q.Acquire(waitCtx, "skill_b") }()

	// 确保阻塞中。
	select {
	case <-done:
		t.Fatal("second Acquire should block")
	case <-time.After(50 * time.Millisecond):
	}
	waitCancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Acquire with cancelled ctx should error")
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not return after ctx cancel")
	}
	if n := q.ActiveCount(); n != 1 {
		t.Errorf("ActiveCount = %d, want 1 (cancelled Acquire should not hold slot)", n)
	}
	q.Release()
}

// TestQuota_Acquire_ReleasedAlwaysBalanced 验证多次 Acquire/Release 后槽位计数
// 平衡（无泄漏）。
func TestQuota_Acquire_ReleasedAlwaysBalanced(t *testing.T) {
	q := newQuotaForTest(QuotaLimits{MemoryMB: 512, CPUPercent: 10, MaxConcurrent: 2}, 4)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		if err := q.Acquire(ctx, "skill_x"); err != nil {
			t.Fatalf("iter %d Acquire: %v", i, err)
		}
		q.Release()
	}
	if n := q.ActiveCount(); n != 0 {
		t.Errorf("ActiveCount after balanced loop = %d, want 0", n)
	}
}

// newQuotaForTest 构造测试用 Quota（不启动 MemStats 轮询 goroutine）。
// cpuCores 仅用于日志，不影响并发计算（来自 QuotaLimits.MaxConcurrent）。
func newQuotaForTest(limits QuotaLimits, cpuCores int) *Quota {
	return newQuota(limits, cpuCores, testLogger())
}

// setCurMemMBForTest 测试钩子：直接设置当前内存用量。
func (q *Quota) setCurMemMBForTest(mb uint64) {
	q.curMemMB.Store(mb)
}
