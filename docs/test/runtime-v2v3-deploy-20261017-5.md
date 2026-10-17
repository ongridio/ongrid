# v2/v3 测试环境部署：2026-10-17 .5

- 目标：`https://101.34.63.91/`，测试环境；用户明确要求部署。
- 来源：`refactor/unified-agent-runtime`，基于 `45ec9ad4` 加本地未提交的 v2/v3 切换改动。没有推送或合并。
- Manager：`ongrid:v0.18.2-v3-test.20261017.5`，镜像 ID `sha256:c56803c1a3db39c77e6031db33556b27084b12248de803795feb867f16fec0b9`。
- 前端：`ongrid-web:v0.18.2-v3-test.20261017.5`，镜像 ID `sha256:ac7d3cc2fb7c03759d18c58fc171014d3899543e104ba49046a8bb6079ca78c3`。
- Edge 和其他基础服务未更新；本次没有新增 schema。
- Manager 使用 Linux builder 的 `make build-ongrid`；测试机通过 `make docker-ongrid` 和 `make docker-build-web` 将产物叠加到原镜像。
- 二进制 SHA256：`b208040ce5e7d6f5bdc1d42240faf548710874a21d3d2ba401f389c5e14761b8`；本地与远端一致。
- 部署目录：`/opt/ongrid/test-deploy-20261017-runtime-v2v3-5/`，含原 Compose、镜像记录、构建日志、部署/回滚及验证脚本。

## 验证

- 部署前：95 项隔离 E2E、runtime race、设置页单测和 Chrome 交互验证已通过，详见 RFC-009。
- 部署后：`/`、`/healthz`、`/readyz` 均 200；Manager/前端 running，restart count 均 0，近 10 分钟未发现 panic。
- 原运行时设置不存在，即跟随 release 默认 v3。验证时分别短暂选择 v2/v3 创建会话，立即恢复原设置，再执行真实模型调用。
- 两版均实际执行 `query_devices` 成功，返回两台在线设备；耗时分别约 1.49 秒和 2.12 秒。
- 两个临时会话已删除，原设置已恢复；当前默认仍为 v3。
- 线上 index 和包含「v1 · 不再支持」的 JS 资源逐字节匹配新构建。页面支持选择 v2、v3，v1 禁用。
- 初次前端资源校验遇到 macOS 打包附属文件；已清理附属文件并重建前端镜像，等待 Nginx 就绪后重新验证通过。
- 本轮现场冒烟没有重跑动态 MCP/Skill 生命周期；这些由前述隔离 E2E 覆盖。

## 回滚

恢复之前 Manager `.4` 和前端 `.2`：

```bash
python3 /opt/ongrid/test-deploy-20261017-runtime-v2v3-5/ongrid-deploy5.py rollback
```

旧镜像没有 v2 执行入口。后续用户若已创建需保留的 v2 会话或待审批动作，不能直接将此镜像回滚当作运行时切换；应优先将新会话默认恢复 `release`，保留当前 v2 引擎处理既有任务，再决定迁移或停用。此次验证创建的 v2 会话已清理，不回滚数据库数据。

## 后续测试环境版本

2026-10-17 后续 Manager 已更新为 `.7`，前端继续使用 `.5`。`.6` 修复 MCP 注册发现唤醒与手动验证竞争，`.7` 修复两版默认助理热加载保留。最终现场安装/卸载 Skill 前后 `/agents/default` 响应一致；MCP 自动发现约 0.11 秒，双版本调用通过。部署、回滚及生命周期验收详见 RFC-009「MCP 就绪与热加载收尾」；当前回滚入口在 `/opt/ongrid/test-deploy-20261017-runtime-hardening-7/ongrid-deploy7.py`，恢复 `.6`，不要误用本文件早期的 `.5` 回滚路径。


2026-10-17 再次更新 Manager 至 `.8`：修复重启后孤立 Worker 未关闭。镜像 ID `sha256:97cc8a61b2ce2e1f9b032456c32210bf39222b72fc338d33e2465cd1b26258d5`，二进制 SHA256 `e3ce9dda30635336d4a6cef559bf94af2edd0af68b2b3d5ff4bb71d8174ff77a`。双版本取消、重启收尾、历史空版本和 v2 Skill 兼容共 6 个现场场景通过，临时数据清理完成。当前回滚入口为 `/opt/ongrid/test-deploy-20261017-worker-recovery-8/ongrid-deploy8.py rollback`，恢复 `.7` Manager；保留已完成的状态收尾，不恢复被中断的执行。前端继续 `.5`，无 schema 变更、未推送或合并。
