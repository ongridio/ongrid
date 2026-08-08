// Command ongrid-edge-supervisor 是 Windows 版 Edge Agent 的 supervisor 进程
// （ADR-033 U3 父子进程架构）。
//
// MVP-1 职责（issue #4）：
//   - Windows Service 入口（golang.org/x/sys/windows/svc）
//   - 启动 + 监控 worker.exe（cmd/ongrid-edge 编译产物）
//   - 健康感知（health.json 文件 IPC，30s 心跳窗口）
//
// MVP-2（issue #9）：
//   - ✅ --install / --uninstall 子命令 + DPAPI 加密 token
//   - ✅ token 90 天轮转检查（edge 端）
//   - ❌ bundle upgrade staging + swap + rollback（MVP-3）
//
// 整个包 //go:build windows（Linux 不编译，对称 cmdpolicy 的 //go:build linux）。
// health 逻辑在 internal/edgeagent/supervisorhealth 包，跨平台可测。

//go:build windows

package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"

	"github.com/ongridio/ongrid/internal/edgeagent/edgedirs"
	"github.com/ongridio/ongrid/internal/edgeagent/install"
)

const serviceName = "ongrid-edge"

// version 是编译期版本号（Makefile -ldflags "-X main.version=v$(VERSION)" 注入）。
// SupervisorSelfSwap.smokeTestVersion 跑 `supervisor.exe.new --version` 验证
// binary 可执行 + 版本非空（W5 加固，防止坏 binary 进入 rename-aside）。
var version = "dev"

func main() {
	// Windows Service 模式下 os.Stderr 写入 invalid handle 返回 error，
	// io.MultiWriter 遇到第一个 writer 失败就 short-circuit 不写后续 writer。
	// 因此 logFile 必须排在 os.Stderr 之前，确保文件一定拿到日志。
	_ = os.MkdirAll(edgedirs.DataDir, 0o755)
	logFile, logErr := os.OpenFile(filepath.Join(edgedirs.DataDir, "supervisor.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	var w io.Writer = os.Stderr
	if logErr == nil {
		w = io.MultiWriter(logFile, os.Stderr)
	}
	log := slog.New(slog.NewJSONHandler(w, nil))

	// --install / --uninstall 子命令（ADR-037 A2 CR4）
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--install":
			opts := parseInstallOptions(os.Args[2:])
			if err := opts.Validate(); err != nil {
				log.Error("--install 参数错误", "err", err)
				fmt.Fprintf(os.Stderr, "Usage: ongrid-edge-supervisor.exe --install --token <X> --cloud-addr <host:port> --access-key <X> [--collector-mode off] [--plugin-bin-dir <P>] [--plugin-work-dir <P>]\n")
				os.Exit(2)
			}
			if err := runInstall(log, opts,
					install.NewSecretStore(filepath.Join(edgedirs.DataDir, secretsFileName)),
					install.NewServiceController(serviceName),
					install.NewEnvWriter(serviceRegKeyPath),
				); err != nil {
				log.Error("install failed", "err", err)
				os.Exit(1)
			}
			return
		case "--uninstall":
			if err := runUninstall(log,
					install.NewServiceController(serviceName),
					install.NewSecretStore(filepath.Join(edgedirs.DataDir, secretsFileName)),
				); err != nil {
				log.Error("uninstall failed", "err", err)
				os.Exit(1)
			}
			return
		case "--version", "-v":
			fmt.Fprintf(os.Stdout, "ongrid-edge-supervisor %s\n", version)
			return
		case "--help", "-h":
			fmt.Fprintf(os.Stdout, "Usage:\n")
			fmt.Fprintf(os.Stdout, "  ongrid-edge-supervisor.exe --install --token <X> --cloud-addr <host:port> --access-key <X> [--collector-mode off] [--plugin-bin-dir <P>] [--plugin-work-dir <P>]\n")
			fmt.Fprintf(os.Stdout, "  ongrid-edge-supervisor.exe --uninstall\n")
			fmt.Fprintf(os.Stdout, "  ongrid-edge-supervisor.exe (run as Windows Service)\n")
			return
		}
	}

	isSvc, err := svc.IsWindowsService()
	if err != nil {
		log.Error("detect windows service context failed", "err", err)
		os.Exit(1)
	}

	if !isSvc {
		// 开发调试模式：直接跑 worker（不通过 SCM）。
		log.Info("running in interactive mode; starting worker directly (dev mode)")
		if err := runWorkerOnly(log); err != nil {
			log.Error("interactive worker exited with error", "err", err)
			os.Exit(1)
		}
		return
	}

	h := &serviceHandler{log: log}
	if err := svc.Run(serviceName, h); err != nil {
		log.Error("service Run failed", "err", err)
		os.Exit(1)
	}
}
