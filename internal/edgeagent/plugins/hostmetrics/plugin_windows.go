// plugin_windows.go 是 Windows 平台的 hostmetrics plugin 实现。
// subprocess windows_exporter.exe 暴露 Prometheus 指标端点。
// 安全约束（对称 ule 1）：
//   - collector 白名单 hardcode（不接受 PluginConfig.Spec 输入）
//   - 采集端口固定 127.0.0.1:9182（localhost only，不暴露外部）
//   - ConfigRender=nil — windows_exporter 是 CLI-driven（对称 node_exporter）
// 复用 SubprocessPlugin 获得：crash restart 指数退避 + stdout/stderr 日志捕获 +
// Plugin interface 合规 + supervisor 健康报告集成。
//  不做：动态 collector 配置 / TLS / Remote Write / Scrape()。
//  接线时决定 metrics 流向（edge-side scrape + tunnel push 或直接暴露端口）。

//go:build windows

package hostmetrics

import (
	"log/slog"
	"path/filepath"

	"github.com/ongridio/ongrid/internal/edgeagent/plugins"
)

// collectorWhitelist 是 windows_exporter 启用的 collector 白名单（hardcode）。
// 对应  第一批 5 skill 的 RCA 需求：
//   - cpu / system / process：CPU 上下文切换 / 系统负载 / 进程异常
//   - logical_disk / net：磁盘 I/O / 网络连接异常
//   - os / service：操作系统信息 / Windows 服务状态
// 注：旧版 wmi_exporter 的 `cs` collector 在 windows_exporter 0.27+ 已移除，
// 相关指标由 `system` collector 覆盖。
const collectorWhitelist = "cpu,logical_disk,net,os,service,system,process"

// defaultListenAddress 是 windows_exporter 指标端点监听地址。
// 固定 127.0.0.1:9182（localhost only，不暴露外部）。
const defaultListenAddress = "127.0.0.1:9182"

// New 构造 Windows hostmetrics plugin。binDir 是 windows_exporter.exe 所在目录，
// workDir 是 plugin 工作目录。
func New(binDir, workDir string, log *slog.Logger) plugins.Plugin {
	if log == nil {
		log = slog.Default()
	}
	pluginWorkDir := filepath.Join(workDir, Name)
	return plugins.NewSubprocess(plugins.SubprocessOpts{
		Name:    Name,
		Binary:  filepath.Join(binDir, "windows_exporter.exe"),
		WorkDir: pluginWorkDir,
		// ConfigFile 路径存在（Supervisor 会创建 workdir），
		// 但 ConfigRender=nil → 不写文件（对称 Linux node_exporter）。
		ConfigFile:   filepath.Join(pluginWorkDir, "spec.snapshot"),
		ConfigRender: nil,
		Args: func(_ plugins.PluginConfig, _ string) []string {
			return []string{
				"--collectors.enabled", collectorWhitelist,
				"--web.listen-address", defaultListenAddress,
			}
		},
		Log: log,
	})
}
