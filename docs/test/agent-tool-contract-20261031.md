# Agent 工具契约部署验收（2026-10-31）

## 部署

- 环境：`https://test.example.invalid/`。
- Manager：`ongrid:v0.18.2-tools.20261031.1`。
- 本机 Edge：`v0.18.2-tools.20261031.1`，原地替换二进制并重启，保留身份与配置。journal 修复需要此更新。
- Web 保持 `ongrid-web:v0.18.2-snmp.20261031.5`。
- 来源：`feat/network-snmp-config` 当前工作区，包含此前未提交改动；不是独立发布或已合并版本。
- 备份目录：`<DEPLOY_DIR>/`。保留旧镜像引用、Compose、数据库快照和 Edge 二进制。
- Manager 回滚：`python3 <DEPLOY_DIR>/deploy.py rollback`。
- Edge 回滚：`sh <DEPLOY_DIR>/rollback-edge.sh`。
- 回滚不自动覆盖数据库。

## 确定性回归

Linux `go test -p 2 -race -count=1`：tools 全部子包、v2/v3 graph 及 callbacks、v2/v3 chatruntime、skill/builtin，10 个包全部通过。

| 涉及点 | 验证方式与结果 |
|---|---|
| 静态工具 Schema | 全量静态 Schema 编译测试通过 |
| 参数错误与审批顺序 | 必填、额外参数、类型/范围、嵌套对象、互斥模式测试；非法参数在审批创建前拒绝，通过 |
| incidents / bash / topology / Trace / K8s action | 参数契约及各工具现有业务测试通过；不在真实集群执行破坏性动作 |
| 动态 MCP Schema | 本地引用、definitions、嵌套关键字、更新后缓存刷新、外部引用拒绝测试通过 |
| 模型 Schema 精度 | const/enum/default/examples 大整数与嵌套业务值精确往返，通过 |
| 布尔 Schema | true/false 外围空白输入通过 |
| 延迟工具 | ToolSearch 后完整执行 Schema 可用，通过 |
| 租户注入 | 裁剪展示 Schema 后仍从完整执行 Schema 注入 tenant_id，通过；这是确定性包装链测试 |
| 数字表示 | 内置整数的 1.0/1e0、uint64 边界及非法输入通过 |
| 外部 host skill | 42/42.0/4.2e1 均路由到绑定 Edge；其他业务数字表示保持原样，通过；使用测试 Repo/Runner |
| 工具提示 | 不再推荐 get_process_list，当前工具名 get_host_processes 测试通过 |
| journal | duration 转换与错误边界测试通过，另有下面的真实 Edge 验证 |

日志：测试机 `/tmp/ongrid-schema-contract-test/tool-contract-regression.log`，本地 `/tmp/ongrid-contract-regression.log`。

## 隔离 HTTP E2E

真实测试 Manager + 独立 MySQL + 可控模型/MCP 服务；不是生产模型回答质量测试。

`go test -p 2 -race -tags=e2e ./tests/e2e -run 'Test(Dynamic|RuntimeVersions|Skills_Registry|MCP_|AssistantChat_)' -timeout 20m -v`

13 个顶层测试组全部通过，含子测试共 73 条 PASS 记录：

- 用户可见聊天、运维场景、主机库存、配置提案、剩余场景、抓包审批。
- 动态 MCP 注册、禁用/恢复、端点替换、删除撤销、通知更新、真实一分钟轮询兜底。
- 动态 skill 安装、读取、卸载并撤销已有回合工具目录。
- MCP CRUD、连通测试及凭证注入。
- v2/v3 切换、动态 MCP、skill/persona 重载、skill 目录。

隔离 MySQL 已自动停止并删除，最终确认容器不存在。日志：测试机 `/tmp/ongrid-schema-contract-test/tool-contract-e2e.log`，本地 `/tmp/ongrid-contract-e2e.log`。

## 新部署真实模型 SSE 验收

