// interfaces.go 定义安装/卸载流程的 3 个核心接口。
//
// 设计原则（MVP-3 #18-1）：
//   - YAGNI：仅定义 install + uninstall 流程当前所需方法，0 投机方法
//   - 编排顺序由调用方（runInstall orchestrator）决定，接口不规定顺序
//   - 每个方法是一个可独立测试的 seam

package install

import "errors"

// ErrUnsupportedPlatform 表示当前平台不支持安装操作（仅 Windows 可用）。
// 非 Windows 构建中所有方法返回此错误。
var ErrUnsupportedPlatform = errors.New("install: unsupported platform (Windows only)")

// SecretStore 管理 DPAPI 加密的凭证文件（secrets.enc）。
//
// Install 内部执行完整流程：DPAPI 加密 → 写文件 → round-trip 验证。
// 验证失败时 Install 自行删除文件（保持调用前状态）。
//
// #16 token rotation 将通过组合 Install + 文件 backup/restore 实现，
// 不需要独立的 Rotate 方法（YAGNI — 等语义明确再加）。
type SecretStore interface {
	// Install 加密 token 并写入 secrets.enc，含 round-trip 验证。
	// 失败时文件应处于调用前状态（不存在或旧内容）。
	Install(token []byte) error

	// Rotate 原子地替换 secrets.enc 中的 token（#16 D4 tmp→rename）。
	// 流程：DPAPI 加密新 token → 写 tmp 文件 → rename 覆盖 → round-trip 验证。
	// 失败时旧文件保持不变。
	Rotate(token []byte) error

	// Remove 删除 secrets.enc（用于 --uninstall）。
	Remove() error
}

// ServiceController 管理 Windows Service 生命周期（sc.exe 包装）。
//
// Create 与 Start 分离：orchestrator 需在两者之间插入 EnvWriter.Write
// （服务启动前必须有环境变量）。
//
// #21 supervisor 自升级：ConfigureRecovery 配置 SCM failure action（service.go
// 的 samesession=false 设计依赖此配置）；ConfigureDefenderExclusion 为 ongrid
// 目录添加 Defender exclusion（W3 加固，减少 AV 干扰 rename-aside）。
type ServiceController interface {
	// Create 注册 Windows 服务（sc.exe create）。
	Create(binPath string) error

	// ConfigureRecovery 配置 SCM failure recovery action（#21 Step 7）。
	// reset=86400（24h window），actions=restart/60000×3（3 次 retry，60s 延迟）。
	// service.go 的 return false, 0（samesession=false）依赖此配置触发 SCM 重启。
	ConfigureRecovery() error

	// ConfigureDefenderExclusion 为 ongrid 目录添加 Windows Defender exclusion
	//（#21 Step 7a，W3 加固）。仅在 Windows Server with Defender 时生效；
	// 第三方 AV 或 Defender 已禁用时静默失败（返回 error 但调用方仅 warn）。
	ConfigureDefenderExclusion() error

	// Start 启动已注册的服务（sc.exe start）。
	Start() error

	// Stop 停止服务（sc.exe stop），忽略"服务未运行"错误。
	Stop() error

	// Delete 删除服务（sc.exe delete）。
	Delete() error
}

// EnvWriter 管理服务注册表 Environment 字段（REG_MULTI_SZ）。
type EnvWriter interface {
	// Write 写入 KEY=VALUE 多字符串到注册表 Environment 字段。
	// 在 ServiceController.Create 之后、Start 之前调用。
	Write(pairs []string) error
}
