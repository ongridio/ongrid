# v3 测试环境更新：2026-10-17 .4

- 目标：`https://101.34.63.91/`，仅测试环境。
- 源码：需求分支 `refactor/unified-agent-runtime`，生产代码提交 `45ec9ad4`；本轮本地 E2E 与覆盖文档尚未提交、未推送。
- 变更：同步 main #524/#525，移除协调控制工具残留的默认 15 秒限制；保留显式 AgentTool 预算及 v1/v2 源码快照。
- Manager 镜像：`ongrid:v0.18.2-v3-test.20261017.4`；镜像 ID `sha256:908f4dd740c41d28de1c0ea9cca0bc2412f7ebf0b10f6bbdbb73fffb776d9b84`。
- 前端及 Edge 保持 `.2`，它们没有本轮生产代码变化。
- 构建使用 Linux builder 的 `make build-ongrid`，远端使用 `make docker-ongrid`；仅重建 Manager 服务。
- Compose 原配置、镜像构建日志、部署脚本与结果：`/opt/ongrid/test-deploy-20261017-v3-45ec9ad4/`。健康检查失败自动恢复 `.3`。
- 手动回滚：在测试机执行 `python3 /opt/ongrid/test-deploy-20261017-v3-45ec9ad4/ongrid-v3-deploy4.py rollback`。本轮没有新增 schema，不回滚数据库数据。

## 验证与边界

- 部署前 Linux runtime race（含 Flow）通过；新增动态注册/MCP/Skill E2E 与全量 E2E race 通过（93 项）。
- 部署后 `/healthz`、`/readyz` 通过。
- 真实模型创建新聊天记录为 v3；`query_devices` 返回两台在线设备，`ToolSearch` 和 `web_search` 实际调用成功；临时聊天均在 finally 中删除。
- 搜索返回的目标质量仍受 SearXNG 上游限制：本次没有直接命中 Kubernetes DNS 排查官方子页面；模型明确说明缺失，第二次查询报告 partial 与 sogou CAPTCHA。不能把工具执行成功等同于搜索目标命中。
- 现有 MCP `approval_demo` 为 enabled/ok；当前没有安装 Skill 包。因此本次部署冒烟不声称现场再次覆盖 Skill 安装/卸载或真实 MCP 工具执行，动态生命周期由隔离 E2E 覆盖。
- 预检曾误用应用不支持的 `version` 子命令，附加进程初始化后退出；版本最终按镜像及部署记录确认，未将该命令当作版本查询接口。
- 本轮没有部署生产环境、推送代码或合并 PR。
