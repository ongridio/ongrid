// state.go 定义升级状态机的 State 类型 + 状态检测函数（issue #23）。
//
// 状态机使原先分散在 7 文件中的隐式状态（通过文件存在性 + sentinel error 推断）
// 变为显式的、可检测的、可文档化的类型。

package upgrademachine

import (
	"fmt"
	"os"
)

// State 表示升级状态机的当前状态。
type State int

const (
	// StateIdle 无升级进行中（incoming/ 不存在，无 last_upgrade_ver）。
	StateIdle State = iota

	// StatePending incoming/MANIFEST.txt 存在（bundle 已下载，等待 swap）。
	StatePending

	// StateSwapped 文件已替换（last_upgrade_ver 已写），等待健康确认。
	// 此状态通过 "last_upgrade_ver 存在但 healthy_marker 不匹配" 检测。
	StateSwapped

	// StateHealthy healthy_marker 内容匹配 last_upgrade_ver（升级成功）。
	StateHealthy

	// StateRolledBack rollback.done 存在（旧版本已恢复，等待下次升级）。
	StateRolledBack
)

// String 返回状态的人类可读名称。
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StatePending:
		return "pending"
	case StateSwapped:
		return "swapped"
	case StateHealthy:
		return "healthy"
	case StateRolledBack:
		return "rolled_back"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// DetectState 根据 StageDir 下的文件状态推断当前升级状态。
//
// 这是一个公共观测函数，供调试、日志、测试断言使用。
// Machine 内部编排逻辑使用更细粒度的检测函数（IsPending、IsUpgradeHealthy 等）
// 而非 DetectState，因为编排需要区分具体条件（如 rollback.done vs pending 的优先级）。
//
// 检测优先级（互斥）：
//  1. rollback.done 存在 → StateRolledBack
//  2. incoming/MANIFEST.txt 存在 → StatePending
//  3. last_upgrade_ver 不存在 → StateIdle
//  4. healthy_marker 匹配 last_upgrade_ver → StateHealthy
//  5. last_upgrade_ver 存在但 healthy_marker 不匹配 → StateSwapped
func DetectState(stageDir string) State {
	// 1. rollback.done → 已回滚
	if _, err := os.Stat(RollbackDonePath(stageDir)); err == nil {
		return StateRolledBack
	}

	// 2. incoming/MANIFEST.txt → 有 pending
	if _, err := os.Stat(ManifestPath(stageDir)); err == nil {
		return StatePending
	}

	// 3. 无 last_upgrade_ver → 从未升级
	lastVer := readTrimmed(LastUpgradeVerPath(stageDir))
	if lastVer == "" {
		return StateIdle
	}

	// 4. healthy_marker 匹配 → 健康
	healthyVer := readTrimmed(HealthyMarkerPath(stageDir))
	if lastVer == healthyVer {
		return StateHealthy
	}

	// 5. last_upgrade_ver 存在但不匹配 → swapped（等待确认）
	return StateSwapped
}
