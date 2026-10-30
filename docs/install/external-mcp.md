# 外部 MCP 接入、验证与回滚

适用于包含外部 MCP 特性的 Ongrid Manager。首次启用前，在测试部署完成以下步骤。客户端界面和网络路径需按实际版本验证。

## 启用

```dotenv
ONGRID_PUBLIC_URL=https://ongrid.example.com
ONGRID_EXTERNAL_MCP_ENABLED=true
ONGRID_EXTERNAL_MCP_WRITES_ENABLED=false
ONGRID_EXTERNAL_MCP_ALLOWED_ORIGINS=
```

先开启查询，再按需将 WRITES_ENABLED 设置为 true 并重启 Manager。默认两项均为 false。多个副本使用相同配置和数据库。PUBLIC_URL 必须为 HTTPS；本机 loopback 测试允许 HTTP。ALLOWED_ORIGINS 为精确、逗号分隔的浏览器 Origin（不含路径）；原生客户端可不发送 Origin。不是 CORS 开放配置，不接受通配符。

原生部署将这些变量加入服务环境；Compose 已传递三项新开关，使用项目已有启动方式重建 Manager。现有 nginx `/api/` 代理保留原路径和请求头、禁用缓存且超时大于 25 秒，无需新建 SSE 通道。自建代理须转发 Authorization、Accept、Content-Type、MCP-Protocol-Version；不要改成 HTML 登录跳转或丢弃 Header。认证失败响应与审计按连接 IP 限制，不采信任意 X-Forwarded-For；合法 PAT 不共用代理 IP 的频率额度。

## 创建 PAT

用系统管理员登录“设置 → MCP 访问”。为每个客户端创建独立 PAT，选择具体读取/修改/删除/执行能力。写操作自动勾选所需读取权限。有效期可选 30 天、90 天、自定义或不过期。

PAT 只显示一次，保存到客户端私有配置，不写入项目、Skill 或对话。可从同一页面编辑名称、有效期和权限，保存后继续使用原 Token，客户端无需重配。不再使用时删除，Token 会从列表移除并立即失效。所有权限覆盖此 Ongrid 部署；普通成员不能创建或使用。账号禁用、管理员权限丢失、到期或删除均阻断后续请求与未执行任务。

## WorkBuddy

在 WorkBuddy 打开「专家·技能·连接器 → 连接器 → 自定义连接器」。部分版本入口为「插件 → MCP 服务器 → 配置 MCP」；官方说明也提供用户级 `~/.workbuddy/mcp.json`。将下方 `ongrid` 项加入 `mcpServers`，保留已有服务，将占位符替换为独立 PAT。保存并启用后，在任务输入框「+ → 连接器」中选择 Ongrid。

```json
{
  "mcpServers": {
    "ongrid": {
      "type": "streamableHttp",
      "url": "https://ongrid.example.com/api/v1/integrations/mcp",
      "headers": { "Authorization": "Bearer <在客户端填写PAT>" },
      "timeout": 30000
    }
  }
}
```

timeout 为客户端连接选项，不代表后台 Operation 执行期限。实际设置名称以安装版本为准；不要假定配置里的环境变量会自动展开。通过客户端 Skill 导入入口选择所需文件夹，具体安装目录由该版本管理。

