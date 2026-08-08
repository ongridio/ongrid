//go:build linux

package edgedirs

// Linux 部署目录约定（FHS）。
//
// 注：Linux edge 没有 supervisor.exe（supervisor 是 Windows 专属）。
// 本文件仅为 cmd/ongrid-edge 未来 import 提供对称 API，supervisor 包
// 不会用到。
const (
	BinDir        = `/usr/local/bin`
	DataDir       = `/var/lib/ongrid-edge`
	PluginWorkDir = `/var/lib/ongrid-edge/plugins`
	StageDir      = `/var/lib/ongrid-edge/.upgrade`
)
