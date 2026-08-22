//go:build windows

package main

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// TestMergedServiceEnv_DedupCaseInsensitive 验证 base env + service
// Environment 字段合并 + 大小写去重（Windows env case-insensitive）。
// service pair ONGRID_EDGE_ACCESS_KEY=new 应覆盖 base 中 lowercase 同名 pair，
// 且最终输出不出现两条 ACCESS_KEY=。
func TestMergedServiceEnv_DedupCaseInsensitive(t *testing.T) {
	oldReader := serviceEnvReader
	serviceEnvReader = func() ([]string, error) {
		return []string{
			"ONGRID_EDGE_ACCESS_KEY=new-value",
			"ONGRID_EDGE_CLOUD_ADDR=frontier.example.com:16667",
		}, nil
	}
	defer func() { serviceEnvReader = oldReader }()

	oldEnviron := environFunc
	environFunc = func() []string {
		return []string{
			"PATH=/usr/bin",
			"ongrid_edge_access_key=old-value", // lowercase key, same env var
			"SYSTEMROOT=C:\\Windows",
		}
	}
	defer func() { environFunc = oldEnviron }()

	got := mergedServiceEnv()

	seen := make(map[string]string, len(got))
	for _, kv := range got {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		seen[strings.ToUpper(k)] = v
	}
	if seen["ONGRID_EDGE_ACCESS_KEY"] != "new-value" {
		t.Errorf("ACCESS_KEY = %q; want new-value (service should override base lowercase)", seen["ONGRID_EDGE_ACCESS_KEY"])
	}
	if seen["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q; want /usr/bin (base preserved)", seen["PATH"])
	}
	if seen["ONGRID_EDGE_CLOUD_ADDR"] != "frontier.example.com:16667" {
		t.Errorf("CLOUD_ADDR missing; got %v", seen)
	}
	// 无重复（case-insensitive）
	upKeys := make([]string, 0, len(got))
	for _, kv := range got {
		k, _, _ := strings.Cut(kv, "=")
		upKeys = append(upKeys, strings.ToUpper(k))
	}
	sort.Strings(upKeys)
	for i := 1; i < len(upKeys); i++ {
		if upKeys[i] == upKeys[i-1] {
			t.Errorf("duplicate key in merged env: %s", upKeys[i])
		}
	}
}

// TestMergedServiceEnv_FallbackWhenRegistryFails 验证 registry 读
// 失败时 fallback 到 os.Environ()，行为与未加 fix 前一致。
func TestMergedServiceEnv_FallbackWhenRegistryFails(t *testing.T) {
	errFake := errors.New("fake registry error")
	oldReader := serviceEnvReader
	serviceEnvReader = func() ([]string, error) { return nil, errFake }
	defer func() { serviceEnvReader = oldReader }()

	oldEnviron := environFunc
	environFunc = func() []string { return []string{"PATH=/usr/bin"} }
	defer func() { environFunc = oldEnviron }()

	got := mergedServiceEnv()
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Errorf("fallback did not return base env; got %v", got)
	}
}
