# MCP 能力覆盖与调用契约

2026-10-28。固定目录共 **284 个 API 操作**：148 个读取、63 个修改、32 个删除、41 个执行；覆盖 34 个 API 模块。目录按当前部署实际注册的路由及 PAT scopes 过滤。未装配的模块不会出现在目录中，后端依赖不可用时返回明确错误。

在原有 MCP 工具之外，补充 20 个固定查询工具、3 个固定写工具，以及 4 个发现/调用工具。它们不使用聊天会话的动态 ToolBag，也不会自动导出新 HTTP 路由或新安装的第三方 MCP 工具。

## 能力发现

- `list_api_operations`：按 `module`、`search`、`limit`、`offset` 查询当前 PAT 可用的操作。
- `describe_api_operation`：用 `action` 取得 path/query/body 的 JSON Schema、权限及验证语义。请求体 schema 描述字段类型，必填值和业务校验由原接口处理。
- `call_api_read`：调用固定读取操作，包括有 JSON 请求体的日志查询等 POST 查询。
- `call_api_write`：调用固定写入、删除及执行操作，必须使用对应 read 和动作权限；普通创建和改名直接执行；高风险动作返回 approval_url，用户在当前操作的确认页确认一次后执行。范围见[确认策略](external-mcp.md#确认范围)。

客户端先发现再调用。例如：

```json
{"name":"list_api_operations","arguments":{"module":"flows","limit":20}}
{"name":"describe_api_operation","arguments":{"action":"get_flows"}}
{"name":"call_api_read","arguments":{"action":"get_flows","query":{"limit":"20","offset":"0"}}}
```

`path` 和 `query` 值为字符串；`body` 是原接口的 JSON 对象。客户端不能提供 URL、HTTP 方法、Header 或替换身份。只允许固定路径模板；每个参数只能占一个路径段，实际匹配路由必须与目录模板一致。

## API 模块

| 模块 / scope 前缀 | 读取 | 修改 | 删除 | 执行 |
| --- | ---: | ---: | ---: | ---: |
| `agent-traces` | 7 | 0 | 0 | 0 |
| `agents` | 9 | 5 | 3 | 1 |
| `alert-rules` | 3 | 0 | 0 | 0 |
| `alerts` | 5 | 0 | 0 | 1 |
| `apm` | 12 | 1 | 1 | 0 |
| `approvals` | 3 | 0 | 0 | 0 |
| `audit` | 1 | 0 | 0 | 0 |
| `credentials` | 2 | 2 | 1 | 0 |
| `devices` | 4 | 0 | 0 | 0 |
| `edges` | 11 | 4 | 3 | 5 |
| `flows` | 6 | 4 | 1 | 2 |
| `iam` | 5 | 8 | 3 | 0 |
| `im` | 2 | 2 | 1 | 1 |
| `integrations` | 1 | 4 | 0 | 8 |
| `k8s` | 10 | 1 | 1 | 0 |
| `knowledge` | 6 | 8 | 3 | 2 |
| `logs` | 12 | 3 | 0 | 4 |
| `marketplace` | 2 | 2 | 1 | 0 |
| `mcp-servers` | 2 | 2 | 1 | 1 |
| `metrics` | 4 | 0 | 0 | 0 |
| `monitor` | 1 | 0 | 0 | 0 |
| `network` | 2 | 2 | 0 | 1 |
| `notifications` | 2 | 2 | 1 | 1 |
| `operator-runs` | 2 | 0 | 0 | 2 |
| `packet-captures` | 5 | 0 | 0 | 8 |
| `pages` | 1 | 0 | 1 | 0 |
| `profiles` | 1 | 0 | 0 | 0 |
| `reports` | 6 | 5 | 3 | 2 |
| `settings` | 1 | 1 | 1 | 1 |
| `skills` | 2 | 0 | 0 | 1 |
| `system` | 5 | 0 | 0 | 0 |
| `topology` | 8 | 7 | 4 | 0 |
| `traces` | 3 | 0 | 0 | 0 |
| `webshell` | 2 | 0 | 3 | 0 |

完整方法、路径和参数定义见 [api_catalog.json](../../internal/manager/server/externalmcp/api_catalog.json)。Scopes 及 UI 选项保持同一模块语义，旧 Token 不会自动获得新权限。

## 固定工具补充

- 知识与源码：`query_knowledge`、`list_repo_sources`、`read_source`、`grep_source`。
- 拓扑与网络：`get_topology`、`expand_topology`、`find_topology_node`、`query_network_devices`、`query_network_interfaces`、`get_network_neighbors`。
- 主机与数据库观测：`get_host_processes`、`rank_hosts`、`find_outlier_hosts`、`list_database_sources`、`analyze_database_status`。
- 文件元数据与 Pod 日志：`host_find_large_files`、`host_du_summary`、`host_stat_file`、`query_k8s_logs`。
- 变更审计：`query_change_events`。
- 页面与消息：`serve_page`、`send_notification`、`send_im_message`，均需要对应权限、明确用户意图和操作页确认。

文件工具只沿用既有受控元数据接口；不提供任意主机文件内容读取。系统能力及字段仍受原服务约束。

## 新增写操作的边界

1. 使用 `idempotency_key` 和 `reason`。`dry_run=true` 只检查请求形状并预览，不运行原 handler，也不证明业务校验或远端动作会成功。
2. 写请求进入持久 Operation；普通新建和改名直接执行，高风险操作同时创建 Approval，返回直达确认链接，确认后才执行。执行请求使用现有 secretbox 加密保存，生产必须具备有效的 ONGRID_SECRET_KEY 或持久 JWT secret；禁止使用弱默认密钥。加密请求明文最多 44 KiB，为 MySQL TEXT 中的 GCM/base64 留出空间。
3. 操作数据库保存加密请求；dry_run 返回脱敏的参数预览。确认页只向登录的管理员展示脱敏参数，PAT 不能获取解密内容、批准或拒绝审批。Opaque `fields`、`value`、`config` 和命名凭证字段均隐藏。业务自由文本无法保证识别任意秘密，调用方仍需控制要提交的内容。
4. 执行前重新检查 Token、账号和 scopes。编辑权限、删除 Token 或禁用账号会阻止尚未执行的操作。
5. API handler 可能调用其他服务，不能与 Operation 共用一个原子事务。成功返回 `accepted_by_api`，不是读回验证；异步任务继续通过资源专用查询接口查看。执行后出错或失联标为 `unknown`，不能推断回滚，也不能换键重放。
6. 原设备、告警、规则和面板专用写工具仍执行版本检查，目录不提供旁路。其他 API 沿用原接口的并发控制，不额外承诺 CAS；等待页面确认期间目标可能变化，执行前应重新核对。
7. 查询执行期限沿用 20 秒，操作执行期限沿用 11 分钟；响应缓冲上限 128 KiB、对外结果 120 KiB。分页/过滤应缩小范围；大结果返回明确错误，不伪装成完整数据。

## 明确不开放的路由

[api_exclusions.json](../../internal/manager/server/externalmcp/api_exclusions.json) 记录 54 项原始路由及原因：

- 登录、刷新、PAT 签发/修改、凭证明文 reveal、审批决定、内部数据面入口及公开 webhook。
- 注册/轮换 Edge 或 Kubernetes 接入凭据、创建公开分享 Token。
- 浏览器终端票据、WebSocket/SSE、二进制下载及 multipart 上传。相应 JSON 查询与任务控制仍可通过目录调用；文件传输继续使用控制台。
- 已有 revision-aware MCP 工具覆盖的写 API，以及通用内部 Operation 入口。外部操作使用按 PAT 归属校验的 `get_operation` / `cancel_operation`。

API 覆盖测试扫描仓库中的静态路由。新增路由必须进入固定目录或排除清单，不能悄悄变成外部能力。客户端与实际生产动作的验收记录见[验证记录](../testing/external-mcp.md)。
