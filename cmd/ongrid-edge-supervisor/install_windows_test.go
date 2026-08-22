//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ongridio/ongrid/internal/edgeagent/install"
)

// TestZeroBytes_ClearsContent 验证 zeroBytes 清零内容。
func TestZeroBytes_ClearsContent(t *testing.T) {
	cases := [][]byte{
		[]byte("secret"),
		[]byte("ed_bk_token_123"),
		{0xFF, 0xAA, 0x55, 0x00},
		{},
	}
	for i, b := range cases {
		original := make([]byte, len(b))
		copy(original, b)
		zeroBytes(b)
		for j, v := range b {
			if v != 0 {
				t.Errorf("case %d byte %d = %d (0x%02X), want 0 (original was 0x%02X)",
					i, j, v, v, original[j])
			}
		}
	}
}

// TestParseInstallOptions_ExtractsAllFields 验证所有参数解析（B3）。
func TestParseInstallOptions_ExtractsAllFields(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want installOptions
	}{
		{
			"all_flags_space",
			[]string{"--token", "tok", "--cloud-addr", "1.2.3.4:40012", "--access-key", "ak123", "--collector-mode", "all", "--plugin-bin-dir", "C:\\bin", "--plugin-work-dir", "C:\\data"},
			installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012", AccessKey: "ak123", CollectorMode: "all", PluginBinDir: "C:\\bin", PluginWorkDir: "C:\\data"},
		},
		{
			"all_flags_equals",
			[]string{"--token=tok", "--cloud-addr=1.2.3.4:40012", "--access-key=ak123"},
			installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012", AccessKey: "ak123"},
		},
		{
			"mixed_inline_and_space",
			[]string{"--token=tok", "--cloud-addr", "1.2.3.4:40012", "--access-key=ak123"},
			installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012", AccessKey: "ak123"},
		},
		{
			"empty_args",
			[]string{},
			installOptions{},
		},
		{
			"missing_value_at_end",
			[]string{"--token"},
			installOptions{}, // Token 仍为空（缺值）
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseInstallOptions(tc.args)
			if got != tc.want {
				t.Errorf("parseInstallOptions(%v)\n  got  = %+v\n  want = %+v", tc.args, got, tc.want)
			}
		})
	}
}

// TestInstallOptions_Validate 验证 Validate 校验必需字段并填默认值。
func TestInstallOptions_Validate(t *testing.T) {
	t.Run("missing_token", func(t *testing.T) {
		o := installOptions{CloudAddr: "1.2.3.4:40012", AccessKey: "ak"}
		if err := o.Validate(); err == nil {
			t.Error("expected error for missing token")
		}
	})
	t.Run("missing_cloud_addr", func(t *testing.T) {
		o := installOptions{Token: "tok", AccessKey: "ak"}
		if err := o.Validate(); err == nil {
			t.Error("expected error for missing cloud_addr")
		}
	})
	t.Run("missing_access_key", func(t *testing.T) {
		o := installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012"}
		if err := o.Validate(); err == nil {
			t.Error("expected error for missing access_key")
		}
	})
	t.Run("applies_defaults", func(t *testing.T) {
		o := installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012", AccessKey: "ak"}
		if err := o.Validate(); err != nil {
			t.Fatalf("Validate failed: %v", err)
		}
		if o.CollectorMode != "off" {
			t.Errorf("default CollectorMode = %q, want off", o.CollectorMode)
		}
		if o.PluginBinDir == "" {
			t.Error("default PluginBinDir should be set")
		}
		if o.PluginWorkDir == "" {
			t.Error("default PluginWorkDir should be set")
		}
	})
	t.Run("tls_ca_file_not_exist_rejected", func(t *testing.T) {
		o := installOptions{
			Token:     "tok",
			CloudAddr: "1.2.3.4:40012",
			AccessKey: "ak",
			TLSCAFile: `Z:\nonexistent\path\ca.pem`,
		}
		err := o.Validate()
		if err == nil {
			t.Fatal("expected error for nonexistent TLSCAFile")
		}
	})
	t.Run("tls_ca_file_exist_accepted", func(t *testing.T) {
		tmp := t.TempDir()
		caFile := filepath.Join(tmp, "ca.pem")
		if err := os.WriteFile(caFile, []byte("dummy"), 0644); err != nil {
			t.Fatal(err)
		}
		o := installOptions{
			Token:     "tok",
			CloudAddr: "1.2.3.4:40012",
			AccessKey: "ak",
			TLSCAFile: caFile,
		}
		if err := o.Validate(); err != nil {
			t.Errorf("Validate should pass when TLSCAFile exists: %v", err)
		}
	})
	t.Run("tls_ca_file_empty_not_checked", func(t *testing.T) {
		o := installOptions{
			Token:     "tok",
			CloudAddr: "1.2.3.4:40012",
			AccessKey: "ak",
			// TLSCAFile 留空 — 不校验（plaintext 模式）
		}
		if err := o.Validate(); err != nil {
			t.Errorf("Validate should pass when TLSCAFile is empty: %v", err)
		}
	})
}

