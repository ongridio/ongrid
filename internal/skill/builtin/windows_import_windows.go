//go:build windows

// Blank import — 触发 windows 子包的 init() 注册所有 Windows builtin skill
// （event_log，以及未来的 services/processes/network/hotfix）。
// Linux 构建跳过此文件，因此 windows 子包在非 Windows 平台不会被导入。
package builtin

import (
	_ "github.com/ongridio/ongrid/internal/skill/builtin/windows"
)
