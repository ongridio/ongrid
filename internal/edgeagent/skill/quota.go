// Package skill 资源配额实现。
// edge agent 启动时检测机器配置（RAM/CPU），按比例计算自适应默认配额，
// 可被 ONGRID_EDGE_QUOTA_* 环境变量覆盖。skill dispatcher 在 Execute 前
// 调 Acquire 检查内存/并发，Execute 后调 Release。
// 超限行为：
//   - 内存超限：拒绝新 skill（返回 ErrMemoryQuotaExceeded）
//   - 并发超限：FIFO 排队（semaphore）等待 Release 或 ctx 取消
//   - CPU 超限：本 issue 不实现（GOMAXPROCS 已限制 Go runtime 并发线程，
//     "skill 排队降频"语义与并发槽位重叠，避免双重限制）
package skill

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"
)

// 自适应配额常量。
// 内存：RAM×8% / floor 512MB / ceil 1.5GB（RCA 期间 5 并发 PowerShell spawn
// 需 ~340MB，原 256MB 下限会拒 skill → RCA 失败）。
// CPU：占总 CPU 10%，GOMAXPROCS = ceil(cores×10/100) clamp [1, 4]
//。
// 并发：= 核数 / floor 2 / ceil 8。
const (
	memRatioPercent    = 8
	memFloorMB         = uint64(512)
	memCeilingMB       = uint64(1536)
	cpuPercentAdaptive = 10 // 占总 CPU 的 10%
	cpuCeiling         = 4  // GOMAXPROCS 上限
	concurrentFloor    = 2
	concurrentCeiling  = 8

	// memStatsPollInterval 是 runtime.MemStats 轮询间隔（更新 curMemMB）。
	// 30s 足够：RCA skill 执行数秒~数十秒，30s 粒度避免拒绝瞬时空闲。
	memStatsPollInterval = 30 * time.Second
)

// ErrMemoryQuotaExceeded 在当前进程 RSS 超过配额上限时由 Acquire 返回。
// dispatcher 把它包装进 RPC 响应 Error 字段，manager 侧记录告警。
var ErrMemoryQuotaExceeded = errors.New("skill: memory quota exceeded")

// QuotaLimits 是配额的静态配置部分（不含运行时状态），便于纯函数测试。
type QuotaLimits struct {
	MemoryMB      uint64 // edge 进程总占用上限（Sys 字节）
	CPUPercent    int    // 占机器总 CPU 的百分比（1-100）
	MaxConcurrent int    // 同时执行的 skill 数
}

// Quota 是运行时配额控制器。Acquire 阻塞直到获得并发槽位且内存未超限；
// Release 释放槽位。curMemMB 由 pollMemStats goroutine 周期更新。
type Quota struct {
	limits   QuotaLimits
	cpuCores int
	curMemMB atomic.Uint64
	sem      chan struct{} // FIFO 并发槽位
	active   atomic.Int64
	log      *slog.Logger
}

// newQuota 构造 Quota 但不启动 MemStats 轮询。调用方应启动 pollMemStats。
func newQuota(limits QuotaLimits, cpuCores int, log *slog.Logger) *Quota {
	if log == nil {
		log = slog.Default()
	}
	conc := limits.MaxConcurrent
	if conc < 1 {
		conc = 1 // 防御性：sem 必须可写入
	}
	return &Quota{
		limits:   limits,
		cpuCores: cpuCores,
		sem:      make(chan struct{}, conc),
		log:      log.With("comp", "quota"),
	}
}