// TestInstallOptions_EnvPairs 验证 envPairs 输出格式正确。
func TestInstallOptions_EnvPairs(t *testing.T) {
	o := installOptions{
		Token:         "tok",
		CloudAddr:     "1.2.3.4:40012",
		AccessKey:     "ak123",
		CollectorMode: "off",
		PluginBinDir:  `C:\bin`,
		PluginWorkDir: `C:\data`,
	}
	pairs := o.envPairs()
	if len(pairs) != 7 {
		t.Fatalf("envPairs len = %d, want 7 (含 SCRAPE_CONFIG_FILE, 修复)", len(pairs))
	}
	// 必须含 ACCESS_KEY 明文（不加密）
	foundAccessKey := false
	for _, p := range pairs {
		if p == "ONGRID_EDGE_ACCESS_KEY=ak123" {
			foundAccessKey = true
		}
	}
	if !foundAccessKey {
		t.Error("envPairs missing ONGRID_EDGE_ACCESS_KEY")
	}
	// 必须含 SECRETS_FILE 路径（让 worker 知道从哪加载 DPAPI token）
	foundSecretsFile := false
	for _, p := range pairs {
		if strings.HasPrefix(p, "ONGRID_EDGE_SECRETS_FILE=") {
			foundSecretsFile = true
		}
	}
	if !foundSecretsFile {
		t.Error("envPairs missing ONGRID_EDGE_SECRETS_FILE")
	}
	// 不应含 SECRET_KEY 明文（仅 secrets.enc 提供）
	for _, p := range pairs {
		if strings.HasPrefix(p, "ONGRID_EDGE_SECRET_KEY=") {
			t.Errorf("SECRET_KEY should NOT be in Environment (DPAPI only): %s", p)
		}
	}
	// 默认无 TLS env（用户未传 --tls-ca-file）
	for _, p := range pairs {
		if strings.HasPrefix(p, "ONGRID_EDGE_TLS_") {
			t.Errorf("TLS env should NOT be set without --tls-ca-file: %s", p)
		}
	}
}

// TestInstallOptions_TLSEnvPairs 验证 --tls-ca-file / --tls-server-name
// 透传到 service Environment。启用 TLS 时必需，缺失会导致
// worker tunnel dial 失败（"force closed"）。
func TestInstallOptions_TLSEnvPairs(t *testing.T) {
	o := installOptions{
		Token:         "tok",
		CloudAddr:     "broker.example.com:16667",
		AccessKey:     "ak123",
		TLSCAFile:     `C:\ongrid-edge\ca.pem`,
		TLSServerName: "broker.example.com",
	}
	pairs := o.envPairs()

	wantCA := false
	wantSN := false
	for _, p := range pairs {
		if p == "ONGRID_EDGE_TLS_CA_FILE=C:\\ongrid-edge\\ca.pem" {
			wantCA = true
		}
		if p == "ONGRID_EDGE_TLS_SERVER_NAME=broker.example.com" {
			wantSN = true
		}
	}
	if !wantCA {
		t.Errorf("envPairs missing ONGRID_EDGE_TLS_CA_FILE; got %v", pairs)
	}
	if !wantSN {
		t.Errorf("envPairs missing ONGRID_EDGE_TLS_SERVER_NAME; got %v", pairs)
	}
}

