// Package edgedirs 定义 edge agent 部署目录约定（ADR-033 I2）。
//
// 这是跨 binary 共享的路径 source of truth：
//   - cmd/ongrid-edge（worker.exe / linux edge）的数据目录
//   - cmd/ongrid-edge-supervisor（supervisor.exe）的 binary / 数据目录
//
// 历史：原 cmd/ongrid-edge/paths_{linux,windows}.go 在本包建立前就存在
// （issue #3 build tag 骨架）。MVP-2 时 paths_*.go 迁移到 import 本包。
// 新代码（supervisor / worker 心跳写端）一律用本包。
package edgedirs

// 跨平台行为：每个平台专属文件（edgedirs_{linux,windows}.go）定义自己的
// BinDir / DataDir / PluginWorkDir / StageDir 常量。本文件不包含任何
// 平台相关定义，仅作包文档。
