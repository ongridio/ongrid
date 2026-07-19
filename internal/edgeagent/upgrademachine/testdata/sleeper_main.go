// sleeper_main.go 是测试辅助程序：启动后持续运行直到被 kill。
// 用于模拟 windows_exporter.exe 等长期运行的 plugin 进程。
// 通过持有自身 .exe 文件锁，模拟 issue #20 的核心场景：
// worker 退出后 plugin 进程 orphaned → .exe 文件锁阻塞 rename。
package main

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// 捕获 Ctrl-Break（taskkill /T 默认发送），但忽略它模拟"被 orphaned 后无法触达"
	// 实际生产中 windows_exporter 不响应 taskkill /T（不在 worker 的进程树）
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// 真正的 orphaned 模拟：只接受 SIGKILL（taskkill /F）
	go func() {
		<-sigCh
		// 什么都不做 — 模拟进程不响应优雅信号
	}()

	// 持续运行直到被强制 kill
	for {
		time.Sleep(time.Hour)
	}
}