// TestEnvPairs_IncludesScrapeConfig 验证 envPairs 含 ONGRID_EDGE_SCRAPE_CONFIG_FILE
// （NSSM→supervisor 切换功能回退修复：worker 找不到 scrape.yaml → embedded baseline
// 只 scrape windows_exporter，自定义 metrics 全断）。
func TestEnvPairs_IncludesScrapeConfig(t *testing.T) {
	o := installOptions{
		Token:            "tok",
		CloudAddr:        "1.2.3.4:40012",
		AccessKey:        "ak123",
		CollectorMode:    "off",
		PluginBinDir:     `C:\bin`,
		PluginWorkDir:    `C:\data`,
		ScrapeConfigFile: `C:\ProgramData\ongrid-edge\scrape.yaml`,
	}
	pairs := o.envPairs()
	found := false
	for _, p := range pairs {
		if p == "ONGRID_EDGE_SCRAPE_CONFIG_FILE=C:\\ProgramData\\ongrid-edge\\scrape.yaml" {
			found = true
		}
	}
	if !found {
		t.Errorf("envPairs missing ONGRID_EDGE_SCRAPE_CONFIG_FILE; got %v", pairs)
	}
}

// TestValidate_DefaultsScrapeConfigFile 验证 Validate 在 ScrapeConfigFile 空时
// 填 edgedirs.DataDir/scrape.yaml 默认值（与 PLUGIN_WORK_DIR 默认值模式一致）。
func TestValidate_DefaultsScrapeConfigFile(t *testing.T) {
	o := installOptions{Token: "tok", CloudAddr: "1.2.3.4:40012", AccessKey: "ak"}
	if err := o.Validate(); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
	if o.ScrapeConfigFile == "" {
		t.Error("Validate should default ScrapeConfigFile (was empty)")
	}
}

// TestParseInstallOptions_ScrapeConfigFlag 验证 --scrape-config-file 解析（同时支持
// space 和 = 两种形式，对齐其它 --flag 模式）。
func TestParseInstallOptions_ScrapeConfigFlag(t *testing.T) {
	t.Run("space form", func(t *testing.T) {
		opts := parseInstallOptions([]string{"--scrape-config-file", `C:\custom\scrape.yaml`})
		if opts.ScrapeConfigFile != `C:\custom\scrape.yaml` {
			t.Errorf("ScrapeConfigFile = %q; want C:\\custom\\scrape.yaml", opts.ScrapeConfigFile)
		}
	})
	t.Run("equals form", func(t *testing.T) {
		opts := parseInstallOptions([]string{"--scrape-config-file=C:/x/scrape.yaml"})
		if opts.ScrapeConfigFile != "C:/x/scrape.yaml" {
			t.Errorf("ScrapeConfigFile = %q; want C:/x/scrape.yaml", opts.ScrapeConfigFile)
		}
	})
}

// TestParseInstallOptions_TLSFlags 验证 --tls-ca-file / --tls-server-name
// 同时支持 --flag <value> 和 --flag=<value> 两种形式。
func TestParseInstallOptions_TLSFlags(t *testing.T) {
	t.Run("space form", func(t *testing.T) {
		opts := parseInstallOptions([]string{
			"--token", "tok",
			"--cloud-addr", "1.2.3.4:40012",
			"--access-key", "ak",
			"--tls-ca-file", `C:\ca.pem`,
			"--tls-server-name", "frontier.example.com",
		})
		if opts.TLSCAFile != `C:\ca.pem` {
			t.Errorf("TLSCAFile = %q; want C:\\ca.pem", opts.TLSCAFile)
		}
		if opts.TLSServerName != "frontier.example.com" {
			t.Errorf("TLSServerName = %q; want frontier.example.com", opts.TLSServerName)
		}
	})
	t.Run("equals form", func(t *testing.T) {
		opts := parseInstallOptions([]string{
			"--token=tok",
			"--cloud-addr=1.2.3.4:40012",
			"--access-key=ak",
			"--tls-ca-file=C:/ca.pem",
			"--tls-server-name=frontier.example.com",
		})
		if opts.TLSCAFile != "C:/ca.pem" {
			t.Errorf("TLSCAFile = %q; want C:/ca.pem", opts.TLSCAFile)
		}
		if opts.TLSServerName != "frontier.example.com" {
			t.Errorf("TLSServerName = %q; want frontier.example.com", opts.TLSServerName)
		}
	})
}

