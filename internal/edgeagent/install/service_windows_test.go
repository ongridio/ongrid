//go:build windows

package install

import (
	"strings"
	"testing"
)

// TestBuildDefenderExclusionCmd_SingleParamName 验证 -ExclusionPath 参数名只出现一次。
// 回归防护：旧代码用 `-ExclusionPath A -ExclusionPath B` 会导致 PowerShell
// ParameterAlreadyBound 错误（#21 dogfood 2026-07-16 发现）。
func TestBuildDefenderExclusionCmd_SingleParamName(t *testing.T) {
	cmd := buildDefenderExclusionCmd(`C:\bin`, `C:\data`)
	if count := strings.Count(cmd, "-ExclusionPath"); count != 1 {
		t.Errorf("-ExclusionPath should appear exactly once, got %d (cmd: %s)", count, cmd)
	}
}

// TestBuildDefenderExclusionCmd_CommaSeparated 验证两个路径用逗号分隔。
// PowerShell 数组参数语法：`-ParamName "val1","val2"`。
func TestBuildDefenderExclusionCmd_CommaSeparated(t *testing.T) {
	binDir := `C:\Program Files\ongrid-edge\bin`
	dataDir := `C:\ProgramData\ongrid-edge`
	cmd := buildDefenderExclusionCmd(binDir, dataDir)

	if !strings.Contains(cmd, `"`+binDir+`","`+dataDir+`"`) {
		t.Errorf("expected comma-separated paths in command, got: %s", cmd)
	}
}

// TestBuildDefenderExclusionCmd_BothPathsPresent 验证两个目录都出现在命令中。
func TestBuildDefenderExclusionCmd_BothPathsPresent(t *testing.T) {
	binDir := `C:\test\bin`
	dataDir := `C:\test\data`
	cmd := buildDefenderExclusionCmd(binDir, dataDir)

	if !strings.Contains(cmd, binDir) {
		t.Errorf("binDir missing from command: %s", cmd)
	}
	if !strings.Contains(cmd, dataDir) {
		t.Errorf("dataDir missing from command: %s", cmd)
	}
}

// TestBuildDefenderExclusionCmd_AddMpPreferencePresent 验证命令前缀正确。
func TestBuildDefenderExclusionCmd_AddMpPreferencePresent(t *testing.T) {
	cmd := buildDefenderExclusionCmd(`C:\a`, `C:\b`)
	if !strings.HasPrefix(cmd, "Add-MpPreference ") {
		t.Errorf("command should start with Add-MpPreference, got: %s", cmd)
	}
	if !strings.Contains(cmd, "-ErrorAction SilentlyContinue") {
		t.Errorf("command should contain -ErrorAction SilentlyContinue, got: %s", cmd)
	}
}
