// 此文件无 build tag — 在 Windows + Linux 上都编译。
//  enum 单一真相源
// 问题（重构前）：每个 enum 有 3 处独立定义，无编译期保障一致：
//   - metadata.go 的 Enum slice（LLM schema）
//   - 各 skill 的 validXxxStates map（运行时校验）
//   - 各 skill 的 stateToPS map（PS 映射）
// 解决方案：每个 enum 声明一份 EnumMapping slice，派生 3 份索引。
// Go var 初始化时一次性派生（编译时绑定），消除手动同步三份索引的 drift 风险。
// 此文件无 build tag 因为 metadata.go（跨平台）需要引用派生的 enumList
// 填充 skill.ParamSchema 的 Enum 字段。Windows 端的 validSet / toPSMap
// 通过相同的 var 引用（跨平台可见）。

package windows

// EnumMapping 声明一个 enum 值的双重表示：用户输入规范名 + PowerShell 枚举名。
// 单一真相源模式：
//   - Canonical：用户/LLM 输入的小写规范名（如 "closed"）
//   - PSValue：PowerShell 枚举名（如 "Closed"）；"" 表示不映射
// 派生方式：声明 []EnumMapping 后用 deriveValidSet / deriveToPSMap / deriveCanonicalList
// 派生 3 份索引。Go var 初始化时派生，启动时 panic 而非运行时静默 drift。
type EnumMapping struct {
	Canonical string
	PSValue   string
}

// deriveValidSet 从 EnumMapping slice 派生 canonical→bool 的 validSet map。
// 用于 skill 层业务校验（拒绝非法 enum 值）。
func deriveValidSet(mappings []EnumMapping) map[string]bool {
	out := make(map[string]bool, len(mappings))
	for _, m := range mappings {
		out[m.Canonical] = true
	}
	return out
}

// deriveToPSMap 从 EnumMapping slice 派生 canonical→PSValue 的映射 map。
// 用于 buildFilter 时把用户输入转为 PowerShell 枚举名。
// 无效输入返回 ""（Go zero value），psQuote("") 安全无结果。
func deriveToPSMap(mappings []EnumMapping) map[string]string {
	out := make(map[string]string, len(mappings))
	for _, m := range mappings {
		out[m.Canonical] = m.PSValue
	}
	return out
}

// deriveCanonicalList 从 EnumMapping slice 派生 canonical 名列表。
// 用于 metadata.ParamSchema 的 Enum 字段（LLM schema）。
func deriveCanonicalList(mappings []EnumMapping) []string {
	out := make([]string, len(mappings))
	for i, m := range mappings {
		out[i] = m.Canonical
	}
	return out
}

// ---- Services enum 声明 ----

var (
	servicesStatusEnum = []EnumMapping{
		{"running", "Running"},
		{"stopped", "Stopped"},
	}
	servicesStatusValid    = deriveValidSet(servicesStatusEnum)
	servicesStatusToPS     = deriveToPSMap(servicesStatusEnum)
	servicesStatusEnumList = append(deriveCanonicalList(servicesStatusEnum), "all")

	servicesStartTypeEnum = []EnumMapping{
		{"auto", "Automatic"},
		{"manual", "Manual"},
		{"disabled", "Disabled"},
	}
	servicesStartTypeValid    = deriveValidSet(servicesStartTypeEnum)
	servicesStartTypeToPS     = deriveToPSMap(servicesStartTypeEnum)
	servicesStartTypeEnumList = append(deriveCanonicalList(servicesStartTypeEnum), "all")
)

// ---- Network enum 声明 ----

var (
	networkStateEnum = []EnumMapping{
		{"closed", "Closed"},
		{"listen", "Listen"},
		{"syn_sent", "SynSent"},
		{"syn_received", "SynReceived"},
		{"established", "Established"},
		{"fin_wait1", "FinWait1"},
		{"fin_wait2", "FinWait2"},
		{"close_wait", "CloseWait"},
		{"closing", "Closing"},
		{"last_ack", "LastAck"},
		{"time_wait", "TimeWait"},
		{"delete_tcb", "DeleteTCB"},
	}
	networkStateValid    = deriveValidSet(networkStateEnum)
	networkStateToPS     = deriveToPSMap(networkStateEnum)
	networkStateEnumList = append(deriveCanonicalList(networkStateEnum), "all")
)