// ---------------------------------------------------------------------------
// Mock 实现（用于 runInstall orchestrator 测试）
// ---------------------------------------------------------------------------

// errDPAPI / errSC / errReg / errStart 是测试用的 sentinel 错误。
var (
	errDPAPI = fmt.Errorf("mock: dpapi failure")
	errSC    = fmt.Errorf("mock: sc.exe failure")
	errReg   = fmt.Errorf("mock: registry failure")
	errStart = fmt.Errorf("mock: sc.exe start failure")
)

// 编译时断言：mock 类型实现 install 包接口。
var (
	_ install.SecretStore        = (*mockSecretStore)(nil)
	_ install.ServiceController  = (*mockServiceController)(nil)
	_ install.EnvWriter          = (*mockEnvWriter)(nil)
)

// mockSecretStore 实现 install.SecretStore。
type mockSecretStore struct {
	installErr error
	removeErr  error
	installed  []byte
	removeCall int
}

func (m *mockSecretStore) Install(token []byte) error {
	if m.installErr != nil {
		return m.installErr
	}
	m.installed = append([]byte(nil), token...)
	return nil
}

func (m *mockSecretStore) Rotate(token []byte) error {
	if m.installErr != nil {
		return m.installErr
	}
	m.installed = append([]byte(nil), token...)
	return nil
}

func (m *mockSecretStore) Remove() error {
	m.removeCall++
	if m.removeErr != nil {
		return m.removeErr
	}
	return nil
}

// mockServiceController 实现 install.ServiceController。
type mockServiceController struct {
	createErr             error
	recoveryErr           error
	defenderExclusionErr  error
	startErr              error
	stopErr               error
	deleteErr             error
	created               string
	recoveryConfigured    bool
	defenderConfigured    bool
	started               bool
	stopped               bool
	deleted               bool
}

func (m *mockServiceController) Create(binPath string) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.created = binPath
	return nil
}

func (m *mockServiceController) ConfigureRecovery() error {
	if m.recoveryErr != nil {
		return m.recoveryErr
	}
	m.recoveryConfigured = true
	return nil
}

func (m *mockServiceController) ConfigureDefenderExclusion() error {
	if m.defenderExclusionErr != nil {
		return m.defenderExclusionErr
	}
	m.defenderConfigured = true
	return nil
}

func (m *mockServiceController) Start() error {
	if m.startErr != nil {
		return m.startErr
	}
	m.started = true
	return nil
}

func (m *mockServiceController) Stop() error {
	m.stopped = true
	if m.stopErr != nil {
		return m.stopErr
	}
	return nil
}

func (m *mockServiceController) Delete() error {
	m.deleted = true
	if m.deleteErr != nil {
		return m.deleteErr
	}
	return nil
}

// mockEnvWriter 实现 install.EnvWriter。
type mockEnvWriter struct {
	writeErr error
	written  []string
}

func (m *mockEnvWriter) Write(pairs []string) error {
	if m.writeErr != nil {
		return m.writeErr
	}
	m.written = pairs
	return nil
}

// ---------------------------------------------------------------------------
// runInstall orchestrator 测试（4 错误路径 + happy path）
// ---------------------------------------------------------------------------

// validInstallOptions 返回通过 Validate 的 installOptions。
func validInstallOptions() installOptions {
	return installOptions{
		Token:         "test_token_abc123",
		CloudAddr:     "1.2.3.4:40012",
		AccessKey:     "ak_test",
		CollectorMode: "off",
		PluginBinDir:  `C:\bin`,
		PluginWorkDir: `C:\data`,
	}
}

// TestRunInstall_DPAPIFailure 验证 DPAPI 加密失败时 install 中止，不创建服务。
func TestRunInstall_DPAPIFailure(t *testing.T) {
	ss := &mockSecretStore{installErr: errDPAPI}
	sc := &mockServiceController{}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err == nil {
		t.Fatal("expected error for DPAPI failure, got nil")
	}

	// 服务不应被创建
	if sc.created != "" {
		t.Errorf("service should not be created, got binPath=%s", sc.created)
	}
	// Environment 不应被写
	if ew.written != nil {
		t.Error("Environment should not be written")
	}
	// Remove 不应被调用（Install 内部自清理，orchestrator 不介入）
	if ss.removeCall != 0 {
		t.Errorf("Remove should not be called, got %d calls", ss.removeCall)
	}
}

