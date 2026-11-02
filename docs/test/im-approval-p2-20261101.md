# IM 审批 P2 验收记录（2026-11-01）

源码位置：`<LOCAL_ARTIFACT_PATH>`。目录没有 Git 元数据，本次未提交、推送、合并或部署。

## 已通过

- `make test-im-approval`：Go race 验证 IM 业务、四平台 provider、持久化 repo、审批业务与存储、IM HTTP，以及 `tests/imapproval` 完整本地链路。
- 本地完整链路使用真实 SQLite：两端绑定证明、不可猜测动作令牌、回调入队后再执行、approvals CAS、真实审批人 42 写入审计、重复回调不重复执行。测试数据位于临时目录并清理。
- 安全负例：错误组织、账号、消息、聊天、会话归属、提出者、过期令牌、已决策记录、入队后解绑、撤销管理员、禁用机器人、凭证轮换。未装配权限检查的 Usecase 默认拒绝决策。
- 存储：租约竞争只有一个胜者；running 回调重启不重放；单次绑定确认；错误用户无法读取/删除绑定；过期 challenge/token 无效；机器人级 Retry-After 不会被较短值覆盖；投递积压指标查询。
- owner：失效的 pending 执行租约不可续活或批准；其他实例的活跃 owner 不被失效。通用工具续租和审批 CAS 竞争时重新读实际状态，不撤掉已批准操作的闭包。
- 四平台 HTTP 契约模拟：原生按钮参数、消息标识、终态清除按钮；飞书 POST interactive/PATCH 更新；钉钉模板变量与主动卡片投递；Telegram 限流即时返回供协调器持久化，避免在通知请求内睡眠。
- v2/v3 审批、MCP 与通用工具相关 service 测试（`-race`）通过；cmd 装配和 IAM user 测试通过。`-tags integration` 的 cloud_bash 批准执行、拒绝不执行链路通过。
- `make build-web` 通过；新增/修改 UI 的 ESLint 通过。
- Chrome headless 7 个 E2E 通过：中英文 × light/dark 审批详情，390/768/1280/1920 宽度；登录回跳不执行；绑定必须先平台证明再 Web 显式确认；解绑。实际检查绑定 light/dark 截图。API 采用模拟，不是真实机器人验收。

截图位于 `output/im-approval/`。过程中修复 SQLite 非 UTC 时间比较导致的口令过期判断，以及 SQLite MIN 时间聚合扫描不兼容；测试保留回归覆盖。

## 外部验收阻断 / 未通过项

- 真实 Slack / Telegram / 飞书 / 钉钉机器人：尚未指定专用测试机器人和目标会话，没有向任何真实 IM 群发送测试消息。原生开关默认关闭；不能把契约模拟当成真实平台端到端通过。
- MySQL：新增 `make test-im-approval-mysql` 隔离测试入口，重复 migration 后运行同一完整审批链路。实际执行两次（含沙箱外重试）均被 Docker daemon 配置的 `<LOCAL_PROXY_ADDRESS>` 代理拒绝连接阻断，未拉到 `mysql:8.4`，因此没有 MySQL 通过结论。没有改动用户 Docker 代理配置。
- 全仓架构检查：安装指定版本 go-arch-lint 后执行，报告两项既有问题：API 生成代码 k8s → edge proto 的同组件引用未在规则放行；web/node_modules/flatted 内 Go 文件没有组件归属。本次新增文件未出现在违规结果中，但整个 `make arch-lint` 仍非通过。
- `npm ci` 报告现有依赖漏洞（34 项），未在 IM 功能中做不相关的强制升级；构建本身通过。

## 上线与回滚

见 [IM 审批 Runbook](../runbooks/im-approval-delivery.md)。钉钉必须配置自有模板及随机 token 回传变量，Slack 必须配置绑定 slash command。先完成真实平台验收后逐平台开放，保留 Web 审批链接。running/approved 不确定结果不得自动重放。

## 测试环境部署（2026-11-01）

- 目标：`https://test.example.invalid/`，用户明确授权部署。
- Manager / Web：`v0.18.2-im.20261101.1`；Edge 未更新。
- Linux Manager 通过根 `make build-ongrid` 构建，CGO + Zig x86_64-linux-gnu.2.31；纯 Go `build-linux` 受 ONNX CGO 依赖限制，未使用该失败产物。
- 部署前保留旧 Compose、镜像引用及 MySQL 一致性快照；全部上传产物经过 SHA256 校验。
- 现场目录：`<DEPLOY_DIR>/`（root-only）。
- `/healthz`、`/readyz`、首页均为 200；未认证绑定接口 401；登录管理员后绑定接口在原生功能关闭时返回预期 503。
- MySQL 实际启动迁移通过：9 张 `im_*` 表存在，`approvals.execution_owner` / `execution_lease_until` 字段存在；近期日志未发现 panic / migration failed / fatal。
- Chrome headless 实际登录 `/settings/channels` 通过，明暗主题截图已查看，页面无 JavaScript 异常。截图：`<LOCAL_ARTIFACT_PATH>,light}.png`。
- 保留现有配置：`ONGRID_PUBLIC_URL=https://test.example.invalid`；`ONGRID_IM_APPROVAL_DELIVERY`、`ONGRID_IM_NATIVE_APPROVAL_PROVIDERS` 均未设置（关闭）。未发送真实 IM 消息，未声称完成四平台回调验收。
- 回滚：`python3 <DEPLOY_DIR>/deploy.py rollback`，恢复上一版 Manager/Web；保留扩展表和数据，不自动覆盖数据库。
- 此次是实际 MySQL schema 迁移验证，不替代隔离 MySQL 全链路测试。

