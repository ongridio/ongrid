// Package supervisorhealth 实现 supervisor.exe 与 worker.exe 之间的
// health.json 文件 IPC。
// worker.exe 每 30s 写一次心跳到 health.json，supervisor.exe 读 + 超时判断。
// 超过 HeartbeatTimeout（90s，3× 心跳间隔，给 GC/network jitter 留 margin）
// 未刷新 → supervisor 视为 worker 卡死，触发回滚/重启。
// 此包故意保持纯 Go（无 Windows 专属依赖），测试可在 L1 Linux CI 跑
//。
package supervisorhealth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWrite_Then_Read_RoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "health.json")

	want := Health{
		Version:       HealthSchemaVersion,
		WorkerPID:     12345,
		StartedAt:     time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
		LastHeartbeat: time.Date(2026, 7, 12, 10, 0, 30, 0, time.UTC),
		Status:        StatusHealthy,
	}

	if err := Write(path, want); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if got != want {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestRead_NotExist_Returns_Error(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Read(filepath.Join(dir, "missing.json"))
	if err == nil {
		t.Fatal("Read should fail for missing file")
	}
}

func TestRead_InvalidJSON_Returns_Error(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "health.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("setup WriteFile failed: %v", err)
	}
	_, err := Read(path)
	if err == nil {
		t.Fatal("Read should fail for invalid JSON")
	}
}

func TestWrite_Overwrite_Existing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "health.json")

	first := Health{Version: 1, WorkerPID: 1, Status: StatusStarting}
	if err := Write(path, first); err != nil {
		t.Fatalf("first Write failed: %v", err)
	}
	second := Health{Version: 1, WorkerPID: 2, Status: StatusHealthy}
	if err := Write(path, second); err != nil {
		t.Fatalf("second Write failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.WorkerPID != 2 || got.Status != StatusHealthy {
		t.Errorf("overwrite failed: got %+v, want PID=2 Status=healthy", got)
	}
}

func TestIsStale_FreshHeartbeat_False(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	h := Health{LastHeartbeat: now.Add(-10 * time.Second)}
	if IsStale(h, now, HeartbeatTimeout) {
		t.Error("IsStale should be false for 10s-old heartbeat (within 90s window)")
	}
}

func TestIsStale_ExpiredHeartbeat_True(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	h := Health{LastHeartbeat: now.Add(-120 * time.Second)} // 120s > 90s
	if !IsStale(h, now, HeartbeatTimeout) {
		t.Error("IsStale should be true for 120s-old heartbeat (beyond 90s window)")
	}
}

func TestIsStale_Boundary_ExactlyAtTimeout_True(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	// 超时判断用 > 比较：恰好等于 timeout 视为未超时（边界属于"最后的有效时刻"）
	h := Health{LastHeartbeat: now.Add(-HeartbeatTimeout)}
	if IsStale(h, now, HeartbeatTimeout) {
		t.Error("IsStale should be false at exactly timeout boundary (>)")
	}
	h2 := Health{LastHeartbeat: now.Add(-(HeartbeatTimeout + time.Second))}
	if !IsStale(h2, now, HeartbeatTimeout) {
		t.Error("IsStale should be true 1s past timeout boundary")
	}
}

func TestWrite_PermissionDenied_Returns_Error(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits behave differently on Windows; covered by L2 windows-latest runner")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission test unreliable")
	}
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}
	err := Write(filepath.Join(readOnlyDir, "health.json"), Health{Version: 1})
	if err == nil {
		t.Fatal("Write should fail on read-only directory")
	}
}
