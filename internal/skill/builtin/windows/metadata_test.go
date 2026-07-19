// metadata_test.go 无 build tag — 跨平台编译，验证 Windows skill metadata 单一真相源。
// 此测试是 #18-3 的核心保障：
//   - metadata 定义在 metadata.go（无 build tag），Windows + Linux 都编译
//   - Windows 端 skill.Metadata() 委托此函数
//   - Linux 端 windows_catalog_other.go 调用此函数注册 stub
//   - 新增 skill 只需在此加一个函数，两侧自动一致

package windows

import (
	"testing"

	"github.com/ongridio/ongrid/internal/skill"
)

func TestEventLogMetadata(t *testing.T) {
	m := EventLogMetadata()

	if m.Key != "host_windows_event_log" {
		t.Errorf("Key = %q, want %q", m.Key, "host_windows_event_log")
	}
	if m.Name == "" {
		t.Error("Name should not be empty")
	}
	if m.Description == "" {
		t.Error("Description should not be empty")
	}
	if m.ResultPreview == "" {
		t.Error("ResultPreview should not be empty")
	}
	if m.Class != skill.ClassSafe {
		t.Errorf("Class = %v, want ClassSafe", m.Class)
	}
	if m.Scope != skill.ScopeHost {
		t.Errorf("Scope = %v, want ScopeHost", m.Scope)
	}
	if m.Category != "log" {
		t.Errorf("Category = %q, want %q", m.Category, "log")
	}
	// event_log 必须有 log_name / max_events / level / since / include_message 5 个参数
	expectedParams := []string{"log_name", "max_events", "level", "since", "include_message"}
	if len(m.Params) != len(expectedParams) {
		t.Fatalf("Params count = %d, want %d", len(m.Params), len(expectedParams))
	}
	for i, name := range expectedParams {
		if m.Params[i].Name != name {
			t.Errorf("Params[%d].Name = %q, want %q", i, m.Params[i].Name, name)
		}
	}
	// log_name 必须是 required enum
	logName := m.Params[0]
	if !logName.Required {
		t.Error("log_name should be required")
	}
	if logName.Type != "enum" || len(logName.Enum) != 5 {
		t.Errorf("log_name Type/Enum = %q/%v, want enum with 5 values", logName.Type, logName.Enum)
	}
	// max_events 默认值 100
	if m.Params[1].Default != 100 {
		t.Errorf("max_events Default = %v, want 100", m.Params[1].Default)
	}
}

func TestServicesMetadata(t *testing.T) {
	m := ServicesMetadata()

	if m.Key != "host_windows_services" {
		t.Errorf("Key = %q, want %q", m.Key, "host_windows_services")
	}
	if m.Description == "" {
		t.Error("Description should not be empty")
	}
	if m.ResultPreview == "" {
		t.Error("ResultPreview should not be empty")
	}
	if m.Class != skill.ClassSafe {
		t.Errorf("Class = %v, want ClassSafe", m.Class)
	}
	if m.Scope != skill.ScopeHost {
		t.Errorf("Scope = %v, want ScopeHost", m.Scope)
	}
	if m.Category != "service" {
		t.Errorf("Category = %q, want %q", m.Category, "service")
	}
	// services 必须有 name / status / start_type 3 个参数
	expectedParams := []string{"name", "status", "start_type"}
	if len(m.Params) != len(expectedParams) {
		t.Fatalf("Params count = %d, want %d", len(m.Params), len(expectedParams))
	}
	for i, name := range expectedParams {
		if m.Params[i].Name != name {
			t.Errorf("Params[%d].Name = %q, want %q", i, m.Params[i].Name, name)
		}
	}
	// status 默认值 "all"，3 个 enum 值
	status := m.Params[1]
	if status.Default != "all" {
		t.Errorf("status Default = %v, want all", status.Default)
	}
	if len(status.Enum) != 3 {
		t.Errorf("status Enum count = %d, want 3", len(status.Enum))
	}
}

