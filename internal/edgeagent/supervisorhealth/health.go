// Package supervisorhealth 实现 supervisor.exe 与 worker.exe 之间的
// health.json 文件 IPC。
// worker.exe 每 30s 写一次心跳到 health.json，supervisor.exe 读 + 超时判断。
// 超过 HeartbeatTimeout（90s，3× 心跳间隔，给 GC/network jitter 留 margin）
// 未刷新 → supervisor 视为 worker 卡死，触发回滚/重启。
// 此包故意保持纯 Go（无 Windows 专属依赖），测试可在 L1 Linux CI 跑
//。
package supervisorhealth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// HealthSchemaVersion 是 health.json schema 的当前版本。
// 字段增删或语义变更时 +1，supervisor 读到不兼容版本应拒绝启动 worker。
const HealthSchemaVersion = 1

// HeartbeatInterval 是 worker.exe 写心跳的频率。
const HeartbeatInterval = 30 * time.Second

// HeartbeatTimeout 是 supervisor.exe 视为 worker 卡死的阈值。
// 设为 3× HeartbeatInterval 给 GC / 临时文件锁 / antivirus 扫描留 margin，
// 避免误杀健康 worker。
const HeartbeatTimeout = 90 * time.Second

// Status 枚举 worker 自报的健康状态。supervisor 主要靠 LastHeartbeat 时间戳
// 做超时判断；Status 是辅助信号（如 worker 启动中但还没就绪）。
type Status string

const (
	// StatusStarting 表示 worker 进程已启动但 skill dispatcher 未就绪。
	StatusStarting Status = "starting"
	// StatusHealthy 表示 worker 正常运行。
	StatusHealthy Status = "healthy"
	// StatusDegraded 表示 worker 自报部分 skill 不可用（如 windows_exporter 挂了）。
	//  supervisor 不对 degraded 做特殊处理，仅记录。
	StatusDegraded Status = "degraded"
)

// Health 是 health.json 文件的 schema。worker 写，supervisor 读。
// 字段对齐 ：worker_pid 用于 supervisor 检测 PID race（旧 worker
// 崩溃后新 worker 复用 PID 槽位但 started_at 不同）；started_at 与
// last_heartbeat 都是 RFC3339 纳秒精度。
type Health struct {
	Version       int       `json:"version"`
	WorkerPID     int       `json:"worker_pid"`
	StartedAt     time.Time `json:"started_at"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Status        Status    `json:"status"`
}

// Write 原子地写入 health.json：先写到同目录的临时文件，再 rename 覆盖。
// 原子写避免 supervisor 读到半写入的 JSON（Windows 上 rename 是原子的，
// 同分区下 NTFS保证原子性）。
func Write(path string, h Health) error {
	buf, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal health: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	// 同目录临时文件保证 rename 是同分区原子操作（跨分区 rename 退化成 copy+unlink）。
	tmp, err := os.CreateTemp(dir, ".health-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			// cleanup 路径：tmp 文件已写坏或 rename 失败，删除残留。
			// 错误丢弃是刻意的（cleanup 失败无处理手段，下次 CreateTemp 会冲突触发上层报错）。
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(buf); err != nil {
		// Write 失败后 Close 错误丢弃（已经被 write 错误掩盖，无诊断价值）。
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	cleanup = false
	return nil
}

// Read 读取 health.json 并反序列化。文件不存在时返回包装 fs.ErrNotExist 的错误。
func Read(path string) (Health, error) {
	var h Health
	buf, err := os.ReadFile(path)
	if err != nil {
		return h, fmt.Errorf("read %s: %w", path, err)
	}
	// 提前 trim BOM（有些编辑器在 Windows 自动加 BOM，会让 encoding/json 报错）。
	buf = bytes.TrimPrefix(buf, []byte("\xef\xbb\xbf"))
	if err := json.Unmarshal(buf, &h); err != nil {
		return h, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return h, nil
}

// IsStale 判断心跳是否过期：now - LastHeartbeat > timeout。
// 边界恰好等于 timeout 视为未过期（属于"最后的有效时刻"）。
// 错误的判断由调用方做（文件不存在 ≠ worker 卡死，可能是首次启动）。
func IsStale(h Health, now time.Time, timeout time.Duration) bool {
	return now.Sub(h.LastHeartbeat) > timeout
}

// IsNotExist 报告错误链中是否包含 fs.ErrNotExist。supervisor 用这个区分
// "health.json 还没写"（worker 启动中，等待）vs "health.json 损坏"（告警）。
func IsNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
