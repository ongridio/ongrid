// Package upgrademachine 实现 supervisor.exe 的升级状态机深模块（ADR-033 U3，issue #23）。
//
// # 状态机
//
// 升级状态机描述 worker 二进制文件的 swap → 健康确认 → 回滚 生命周期：
//
//	idle → pending → swapped → healthy
//	                   └→ rolled_back → idle
//
// 状态定义：
//
//   - StateIdle       — 无升级进行中（incoming/ 不存在，无 rollback.done）
//   - StatePending    — incoming/MANIFEST.txt 存在（worker 下载了新 bundle，等待 swap）
//   - StateSwapped    — 文件已替换，last_upgrade_ver 已写，等待健康确认
//   - StateHealthy    — healthy_marker 匹配 last_upgrade_ver（新 worker register_edge 成功）
//   - StateRolledBack — rollback.done 存在（旧版本已恢复，等待下次升级）
//
// # 状态转移触发
//
//	 idle → pending       DownloadBundle（worker 侧 upgradebundle 包）
//	 pending → swapped    Machine.Apply / Machine.BootCheck
//	 swapped → healthy    Machine.HealthCheck（poll IsUpgradeHealthy）
//	 swapped → rolled_back Machine.HealthCheck 超时 → Machine.RollbackAndMark
//	 any → rolled_back    Machine.BootCheck（启动时检测不健康）
//	 rolled_back → idle   manager 推新 bundle 前 DeleteRollbackSentinel
//
// # 文件系统 IPC
//
// supervisor 与 worker 通过 StageDir 下的文件通信（无 socket/pipe）：
//
//	<StageDir>/incoming/MANIFEST.txt  — pending 触发器
//	<StageDir>/incoming/VERSION       — bundle 版本号
//	<StageDir>/last_upgrade_ver       — 当前升级版本号
//	<StageDir>/last_upgrade_at        — 升级时间戳
//	<StageDir>/healthy_marker         — worker register_edge 成功后写的版本号
//	<StageDir>/rollback.done          — rollback 完成哨兵
//	<dest>.previous                   — swap 备份的旧文件
//
// 所有文件名常量定义在 ipc.go，是跨包单一真相源。
// upgradebundle（worker 侧）和 cmd/（supervisor 侧）均引用本包的常量。
package upgrademachine