func TestProcessesMetadata(t *testing.T) {
	m := ProcessesMetadata()

	if m.Key != "host_windows_processes" {
		t.Errorf("Key = %q, want %q", m.Key, "host_windows_processes")
	}
	if m.Description == "" {
		t.Error("Description should not be empty")
	}
	if m.ResultPreview == "" {
		t.Error("ResultPreview should not be empty")
	}
	if m.Class != skill.ClassSafe {
		t.Errorf("Class = %v, want ClassSafe", m.Class)
	}
	if m.Scope != skill.ScopeHost {
		t.Errorf("Scope = %v, want ScopeHost", m.Scope)
	}
	if m.Category != "process" {
		t.Errorf("Category = %q, want %q", m.Category, "process")
	}
	// processes 必须有 name / top_n / min_memory_mb 3 个参数
	expectedParams := []string{"name", "top_n", "min_memory_mb"}
	if len(m.Params) != len(expectedParams) {
		t.Fatalf("Params count = %d, want %d", len(m.Params), len(expectedParams))
	}
	for i, name := range expectedParams {
		if m.Params[i].Name != name {
			t.Errorf("Params[%d].Name = %q, want %q", i, m.Params[i].Name, name)
		}
	}
	// top_n 默认值 10
	if m.Params[1].Default != 10 {
		t.Errorf("top_n Default = %v, want 10", m.Params[1].Default)
	}
}

func TestNetworkMetadata(t *testing.T) {
	m := NetworkMetadata()

	if m.Key != "host_windows_network" {
		t.Errorf("Key = %q, want %q", m.Key, "host_windows_network")
	}
	if m.Description == "" {
		t.Error("Description should not be empty")
	}
	if m.ResultPreview == "" {
		t.Error("ResultPreview should not be empty")
	}
	if m.Class != skill.ClassSafe {
		t.Errorf("Class = %v, want ClassSafe", m.Class)
	}
	if m.Scope != skill.ScopeHost {
		t.Errorf("Scope = %v, want ScopeHost", m.Scope)
	}
	if m.Category != "network" {
		t.Errorf("Category = %q, want %q", m.Category, "network")
	}
	// network 必须有 state / local_port / remote_address / remote_port / top_n 5 个参数
	expectedParams := []string{"state", "local_port", "remote_address", "remote_port", "top_n"}
	if len(m.Params) != len(expectedParams) {
		t.Fatalf("Params count = %d, want %d", len(m.Params), len(expectedParams))
	}
	for i, name := range expectedParams {
		if m.Params[i].Name != name {
			t.Errorf("Params[%d].Name = %q, want %q", i, m.Params[i].Name, name)
		}
	}
	// state 默认值 "all"
	state := m.Params[0]
	if state.Default != "all" {
		t.Errorf("state Default = %v, want all", state.Default)
	}
	// top_n 默认值 50
	if m.Params[4].Default != 50 {
		t.Errorf("top_n Default = %v, want 50", m.Params[4].Default)
	}
}

// TestAllMetadataKeysUnique 确保所有 Windows skill 的 Key 不重复。
// 必须包含全部已声明 metadata 函数（新增 skill 时同步加一行）。
func TestAllMetadataKeysUnique(t *testing.T) {
	keys := map[string]bool{
		EventLogMetadata().Key:  true,
		ServicesMetadata().Key:  true,
		ProcessesMetadata().Key: true,
		NetworkMetadata().Key:   true,
		HotfixMetadata().Key:    true,
	}
	if len(keys) != 5 {
		t.Errorf("unique keys = %d, want 5 (duplicate detected)", len(keys))
	}
}

// TestAllMetadataValid 确保所有 metadata 通过框架校验（Validate）。
// 必须包含全部已声明 metadata 函数（新增 skill 时同步加一行）。
func TestAllMetadataValid(t *testing.T) {
	all := []skill.Metadata{
		EventLogMetadata(),
		ServicesMetadata(),
		ProcessesMetadata(),
		NetworkMetadata(),
		HotfixMetadata(),
	}
	for _, m := range all {
		if err := m.Validate(); err != nil {
			t.Errorf("metadata %q failed validation: %v", m.Key, err)
		}
	}
}
