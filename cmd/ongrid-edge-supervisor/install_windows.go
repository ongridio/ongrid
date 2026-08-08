// install_windows.go 实现 supervisor.exe --install / --uninstall 子命令
//。
// --install --token <X> --cloud-addr <X> --access-key <X> [--collector-mode off] [--plugin-bin-dir <P>] [--plugin-work-dir <P>]:
//   1. DPAPI CryptProtectData(token) → secrets.enc
//   2. 验证 CryptUnprotectData(secrets.enc) 能还原
//   3. 清零明文 token 内存
//   4. sc.exe create → 启动服务
//   5. 写注册表 Environment MultiString（cloud_addr / access_key / collector_mode / plugin_*_dir）
// --uninstall:
//   1. sc.exe stop + delete（注册表 Environment 字段随服务键一起清除）
//   2. 删除 secrets.enc
// 安全：明文 token 仅在 CLI flag 时刻存在于内存，加密后立即清零。
// 不写日志/临时文件。Go string 不可变（无法真正清零 argv），但 []byte 可以。
// 注：access_key 暂以明文存于 Environment（与 R4 现状一致； 仅要求 SECRET_KEY
// 走 DPAPI，ACCESS_KEY 在现有  服务中也是明文环境变量）。
//  深化：runInstall 从 66 行单体函数拆为 ≤30 行 orchestrator，
// 3 个正交关注点委托给 install 包接口（SecretStore / ServiceController / EnvWriter）。

//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ongridio/ongrid/internal/edgeagent/edgedirs"
	"github.com/ongridio/ongrid/internal/edgeagent/install"
)

// secretsFileName 是 DPAPI 加密的 token 文件名。
const secretsFileName = "secrets.enc"

