//go:build !windows

package logs

// promtailBinaryName 是 Linux 平台的 promtail binary 文件名。
// 用于 plugin.go 中 filepath.Join(binDir, promtailBinaryName)。
// Windows 版本见 binary_name_windows.go。
const promtailBinaryName = "promtail"
