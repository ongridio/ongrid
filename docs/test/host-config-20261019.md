# Host 长期插件配置验收（2026-10-19）

本轮为本地代码和浏览器 mock 验收，不代表测试环境已部署。

- Linux Docker：相关 biz、device/edge store 使用 `go test -race` 通过。
- Host 插件 HTTP 路由测试通过：Host 650 使用 Host 接口；普通用户写入返回 403。
- Manager `go build ./cmd/ongrid` 通过。
- TypeScript 检查通过；设备、设备详情、APM 配置相关 40 项测试通过。
- `make test-web-ci-platform`：10 个文件、105 项全部通过。
- 回归覆盖：安装 41 → 42，长期目标和禁用意图保留；旧实例拒绝写入；Profile 会话不重放；删除配置不被重新接纳；无安装时允许编辑；已删除 Host 不进入插件统计和 APM 候选项。
- Chrome headless：使用无当前 Edge 的 Host 650 打开 `/apm/capture/devices/650`，采集目标、编辑入口和离线说明正常。浅色/深色截图已实看：`artifacts/host-config-20261019/`。

限制：没有执行本轮真实重装部署验收；数据库本地凭据恢复不是本轮实现能力。完整 server/edge 测试曾在 root Docker 环境遇到已有文件权限测试失败，本轮仅报告 Host 路由定向测试，不宣称整个 server 包通过。
