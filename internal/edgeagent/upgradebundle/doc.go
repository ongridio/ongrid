// Package upgradebundle 实现 worker 侧的 bundle 下载与健康标记写入。
// 职责分工（对称 supervisor 侧的 upgrademachine 包）：
//   - upgradebundle（本包，worker 侧）— 下载 bundle 到 incoming/ + 写 healthy_marker
//   - upgrademachine（supervisor 侧）— swap + rollback + 状态机编排 + IPC 常量单一真相源
// 文件系统 IPC（常量定义在 upgrademachine 包，两侧共享引用）：
//   <StageDir>/incoming/MANIFEST.txt — 存在 = pending upgrade 触发器
//   <StageDir>/incoming/VERSION     — bundle 版本号
//   <StageDir>/healthy_marker       — register_edge 成功后写的版本号
//   <StageDir>/rollback.done        — rollback 完成 sentinel（本包负责清理）
// 本包纯 Go（无 Windows 专属依赖），L1 Linux CI 可测。
package upgradebundle
