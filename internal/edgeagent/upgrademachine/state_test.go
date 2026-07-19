package upgrademachine

import (
	"path/filepath"
	"testing"
)

func TestDetectState_Idle(t *testing.T) {
	dir := t.TempDir()
	// 空目录 → 从未升级
	if s := DetectState(dir); s != StateIdle {
		t.Errorf("DetectState() = %s, want idle", s)
	}
}

func TestDetectState_Pending(t *testing.T) {
	dir := t.TempDir()
	// 创建 incoming/MANIFEST.txt
	writeTestFile(t, filepath.Join(dir, IncomingDirName, ManifestFileName), "fake manifest")
	if s := DetectState(dir); s != StatePending {
		t.Errorf("DetectState() = %s, want pending", s)
	}
}

func TestDetectState_RolledBack(t *testing.T) {
	dir := t.TempDir()
	// rollback.done 存在（优先级高于 pending）
	writeTestFile(t, filepath.Join(dir, RollbackDoneFile), "done")
	if s := DetectState(dir); s != StateRolledBack {
		t.Errorf("DetectState() = %s, want rolled_back", s)
	}
}

func TestDetectState_RolledBack_HigherThanPending(t *testing.T) {
	dir := t.TempDir()
	// 同时存在 rollback.done 和 incoming/MANIFEST.txt → rollback.done 优先
	writeTestFile(t, filepath.Join(dir, RollbackDoneFile), "done")
	writeTestFile(t, filepath.Join(dir, IncomingDirName, ManifestFileName), "fake manifest")
	if s := DetectState(dir); s != StateRolledBack {
		t.Errorf("DetectState() = %s, want rolled_back (higher priority than pending)", s)
	}
}

func TestDetectState_Healthy(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v1.0.0")
	writeTestFile(t, filepath.Join(dir, HealthyMarkerFile), "v1.0.0")
	if s := DetectState(dir); s != StateHealthy {
		t.Errorf("DetectState() = %s, want healthy", s)
	}
}

func TestDetectState_Swapped(t *testing.T) {
	dir := t.TempDir()
	// last_upgrade_ver 存在但 healthy_marker 不匹配
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v2.0.0")
	writeTestFile(t, filepath.Join(dir, HealthyMarkerFile), "v1.0.0")
	if s := DetectState(dir); s != StateSwapped {
		t.Errorf("DetectState() = %s, want swapped", s)
	}
}

func TestDetectState_Swapped_NoMarker(t *testing.T) {
	dir := t.TempDir()
	// last_upgrade_ver 存在，无 healthy_marker
	writeTestFile(t, filepath.Join(dir, LastUpgradeVerFile), "v2.0.0")
	if s := DetectState(dir); s != StateSwapped {
		t.Errorf("DetectState() = %s, want swapped", s)
	}
}

func TestState_String(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{StateIdle, "idle"},
		{StatePending, "pending"},
		{StateSwapped, "swapped"},
		{StateHealthy, "healthy"},
		{StateRolledBack, "rolled_back"},
		{State(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("State(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}
