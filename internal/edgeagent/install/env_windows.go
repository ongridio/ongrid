//go:build windows

package install

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// RegistryEnvWriter 是 EnvWriter 的 Windows 实现，
// 通过注册表写服务 Environment 字段（REG_MULTI_SZ）。
type RegistryEnvWriter struct {
	regPath string
}

// NewEnvWriter 创建注册表 EnvWriter。
// regPath 是服务注册表路径（如 `SYSTEM\CurrentControlSet\Services\ongrid-edge`）。
func NewEnvWriter(regPath string) EnvWriter {
	return &RegistryEnvWriter{regPath: regPath}
}

// Write 写入 KEY=VALUE 多字符串到注册表 Environment 字段。
// 在 ServiceController.Create 之后调用（服务键此时已存在）。
func (w *RegistryEnvWriter) Write(pairs []string) error {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, w.regPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open service registry key: %w", err)
	}
	defer key.Close()

	if err := key.SetStringsValue("Environment", pairs); err != nil {
		return fmt.Errorf("set Environment MultiString: %w", err)
	}
	return nil
}