## 飞书卡片闭环与现场低效链路修复

- 飞书卡片移除 Web 跳转，完整展示递归脱敏的操作参数和受限结果；纯文本防止参数注入交互内容。批准/拒绝均有二次确认，详情不可完整展示时只允许拒绝，回调重复校验。
- 新增管理员本人预授权 API 与机器人私聊 `/whoami`；不把最近发言人或昵称视作管理员。
- 现场发现 session `<REDACTED_ID>` 的 host_bash 审批 ProposedBy=0，导致与 IM 服务账号 owner=1 的通知筛选不匹配。修复 v2/v3 主循环及 Worker 通过 InvokeOption 传递 session/request UserID。未放宽通知隔离筛选，未回填或执行旧审批。
- 普通“确认/批准”在原生 IM 路径不会创建新会话或新回合，也不会取消在途审批；直接引导点击卡片。系统工具指引要求明确操作直接生成真实审批，避免先用文字索要一次确认。
- `make test-im-approval` 全部 race 通过；v2/v3 无 HTTP context 的协调器/Worker 身份透传测试通过；`/whoami` 私聊与失效连接隔离、文字批准不进入 Agent 测试通过。
- 隔离 MySQL 8.0 真实测试通过（约 20.7s）：双向绑定 / 管理员预授权 → action token → inbox → approvals CAS → 真实审批人 → 单次执行；隔离容器及数据卷已删除，未使用业务数据库。
- 真实飞书客户端点击闭环仍待用户提供 `/whoami` 身份、确认绑定，并进行一次无害操作的批准/拒绝验收。不得将 API/契约测试描述为平台端 E2E。

- 最终 Manager 已部署为 `v0.18.2-im.20261101.3`，Web 保持 `.1`，Edge 不变。飞书原生开关与持久化通知开启，其他平台原生审批未开启。
- 部署目录 `<DEPLOY_DIR>/`；旧镜像和数据库快照保留。回滚命令 `python3 <DEPLOY_DIR>/deploy.py rollback`。
- 如需关闭整个新卡片版本并恢复部署前功能开关，可使用第一阶段备份：`python3 <DEPLOY_DIR>/deploy.py rollback`，恢复 `.1` 及原关闭配置；不自动覆盖数据库。

## 现场通知零日期修复（2026-11-01）

真实飞书会话生成了 ProposedBy=1 的待审批 `<REDACTED_ID>`，但通知表为空。worker 每五秒报告 MySQL 1292：`Incorrect datetime value: '0000-00-00' for column 'refresh_at'`。此前隔离 MySQL 测试覆盖绑定/决策，没有覆盖通知投影；健康探针也不能发现后台 worker 反复失败。

- `ApprovalNotification.RefreshAt` 改为 nullable 时间指针；未安排刷新存 NULL；nil 表示原生 pending 卡片需要刷新。
- 新增真实 MySQL 严格模式测试覆盖：审批投影创建、NULL 刷新时间、失败重试落库、刷新时间持久化、重新认领、终态回写。
- `make test-im-approval` race 通过；隔离 MySQL 8.0 三组集成测试通过（25.2s），启用 STRICT_TRANS_TABLES / NO_ZERO_DATE / NO_ZERO_IN_DATE。临时容器、数据卷和 SSH 隧道均已清理。
- 修复构建版本 `v0.18.2-im.20261101.4`；备份和回滚目录 `<DEPLOY_DIR>/`。

- `.4` 首次部署遇到测试盘 100% 满，Manager 创建缓存目录失败；自动回滚后旧版也无法启动。清理本次四次部署的可重建二进制/上传包及 Docker 构建缓存（缓存报告回收 1.666 GB），保留业务数据、数据库快照和回滚镜像。根分区恢复约 1.3 GiB 可用（98%）。未执行用户会话里的旧备份删除命令。
- 空间恢复后 `.4` 部署健康检查通过。现场原审批通知成功投递：`<REDACTED_ID>` 的 delivered_status=pending、native_card=true、attempts=0、last_error 为空，平台已返回有效消息 ID。只确认真实卡片发送成功，未代替用户点击批准，未执行删除。
- 先前测试结论边界修正：绑定/决策 MySQL E2E 不覆盖通知 worker；现已增加通知投影严格模式回归。部署前应检查磁盘容量，健康检查后还必须检查后台通知投递状态。

### 自助绑定闭环补齐（本地验证，尚未部署）

- 入口保持「设置 → IM → 我的 IM 审批身份」，补充三步说明、复制命令、自动轮询认领、过期禁用、成功和解绑反馈。
- `/bind` 认领回复明确要求返回页面确认；未绑定卡片错误不再指向 `/whoami`。
- `go test -race ./internal/manager/biz/imbridge ./internal/manager/biz/imbridge/provider/feishu ./tests/imapproval` 通过；新增未绑定不得入队和各类回调错误的下一步提示测试。
- 前端测试覆盖自动认领但不自动确认、确认后成功反馈、过期口令不可确认。TypeScript 检查通过。
- Chromium 使用隔离模拟 API 验证真实组件交互，保存 `<LOCAL_ARTIFACT_PATH>` 和 `<LOCAL_ARTIFACT_PATH>` 并实看；未访问真实用户绑定、未发 IM 消息、未执行真实审批。
- 本轮没有部署，不代表真实飞书用户绑定和点击审批的线上 E2E 已完成。
