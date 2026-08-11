//go:build windows

package logs

// promtailBinaryName 是 Windows 平台的 promtail binary 文件名（含 .exe 后缀）。
// 用于 plugin.go 中 filepath.Join(binDir, promtailBinaryName)。
// Linux 版本见 binary_name_linux.go。
const promtailBinaryName = "promtail.exe"
