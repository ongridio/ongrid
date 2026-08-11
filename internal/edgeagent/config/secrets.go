// Package config 提供 edge agent 的运行时配置加载逻辑，
// 包括 DPAPI 加密的 secrets.enc 读取（ADR-037 A2 CR4）。
package config

import (
	"fmt"
	"os"

	"github.com/ongridio/ongrid/internal/edgeagent/dpapi"
)

// LoadSecrets 读取 secrets.enc 文件并使用 DPAPI 解密 broker token。
//
// 流程：os.ReadFile → dpapi.Unprotect → 返回明文 token。
//
// 返回值：
//   - (token, nil)：成功解密
//   - ("", err)：文件不存在 / 解密失败 / token 为空
//
// 调用方应在失败时回退到 ONGRID_EDGE_SECRET_KEY 环境变量（向后兼容）。
func LoadSecrets(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secrets: read %s: %w", path, err)
	}

	plaintext, err := dpapi.Unprotect(data)
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt: %w", err)
	}

	token := string(plaintext)
	if token == "" {
		return "", fmt.Errorf("secrets: decrypted token is empty")
	}

	return token, nil
}