五组临时会话全部产生 done/summary，没有 SSE error 事件，均已删除：

| 场景 | 实际工具与结果 |
|---|---|
| CPU/内存/负载最紧张的 3 台主机 | rank_hosts 3 次、get_host_load、get_host_processes；如实返回仅有 2 台主机，正常结束 |
| 过去 7 天告警规则 Top 5 | query_incidents 1 次；明确现有列表与生命周期 event_count 不能支持准确时间窗聚合，没有循环重试 |
| 延迟加载告警详情 | ToolSearch → query_incidents → get_incident_detail，成功 |
| 空字符串 service 的 Trace 查询 | 调用一次，被 Schema 拒绝；模型说明所需条件后结束。状态标记问题见下文 |
| 最近 10m journal | query_devices → ToolSearch → host_read_journal，成功返回 5 行 |

通过真实 Skill HTTP API 对 Host 1 再验证 journal：`10m` → `600 seconds ago`、`1h30m` → `5400 seconds ago`，分别成功返回 5 行、2 行，未出现 journalctl 日期解析错误。

原始证据仅保存于测试机部署目录 `live-check.json`、`direct-check.json`，不将主机日志或认证数据提交仓库。

## 尚未通过的点

1. **失败工具被标记为 success**：空 service 的 query_traceql 返回 `status: failed` 错误内容，但 SSE tool_end 和 summary.tool_calls 仍是 success。执行拦截有效、对话可恢复，状态传播尚不准确。Graph 适配器将错误包装为工具结果并返回 nil error，后续状态记录需要识别这一失败语义。不能将此场景视为状态验收通过。
2. **告警时间窗聚合能力仍缺失**：本次验证了如实说明限制和不循环，不代表已实现准确的过去 7 天规则 Top 5/触发分布。
3. 本次五组新会话没有复现 unknown/stream error，不代表所有历史会话或模型场景已排除这些问题。

## 最终环境检查

- `/healthz`、`/readyz` 均为 200；Edge service active。
- 检查期间 Manager 日志未发现 `panic:` / `fatal error:`。
- 清理旧 Go 缓存、超过 24 小时未使用的 Docker 构建缓存/悬空镜像、旧 E2E 的 macOS/ARM 编译产物；保留业务数据与带版本标签的回滚镜像。
- 最终磁盘剩余约 1.6 GB，需后续继续管理历史部署产物容量。
- 本轮未推送、未合并代码。

## 后续修复：Word 错误记录与迭代收尾（尚未部署）

本节为上述部署之后的代码回归，不代表测试环境已更新。

- v2/v3 在本轮 `MaxIterations` 的最后一个模型回合强制移除工具并生成证据总结；系统提醒不重置预算，旧用户回合不计入本轮。保持原有轮次上限。模型继续输出工具时不再执行，返回明确的未完成提示；取消和模型服务错误继续向上传播。
- 顶层失败结果统一进入执行表、SSE、审计和指标的 error/timeout 分支；失败结果不写入成功读缓存。错误摘要按持久化字段长度限制为 512 字符。
- 历史工具消息按会话、调用 ID、时间和工具名恢复执行表记录，返回新增 `tool_call` 元数据；跨会话或后续重复 ID 不串记录，找不到执行证据时保持未知。
- 主机 CPU 增加有效性和采样窗口。交互查询用独立基线，避免改变定时采集；首样本、缺失样本、计数器重置不标记为有效 0%。
- 进程 CPU 改为短窗口增量，按整机逻辑 CPU 容量归一化，并校验进程创建时间以防 PID 复用。旧 Edge 缺少元数据时明确标记 unknown/legacy_unspecified；主机排名仍是 5 分钟均值，工具说明明确窗口差异。

验证：