// Acquire 获取一个并发槽位。内存超限立即返回 ErrMemoryQuotaExceeded；
// 并发满则阻塞等待 Release 或 ctx 取消。获得槽位后二次检查内存（排队期间
// 可能已超限），超限则释放槽位并返回错误。
// 注意：内存检查基于 runtime.MemStats.Sys（进程已分配虚拟内存总量），
// 不是 RSS（OS 报告的物理内存）。Go runtime Sys 包含 heap + stacks + mspan
// 等元数据，是 Go 进程占用最稳定的指标。RSS 需读 /proc 或 OS API，跨平台
// 复杂度高， 用 Sys 近似。
func (q *Quota) Acquire(ctx context.Context, skillKey string) error {
	// 快速路径：内存已超限，立即拒绝（不占用槽位）。
	if err := q.checkMemLimit(skillKey, "pre-queue"); err != nil {
		return err
	}
	// 并发槽位 FIFO 排队。
	select {
	case q.sem <- struct{}{}:
		// 获得槽位。
	case <-ctx.Done():
		return ctx.Err()
	}
	q.active.Add(1)
	// 排队期间内存可能已超限，二次检查。
	if err := q.checkMemLimit(skillKey, "post-queue"); err != nil {
		<-q.sem
		q.active.Add(-1)
		return err
	}
	return nil
}

// checkMemLimit 检查当前 curMemMB 是否超限。超限记录告警（slog.Error 满足
//  验收标准"内存超限拒绝新 skill + slog.Error 含 skillKey/quota 字段"）
// 并返回 ErrMemoryQuotaExceeded；未超限返回 nil。phase 标识调用点
// （pre-queue / post-queue）便于日志区分。
func (q *Quota) checkMemLimit(skillKey, phase string) error {
	if q.limits.MemoryMB == 0 {
		return nil // 未配内存上限（理论不会，computeAdaptive 总返回 ≥512MB）
	}
	cur := q.curMemMB.Load()
	if cur <= q.limits.MemoryMB {
		return nil
	}
	q.log.Error("skill rejected: memory quota exceeded",
		slog.String("skill_key", skillKey),
		slog.String("quota", "memory"),
		slog.String("phase", phase),
		slog.Uint64("cur_mem_mb", cur),
		slog.Uint64("limit_mem_mb", q.limits.MemoryMB),
	)
	return ErrMemoryQuotaExceeded
}

// Release 释放一个并发槽位。dispatcher 在 skill Execute 返回（包括 error）
// 后必须调用；建议 `defer q.Release()`。
func (q *Quota) Release() {
	<-q.sem
	q.active.Add(-1)
}

// ActiveCount 返回当前持有的并发槽位数（用于 metrics / health 上报）。
func (q *Quota) ActiveCount() int64 {
	return q.active.Load()
}

// Limits 返回当前配额的静态配置（启动时计算，运行期不变）。
func (q *Quota) Limits() QuotaLimits {
	return q.limits
}

// pollMemStats 周期性采样 runtime.MemStats.Sys 更新 curMemMB。ctx 取消时退出。
func (q *Quota) pollMemStats(ctx context.Context) {
	sample := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		q.curMemMB.Store(ms.Sys / (1024 * 1024))
	}
	// 首次立即采样，避免启动后 30s 内 curMemMB=0。
	sample()
	ticker := time.NewTicker(memStatsPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample()
		}
	}
}

// computeAdaptive 根据机器 RAM/CPU 计算自适应配额。
// 纯函数，便于单元测试。totalRAM 单位是字节，返回的 MemoryMB 单位是 MB。
func computeAdaptive(totalRAM uint64, cpuCores int) QuotaLimits {
	// 内存：RAM × 8%，clamp 到 [512MB, 1.5GB]。
	mem := totalRAM * memRatioPercent / 100 / (1024 * 1024)
	if mem < memFloorMB {
		mem = memFloorMB
	}
	if mem > memCeilingMB {
		mem = memCeilingMB
	}
	// CPU：固定 10%；GOMAXPROCS 由调用方按
	// max(1, cores*cpuPercent/100) 计算。
	cpuPercent := cpuPercentAdaptive
	// 并发：= 核数，clamp 到 [2, 8]。
	conc := cpuCores
	if conc < concurrentFloor {
		conc = concurrentFloor
	}
	if conc > concurrentCeiling {
		conc = concurrentCeiling
	}
	return QuotaLimits{
		MemoryMB:      mem,
		CPUPercent:    cpuPercent,
		MaxConcurrent: conc,
	}
}