官方依据：[客户端连接器](https://www.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Connector)、[MCP 配置](https://www.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/MCP-Guide)、[连接器说明](https://open.workbuddy.cn/docs/connector)。本机 WorkBuddy 5.9.3 已验证连接及 51 个工具的发现，真实资源动作尚未验收；详见[验证记录](../testing/external-mcp.md#workbuddy-连接修复2026-10-29)。

客户端必须能访问所填 HTTPS 地址并信任其证书；若从云端调用，该地址也需从云端可达。控制台「设置 → MCP 访问」点击页头「接入指南」按钮，在弹窗中查看 WorkBuddy 接入指引；WorkBuddy 示例自动使用本部署的 MCP 地址，但始终保留 Token 占位符。

## 读写验证

1. initialize 协商 `2025-06-18`，发送 initialized 通知并列出工具。GET 返回 405 是预期；本实现不提供持久会话。
2. 用只读 PAT 查询明确的设备，确认不出现写工具；手工构造写调用也必须拒绝。
3. 用带 monitor:read/write/delete 的测试 PAT 创建测试面板，应无需确认直接成功；记录 Operation ID 和返回 revision。相同参数与幂等键重复提交应返回同一个 ID。
4. 更新/删除先读取当前 revision，再提交 expected_revision。用过期 revision 提交应得到 RESOURCE_VERSION_CONFLICT。
5. 提交面板改名应直接执行。提交删除等高风险动作应返回 awaiting_approval 和 approval_url。打开链接直接查看这笔操作；未登录则登录后返回原页面。点击“确认并执行”一次，页面自动显示状态，无需去审批中心查找或再次确认。拒绝与只打开页面不能执行；重复点击不能重复执行。也可用 get_operation 查询；queued 不代表副作用完成。
6. K8s 动作先 dry_run，再在 5 分钟内用匹配参数、preflight_token 和 expected_resource_version 提交。若页面确认超过到期时间，原操作应失败；重新读取、预检查并创建新的逻辑操作。
7. 远端超时或 unknown 先查询原操作和目标。不要换幂等键重放。accepted_by_remote 不证明 Pod 已就绪或服务健康。
8. 编辑测试 PAT，确认原 Token 使用新权限；删除测试 PAT，确认后续请求返回 401。清理测试面板需使用仍有权限的令牌。

终态且已审计的操作/幂等键保留至少 30 天。不要使用过期历史键表达新操作。unknown 和未决操作保留，不自动补偿。完整发布验收还须记录客户端版本、请求实际来源、内网可达性和四套 Skill 的输出。

旧版本已提交的 awaiting_approval 请求仍需人工确认，不会因升级自动执行；新链接使用 `/approvals/{approval_id}`。不要重复提交同一操作。

## 平台能力发现

扩展模块需编辑现有 PAT 或创建新 PAT，旧 Token 不会自动获得权限。使用 `list_api_operations` 搜索模块，再用 `describe_api_operation` 读取参数，最后通过 `call_api_read` / `call_api_write` 调用。普通新建和改名直接按权限执行，高风险 API 写操作通过返回的链接确认一次后执行，使用 `get_operation` 追踪；`accepted_by_api` 只表示接口成功响应，不能当作远端任务完成。生产配置必须保留有效的加密密钥，否则新增敏感写入会被拒绝。完整模块、示例和未开放接口见[能力覆盖](../design/external-mcp-coverage.md)。

## MCP 请求预算

已认证只读查询、工具发现和轮询不限每分钟次数。写工具每 PAT 120 次/分钟、突发 20 次；每 PAT/账号/单实例并发分别为 10/20/40。总并发包含认证阶段，只限制 MCP 入口，不是全局 API 配额。失败认证单独限制错误响应与审计，合法客户端不会共用 nginx 的 IP 频率额度。完整边界见[预算说明](../design/external-mcp.md#结果审计与预算)。

验收时连续调用 tools/list 和只读工具至少 40 次，确认不再因旧的 PAT/IP 频率额度返回 429；写操作使用 dry_run 验证突发保护，避免为测试制造真实副作用。这些值是联调起点，生产容量需结合查询成本、连接池与负载验证。

## 停止写入与恢复

将 WRITES_ENABLED=false 并滚动重启全部 Manager，保留 ENABLED=true，可继续读取/取消原操作。已经下发的远端动作仍可能完成；关闭入口或删除 PAT 不会撤销已发生变更。

回滚前先暂停写入、处置未决任务并保留数据库。旧版本不理解外部 Operation 状态；不要删除新增表/列来“回滚”。代码回滚与资源恢复分别处理，恢复资源仍需当前版本、权限和新的操作记录。

审计、状态及限制见[设计文档](../design/external-mcp.md)。不要将协议测试通过等同于真实客户端与生产资源验收通过。