- macOS 与 Linux 相关 Go 包 `go test -race` 通过；Linux 覆盖完整 tools、v2/v3 graph/callbacks/chatruntime、collector、session store 和 HTTP handler。
- 真正构建的 Eino Graph 同步与流式路径：多工具调用后在预留轮次完成总结，不触发 MaxSteps。
- HTTP 模型适配测试：最终请求不携带工具，连调用选项中传入的工具也清除；模型仍返回工具时不执行。
- Linux 实际 `/proc` 采样包含当前测试进程，CPU 窗口有效；交互式主机采样不更改定时采集基线。
- 历史执行关联测试覆盖跨会话、跨回合复用调用 ID，以及仅返回工具结果页。
- 前端工具状态 16 项测试通过，TypeScript 类型检查通过。

本批未执行新部署或完整浏览器 E2E。告警时间窗聚合能力仍是独立缺口，收尾保护不会创造缺失的业务数据。回滚无需数据库迁移：新增字段均为可选兼容字段，可回退 Manager/Web/Edge 至上一镜像/二进制；旧 Edge 返回值会被 Manager 标记为未知口径。

## 运行时收尾最终部署验收

- 环境：`https://test.example.invalid/`。
- Manager / Web：`v0.18.2-runtime.20261031.4`；Web 内容与本次 `.2` 构建相同。
- 本机 Edge：`v0.18.2-runtime.20261031.2`，保留身份与配置；已核对运行文件与发布产物 SHA-256 一致。
- 备份与验收证据：`<DEPLOY_DIR>/`，包含数据库快照、原 Compose、旧 Edge 二进制和脱离仓库保存的会话结果。
- Manager/Web 回滚：`python3 <DEPLOY_DIR>/deploy.py rollback`。
- Edge 回滚：`sh <DEPLOY_DIR>/rollback-edge.sh`。

真实验收发现并补修两项模型兼容问题：

1. Eino 的 `ToolChoiceForbidden` 未传到模型 HTTP 请求，补充内部 `DisableTools`，最终请求显式发送 `tool_choice=none`。
2. 模型在没有工具的最终请求仍沿历史工具协议输出 DSML 文本。追加临时的阶段结束提醒，要求直接总结已有结果；协议标记不能作为有效终答展示。该提醒不写入用户历史，也不增加执行次数。

最终实测：

- 临时 Agent 设置 `max_turns=3`，连续两次运行均只执行 `query_devices`、`query_incidents` 两个查询，第 3 次给出有具体证据的总结，明确告警规则尚未查询；无 SSE error、无 DSML、无通用兜底文本。
- 同一会话继续提问，工具重新执行，验证下一轮预算重置。
- CPU 真实调用：主机 `cpu_sample_status=valid`、约 0.25 秒窗口；进程 `cpu_basis=host_total`、约 0.27 秒窗口。均生成正常总结。
- 空 service 的 Trace 调用被 schema 拦截，SSE、summary 和重新加载的历史记录都为 error。
- Playwright 实际加载历史失败会话，验证“失败”且无“状态未知”；明暗主题截图人工检查通过，未发现页面 JS 异常。
- 独立 MySQL 的会话与 v2/v3 动态 MCP/Skill E2E 首轮 15 个通过项，测试容器自动删除。补修后的模型适配、Graph 和 E2E 重跑结果另见本次最终日志。
- 7 个临时会话及 1 个临时 Agent 已删除，清理无错误。

尚未改变：输出侧与执行侧缓存计数的差异、流式与非流式调用裁剪差异，以及告警时间窗聚合能力。本次部署不代表这些后续事项已完成。未推送或合并代码。

最终收尾：补修后的 Linux 模型适配与 Graph `-race` 回归通过；3 组会话/runtime E2E（含子项共 15 项）重跑通过，独立 MySQL 容器已销毁。`/healthz`、`/readyz` 返回 200，部署后日志无 panic/fatal，Edge active。清理构建缓存后可用磁盘约 1.9 GB。最终本地日志：`/tmp/ongrid-runtime-final-e2e.log`，浏览器截图：`/tmp/ongrid-runtime-history-dark.png`、`/tmp/ongrid-runtime-history-light.png`。