// serviceRegKeyPath 是 Windows Service 注册表路径（HKLM 前缀由 registry.OpenKey 自动加）。
const serviceRegKeyPath = `SYSTEM\CurrentControlSet\Services\` + serviceName

// installOptions 是 --install 子命令接收的所有参数。
type installOptions struct {
	// Token 是 broker SECRET_KEY（DPAPI 加密后存 secrets.enc）。必需。
	Token string
	// CloudAddr 是 frontier broker 地址（host:port）。必需。
	CloudAddr string
	// AccessKey 是 broker ACCESS_KEY（明文存服务 Environment）。必需。
	AccessKey string
	// CollectorMode 是 node_* 采集模式（off / all），默认 off。
	CollectorMode string
	// PluginBinDir / PluginWorkDir 可选，缺省由 edgedirs 默认值决定。
	PluginBinDir  string
	PluginWorkDir string
}

// Validate 校验必需字段并应用默认值。
func (o *installOptions) Validate() error {
	if o.Token == "" {
		return fmt.Errorf("--token <X> is required")
	}
	if o.CloudAddr == "" {
		return fmt.Errorf("--cloud-addr <host:port> is required")
	}
	if o.AccessKey == "" {
		return fmt.Errorf("--access-key <X> is required")
	}
	if o.CollectorMode == "" {
		o.CollectorMode = "off"
	}
	if o.PluginBinDir == "" {
		o.PluginBinDir = edgedirs.BinDir
	}
	if o.PluginWorkDir == "" {
		o.PluginWorkDir = edgedirs.PluginWorkDir
	}
	return nil
}

// envPairs 返回写入服务 Environment 字段的 KEY=VALUE 多字符串数组。
// 顺序固定，便于人工排查与重装比对。
func (o *installOptions) envPairs() []string {
	return []string{
		"ONGRID_EDGE_CLOUD_ADDR=" + o.CloudAddr,
		"ONGRID_EDGE_ACCESS_KEY=" + o.AccessKey,
		// SECRET_KEY 由 secrets.enc 提供（DPAPI），不写入 Environment
		"ONGRID_EDGE_COLLECTOR_MODE=" + o.CollectorMode,
		"ONGRID_EDGE_PLUGIN_BIN_DIR=" + o.PluginBinDir,
		"ONGRID_EDGE_PLUGIN_WORK_DIR=" + o.PluginWorkDir,
		// secrets.enc 路径（让 worker main.go 知道从哪加载 DPAPI token）
		"ONGRID_EDGE_SECRETS_FILE=" + filepath.Join(edgedirs.DataDir, secretsFileName),
	}
}

// runInstall 执行 --install 流程，编排接口的调用顺序。
// 编排顺序：
//  1. SecretStore.Install — DPAPI 加密 + round-trip 验证（内部自清理）
//  2. ServiceController.Create — sc.exe create（失败时回滚凭证）
//  3. ServiceController.ConfigureDefenderExclusion — Add-MpPreference
//  4. ServiceController.ConfigureRecovery — sc.exe failure
//  5. EnvWriter.Write — registry Environment（失败不回滚服务）
//  6. ServiceController.Start — sc.exe start（失败仅告警，不报错）
func runInstall(
	log *slog.Logger, opts installOptions,
	ss install.SecretStore, sc install.ServiceController, ew install.EnvWriter,
) error {
	if err := opts.Validate(); err != nil {
		return err
	}

	// 将 token 转为 []byte 一次，全链路复用，defer 清零
	// Go string 不可变（argv 残留到进程退出），但 []byte 副本可以清零
	tokenBytes := []byte(opts.Token)
	defer zeroBytes(tokenBytes)

	// 确保 data 目录存在
	if err := os.MkdirAll(edgedirs.DataDir, 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	// 1. 凭证（DPAPI 加密 + 验证；失败时 Install 内部自清理）
	if err := ss.Install(tokenBytes); err != nil {
		return err
	}

	// 2. 服务注册（失败时回滚凭证）
	exePath, err := os.Executable()
	if err != nil {
		_ = ss.Remove()
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if err := sc.Create(exePath); err != nil {
		_ = ss.Remove()
		return fmt.Errorf("create service: %w", err)
	}

	// 3. Defender exclusion
	if err := sc.ConfigureDefenderExclusion(); err != nil {
		log.Warn("failed to configure Windows Defender exclusion (non-fatal; AV may interfere with supervisor self-swap)",
			slog.String("err", err.Error()))
	}

	// 4. SCM failure recovery
	if err := sc.ConfigureRecovery(); err != nil {
		log.Warn("failed to configure SCM failure recovery (non-fatal; supervisor won't auto-restart on crash)",
			slog.String("err", err.Error()))
	}

	// 5. 环境配置（失败不回滚服务 — 由调用方决定是否 --uninstall）
	if err := ew.Write(opts.envPairs()); err != nil {
		log.Warn("service created but failed to set Environment; manual cleanup needed",
			slog.String("err", err.Error()))
		return fmt.Errorf("write service Environment: %w", err)
	}

	// 6. 启动（失败仅告警，不阻断 install）
	if err := sc.Start(); err != nil {
		log.Warn("service created but failed to start",
			slog.String("err", err.Error()))
	}

	log.Info("install completed",
		slog.String("binary", exePath),
		slog.String("cloud_addr", opts.CloudAddr))
	return nil
}

// runUninstall 执行 --uninstall 流程。
func runUninstall(log *slog.Logger, sc install.ServiceController, ss install.SecretStore) error {
	// 1. 停止服务（忽略 "服务未运行" 错误）
	_ = sc.Stop()

	// 2. 删除服务（注册表 Environment 字段随服务键一起删除）
	if err := sc.Delete(); err != nil {
		return err
	}

	// 3. 删除 secrets.enc
	if err := ss.Remove(); err != nil {
		log.Warn("failed to remove secrets.enc", slog.String("err", err.Error()))
	}

	log.Info("uninstall completed")
	return nil
}

// zeroBytes 清零 byte slice（SecureString cleanup）。
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// parseInstallOptions 从 args 中提取所有 --install 子命令参数。
// 支持两种格式：--flag <VALUE> 和 --flag=<VALUE>。
func parseInstallOptions(args []string) installOptions {
	var opts installOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		// 提取 --flag 或 --flag=VALUE
		flag, inlineVal, hasInline := strings.Cut(a, "=")
		// 取值：优先 inline，否则看下一个 arg
		getValue := func() string {
			if hasInline {
				return inlineVal
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch flag {
		case "--token":
			opts.Token = getValue()
		case "--cloud-addr":
			opts.CloudAddr = getValue()
		case "--access-key":
			opts.AccessKey = getValue()
		case "--collector-mode":
			opts.CollectorMode = getValue()
		case "--plugin-bin-dir":
			opts.PluginBinDir = getValue()
		case "--plugin-work-dir":
			opts.PluginWorkDir = getValue()
		}
	}
	return opts
}
