# Agent / Edge 版本与未知插件状态修复

## 现场复现

测试环境会话 `bfdbc427-2eec-4440-bd30-b079a7d3ed77`：`get_host_summary` 输出 `current_edge.agent_version`，模型沿用为“Agent 版本”；摘要固定输出 `plugin_status=unsupported`，被错误解释成插件未启用和数据库指标不可用。

## 修复

- 面向模型的摘要输出改为 `current_edge.edge_version`。Edge 注册、数据库和管理 API 的既有 `agent_version` 字段保留，避免破坏旧 Edge 协议。
- 摘要未查询插件状态，输出 `not_queried` 和原因；不能当作不支持、未启用或采集不可用。
- v2/v3 在主助理及 worker 的提示中注入实际执行引擎版本 `agent_runtime_version`，不从 Edge、Manager release 或模型名称猜测。
- 明确解释历史摘要的 `current_edge.agent_version` 与 `plugin_status=unsupported`，避免已有会话继续沿用错误语义。
- 提醒模型区分告警标题的历史 value 与当前指标，不能凭缺少数据或版本差异推断能力缺失。

## 自动回归

Linux builder 中以下包 `go test -race` 通过：

- `internal/manager/biz/aiops/tools`
- `internal/manager/biz/aiops/runtime/v2/chatruntime`
- `internal/manager/biz/aiops/runtime/v3/chatruntime`
- `internal/manager/biz/aiops/runtime/contract`（无单独测试，由两版组合提示测试覆盖）

新增案例覆盖不同 Host / Edge ID、在线/离线摘要、版本归属、未查询插件状态，以及两个运行时的主助理和 worker 提示。结构测试验证确定性契约，不能替代真实模型答案的语义验收。

## 测试环境部署与模型验收

- Manager 已更新为 `v0.18.2-host.20261018.2`；前端与 Edge 保持上一轮版本。
- Manager 镜像 `sha256:05adc8b8794c1bff8ac22e652e80ca2f5aa1075765dd6c6e8dca5667c1ea13b6`。
- 二进制 SHA256 `b67ddad78a3aa519732132472c5c02851dace14a9ad83a87e7cbe573a198c791`。
- 首页、healthz、readyz 均 200；没有数据库 schema 改动。
- 两版各跑“健康摘要 → 询问 runtime / Edge 版本及历史 unsupported 含义”。检查实际工具结果，确认只有 `edge_version` 且插件状态为 `not_queried`；人工核对两版回答，均正确区分 runtime 和 Edge，明确历史 unsupported 不能证明插件未启用。
- [完整模型回答](artifacts/version-semantics-20261018/semantic-acceptance.json)。两个临时会话删除，原默认运行时设置恢复。未修改用户原会话；历史回答不会被追溯重写。
- 语义验收仅对本问题通过，不代表所有运维推断正确：模型仍把历史告警 value 当作当前值，且遇到 `get_host_load` 的 `no such rpc` 时会过度推断版本能力缺失。这些问题单独记录，不能宣称已随本次版本命名修复解决。
- 回滚：`python3 /opt/ongrid/test-deploy-20261018-version-semantics-2/deploy.py rollback`。恢复上一版 Manager，保留数据库；部署目录有数据库备份与原 Compose。
- 未推送、未合并。
- 对 `get_host_load` 连续补查 3 轮，两台设备全部返回负载、无错误，证明前述错误不能归因为版本永久缺少 RPC；间歇性调用失败的根因尚未定位，不能据此声称已修复。现场证据位于部署目录 `rpc-recheck.json`。