// TestRunInstall_SCCreateFailure 验证 sc.exe create 失败时回滚 secrets.enc。
func TestRunInstall_SCCreateFailure(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{createErr: errSC}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err == nil {
		t.Fatal("expected error for sc.exe create failure, got nil")
	}

	// Remove 应被调用（回滚 secrets）
	if ss.removeCall != 1 {
		t.Errorf("Remove should be called once for rollback, got %d calls", ss.removeCall)
	}
	// Environment 不应被写
	if ew.written != nil {
		t.Error("Environment should not be written after sc.exe create failure")
	}
	// Start 不应被调用
	if sc.started {
		t.Error("Start should not be called")
	}
}

// TestRunInstall_RegistryFailure 验证 registry 失败时报错但不回滚服务。
func TestRunInstall_RegistryFailure(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{}
	ew := &mockEnvWriter{writeErr: errReg}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err == nil {
		t.Fatal("expected error for registry failure, got nil")
	}

	// Remove 不应被调用（服务已创建，不回滚）
	if ss.removeCall != 0 {
		t.Errorf("Remove should NOT be called for registry failure, got %d calls", ss.removeCall)
	}
	// 服务应已创建
	if sc.created == "" {
		t.Error("service should be created before registry write")
	}
	// Start 不应被调用
	if sc.started {
		t.Error("Start should not be called after registry failure")
	}
}

// TestRunInstall_SCStartFailure 验证 sc.exe start 失败时不报错（仅告警）。
func TestRunInstall_SCStartFailure(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{startErr: errStart}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err != nil {
		t.Fatalf("start failure should NOT return error, got: %v", err)
	}

	// 服务应已创建 + Environment 已写
	if sc.created == "" {
		t.Error("service should be created")
	}
	if ew.written == nil {
		t.Error("Environment should be written")
	}
	// Remove 不应被调用
	if ss.removeCall != 0 {
		t.Errorf("Remove should NOT be called for start failure, got %d calls", ss.removeCall)
	}
}

// TestRunInstall_Success 验证 happy path：全部步骤成功执行。
func TestRunInstall_Success(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 所有步骤应执行
	if len(ss.installed) == 0 {
		t.Error("token should be installed")
	}
	if sc.created == "" {
		t.Error("service should be created")
	}
	if !sc.recoveryConfigured {
		t.Error("SCM failure recovery should be configured")
	}
	if !sc.defenderConfigured {
		t.Error("Defender exclusion should be configured")
	}
	if !sc.started {
		t.Error("service should be started")
	}
	if ew.written == nil {
		t.Error("Environment should be written")
	}
	// Remove 不应被调用
	if ss.removeCall != 0 {
		t.Errorf("Remove should NOT be called on success, got %d calls", ss.removeCall)
	}
}

// TestRunInstall_DefenderExclusionFailure_NonFatal 验证 Defender exclusion 失败不阻断 install。
func TestRunInstall_DefenderExclusionFailure_NonFatal(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{
		defenderExclusionErr: fmt.Errorf("mock: Add-MpPreference failed"),
	}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err != nil {
		t.Fatalf("Defender exclusion 失败不应阻断 install： %v", err)
	}
	// 后续步骤应继续
	if !sc.recoveryConfigured {
		t.Error("ConfigureRecovery 应在 Defender 失败后继续执行")
	}
	if !sc.started {
		t.Error("Start 应在 Defender 失败后继续执行")
	}
}

// TestRunInstall_RecoveryFailure_NonFatal 验证 ConfigureRecovery 失败不阻断 install。
func TestRunInstall_RecoveryFailure_NonFatal(t *testing.T) {
	ss := &mockSecretStore{}
	sc := &mockServiceController{
		recoveryErr: fmt.Errorf("mock: sc.exe failure failed"),
	}
	ew := &mockEnvWriter{}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	err := runInstall(log, validInstallOptions(), ss, sc, ew)
	if err != nil {
		t.Fatalf("ConfigureRecovery 失败不应阻断 install： %v", err)
	}
	if !sc.defenderConfigured {
		t.Error("Defender exclusion 应已配置（在 Recovery 之前）")
	}
	if !sc.started {
		t.Error("Start 应在 Recovery 失败后继续执行")
	}
}
