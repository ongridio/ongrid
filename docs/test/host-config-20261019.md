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

## 与 main 的 #530 / #531 整合

在 #528 分支合入 main `f895e1fe`，不单独 revert #530。

- 保留 #531 的单一 BaseTool 执行结构；没有恢复 Registry.Invoke / execute* 旧分支。
- 排名与异常检测只按 Host 聚合并从设备库存取名，不扫描 500 个 Edge、不按历史安装拆分主机。
- 保留 #530 的 Host 42 → Edge 7、无关 Host 99 → Edge 42 错号测试，并调整为 Host 输出。仅有 edge_id 的指标不猜测归属。
- 告警快照直接查询 Host；无安装、关联查询故障不影响设备信息与观测数据。Host 本身不可查询时明确省略。
- 新增无 Edge 服务时仍注册 Host 排名、摘要、拓扑工具的回归；query_devices 不再把 Edge 主键伪装成 device_id。
- AIOps 全包（包括 v1/v2/v3 runtime）race 测试通过；工具补充测试通过；Manager 编译通过。

本轮没有部署，也没有合并 GitHub PR。