// applyConfigOverlay 将用户 config 覆盖合并到自适应默认值上。
// overlay 中 0 字段表示"未设"，保留 base；非 0 字段直接覆盖。
// 用户显式覆盖视为有意识决策（即使值 <floor 也不拒绝，但运行时日志会告警）。
func applyConfigOverride(base, overlay QuotaLimits) QuotaLimits {
	out := base
	if overlay.MemoryMB != 0 {
		out.MemoryMB = overlay.MemoryMB
	}
	if overlay.CPUPercent != 0 {
		out.CPUPercent = overlay.CPUPercent
	}
	if overlay.MaxConcurrent != 0 {
		out.MaxConcurrent = overlay.MaxConcurrent
	}
	return out
}

// defaultQuota 是包级单例，main.go 启动时调用 SetDefault 注入；dispatcher
// 默认走它。nil 时 Acquire/Release 是 no-op（向后兼容 ）。
var defaultQuota atomic.Pointer[Quota]

// SetDefault 设置包级默认 Quota。main.go 在启动 detectMachineSpec 后调用。
func SetDefault(q *Quota) {
	defaultQuota.Store(q)
}

// getDefault 返回包级默认 Quota，未设置时返回 nil。
func getDefault() *Quota {
	return defaultQuota.Load()
}

// InitFromEnv 是 main.go 启动时调的高层封装：detectMachineSpec →
// computeAdaptive → applyConfigOverride → SetDefault → 启动 pollMemStats。
// overlay 是用户通过 ONGRID_EDGE_QUOTA_* env vars 提供的覆盖值；
// 0 字段保留自适应默认。函数返回创建的 *Quota（便于 main.go 读 Limits() 上报）。
func InitFromEnv(ctx context.Context, overlay QuotaLimits, log *slog.Logger) *Quota {
	totalRAM, cpuCores := detectMachineSpec()
	adaptive := computeAdaptive(totalRAM, cpuCores)
	final := applyConfigOverride(adaptive, overlay)

	// GOMAXPROCS 间接限制 CPU（Go runtime 并发 OS 线程数）。
	// 严格说 GOMAXPROCS ≠ CPU 配额（Go 不真限使用率），但 dispatcher 通过
	// MaxConcurrentSkills 槽位限制 skill 并发，CPU 排队语义已实现。
	// ceil(cores * percent / 100) 避免 2~9 核机器被整数除法截断为 0；
	// clamp 到 [1, cpuCeiling] 满足  "最大 4 核" 承诺。
	rawProcs := (cpuCores*final.CPUPercent + 99) / 100 // integer ceil
	maxProcs := 1
	if rawProcs > 1 {
		maxProcs = rawProcs
	}
	if maxProcs > cpuCeiling {
		maxProcs = cpuCeiling
	}
	prev := runtime.GOMAXPROCS(maxProcs)

	q := newQuota(final, cpuCores, log)
	SetDefault(q)
	go q.pollMemStats(ctx)

	if log != nil {
		log.Info("quota initialized",
			slog.Uint64("total_ram_bytes", totalRAM),
			slog.Int("cpu_cores", cpuCores),
			slog.Uint64("mem_limit_mb", final.MemoryMB),
			slog.Int("cpu_percent", final.CPUPercent),
			slog.Int("max_procs", maxProcs),
			slog.Int("prev_max_procs", prev),
			slog.Int("max_concurrent_skills", final.MaxConcurrent),
			slog.Bool("user_overrode",
				overlay.MemoryMB != 0 || overlay.CPUPercent != 0 || overlay.MaxConcurrent != 0),
		)
	}
	return q
}

// testLogger 返回 slog.Default()，测试输出到 stderr。便于在测试失败时
// 看到被测代码的诊断日志（如 Acquire 拒绝路径的 slog.Error）。
// 测试包内可见（同包）。
func testLogger() *slog.Logger {
	return slog.Default()
}
