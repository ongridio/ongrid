//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 完成旧 root hook 到新 hook 的一次性自举。配置未就绪时不注册，
// 防止 Manager 将第一阶段的版本上报误判为完整升级成功。
func prepareSystemdUpgrade(ctx context.Context, stage, version string) (bool, error) {
	initName, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(initName)) != "systemd" {
		return false, nil
	}
	if stage == "" {
		stage = "/var/lib/ongrid-edge/.upgrade"
	}
	if !upgradeVersionMatches(stage, "last_upgrade_ver", version) {
		return false, nil
	}
	if _, err := os.Stat(filepath.Join(stage, "last_upgrade_at")); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(checkCtx, "systemctl", "show", "ongrid-edge.service", "-p", "MainPID", "-p", "ProtectSystem", "-p", "ProtectHome", "-p", "PrivateTmp").Output()
	if err != nil {
		return false, fmt.Errorf("read systemd upgrade state: %w", err)
	}
	props := parseSystemdProperties(string(raw))
	// 不影响手工运行、容器或其他 unit 的进程。
	if props["MainPID"] != strconv.Itoa(os.Getpid()) {
		return false, nil
	}
	return prepareSystemdUpgradeState(stage, version, props)
}
func upgradeVersionMatches(stage, name, version string) bool {
	raw, err := os.ReadFile(filepath.Join(stage, name))
	return err == nil && strings.TrimSpace(string(raw)) == version
}
func parseSystemdProperties(raw string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props
}
func prepareSystemdUpgradeState(stage, version string, props map[string]string) (bool, error) {
	if upgradeVersionMatches(stage, "systemd_config_ready", version) {
		for _, key := range []string{"ProtectSystem", "ProtectHome", "PrivateTmp"} {
			if props[key] != "no" && props[key] != "false" {
				return false, fmt.Errorf("systemd upgrade configuration not effective: %s=%s", key, props[key])
			}
		}
		return false, nil
	}
	// 原子写入请求；正常退出后 Restart=always 会先运行新的 root hook。
	path := filepath.Join(stage, "systemd_bootstrap_request")
	if err := os.WriteFile(path+".tmp", []byte(version+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("stage systemd bootstrap: %w", err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return false, fmt.Errorf("commit systemd bootstrap: %w", err)
	}
	return true, nil
}
