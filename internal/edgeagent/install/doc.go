// Package install 提供 Windows edge agent 的安装/卸载抽象。
//
// 将 runInstall 的 3 个正交关注点拆为独立接口（ADR-037 A2 CR4,
// MVP-3 #18-1 深化 pass）：
//
//   - SecretStore: DPAPI 加密的凭证文件（secrets.enc）
//   - ServiceController: Windows Service 生命周期（sc.exe 包装）
//   - EnvWriter: 服务注册表 Environment 字段（REG_MULTI_SZ）
//
// 接口定义在 interfaces.go（无 build tag，跨平台可见）。
// 具体实现按平台分文件：*_windows.go 为真实实现，
// install_other.go 为非 Windows stub（返回 ErrUnsupportedPlatform）。
//
// 这使得 manager（Linux）可以引用 install 包的符号而不编译失败，
// 与 dpapi / edgedirs 包的跨平台模式一致。
package install
