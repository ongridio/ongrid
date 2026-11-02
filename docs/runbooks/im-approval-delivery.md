# IM 审批通知投递

适用：HLD-003 的 P1，2026-11-01 初始实现。审批事实始终来自 approvals，投递故障不能通过重复批准来修复。

## 配置与灰度

- `ONGRID_PUBLIC_URL=https://<控制台域名>`：复用已有配置，只接受 HTTPS origin，不包含用户信息、路径、查询参数或 fragment。未配置或不合法时不生成链接，日志给出原因。
- `ONGRID_IM_APPROVAL_DELIVERY=true`：启用持久化通知 worker。默认关闭，关闭时沿用当前运行内的通知与状态更新。启用后不同时运行两套发送逻辑。
- 钉钉需要有效的 `ONGRID_SECRET_KEY` 或非默认 `ONGRID_JWT_SECRET`，沿用 secretbox 加密；不要在日志、工单或截图中输出密钥或临时 webhook。
- 上线先在隔离测试 Manager 配置开关，验证四平台真实机器人后再推广；不要在同一数据库中混跑开启和关闭开关的 Manager 并向两者分发 IM 请求。

启动时 IM store.Migrate 增量创建 im_approval_targets、im_approval_notifications。只新增投递数据，不改审批状态枚举和执行器。上线前备份数据库；本轮 SQLite 迁移与并发测试已覆盖，MySQL 集成验收仍须在目标环境执行。

## 排查顺序

1. 查 approvals 的真实状态：pending、approved、executed、rejected 或 failed。approved 不表示执行完成；结果未知时禁止重新执行。
2. 查 im_approval_targets 是否有对应 session_id；它保存原应用、聊天和线程的身份快照。注册失败会在运行工具前终止本次 IM 请求，避免产生无人可见的审批。
3. 查 im_approval_notifications 的 delivered_status、next_at、attempts、lease_until、last_error。只使用只读数据库查询，不复制 secret_context。
4. 结合 `IM approval notification delayed` 日志的 approval_id 和 reason 查原因。平台原始错误可能带凭证，因此只记录受控错误分类。

| last_error | 处理 |
|---|---|
| app_disabled / app_unavailable | 检查机器人是否仍启用、是否已删除；不要自动改投其他机器人 |
| app_identity_changed | 同一配置记录的平台或 AppID 已改变；旧会话不迁移到新身份，使用新会话 |
| reply_context_expired | 钉钉临时回复地址过期；在原会话产生新消息可更新该会话的回复上下文。`/new` 会建立新会话，不会迁移旧审批 |
| reply_context_unavailable | 检查密钥是否变更，不打印密文或明文 |
| platform_delivery_failed | 检查平台权限、网络、限流和消息是否被删除；无法确定是否送达时不要立即补发业务动作 |
| approval_unavailable | 检查原会话归属和服务账号；不能绕过校验读其他会话审批 |

首次失败和第 20 次失败记录通知日志；退避带抖动，20 次失败后每小时重试。禁用应用、过期地址按分钟复查，以便原配置修复后恢复。没有新增平台指标告警规则，试点期间需监控上述日志与数据库积压；正式推广前补独立指标和告警。

已发送后、message_id 落库前崩溃可能产生重复通知，采用至少一次投递语义。重复通知不会再次批准或执行。编辑请求报错目前保守重试，不在未知结果下自动追加消息；不支持编辑的发送器会追加终态通知。

## 保留与回滚

过期的钉钉 secret_context 会清空；终态消息引用在 30 天后清空。保留最小终态投递记录，防止修复扫描重放历史。审批审计不清理。

关闭 `ONGRID_IM_APPROVAL_DELIVERY` 并重启可回到旧通知路径；新页面和链接可保留。数据库新表不删除，已决定的审批不回滚；旧审批后台状态回写会暂停。再次启用后恢复扫描。停机时未完成的租约最长两分钟后可重新领取。

## 明确限制

- 恢复通知不等于恢复 Agent：Manager 重启会丢失通用工具内存执行回调。保持已有 fail-closed 行为，不能自动重建或重新批准。
- 未实现多实例 runtime owner 存活判断，因此不自动批量失效所有 pending，也不宣称已解决重启后的完整审批生命周期。
- 尚无 IM 原生批准/拒绝按钮和账号绑定；点击链接必须在 Web 登录，并通过既有管理员鉴权。
- 真实四平台限流、消息编辑失效和钉钉过期后的主动推送，需要平台联调后才能扩大默认启用范围。

## 原生审批与身份绑定（P2）

默认关闭原生决策。仅对已完成验收的平台设置：

```text
ONGRID_IM_APPROVAL_DELIVERY=true
ONGRID_IM_NATIVE_APPROVAL_PROVIDERS=slack,telegram,feishu,dingtalk
ONGRID_PUBLIC_URL=https://your-ongrid.example
```

按平台逗号分隔，小范围启用；需要强加密密钥（已有 ONGRID_SECRET_KEY 或安装生成的 JWT secret）。原生按钮只在 stream 模式启用，webhook 模式继续走 Web 链接。机器人更换凭证会让绑定和已生成动作令牌失效，需重新绑定；旧会话冻结的通知目标不随身份变化迁移，使用 `/new` 创建新会话。

入口：设置 → IM → 我的 IM 审批身份。登录管理员选择机器人发起绑定，在该机器人的私聊中发送一次性口令，回到页面检查并核对不可变平台账号后确认。口令十分钟有效，只存摘要；组织/平台账号加密存储。解绑后，尚未开始的后台决策会重新检查绑定而拒绝执行。不能撤销已经发生的外部副作用。

平台侧配置：

- Slack：Socket Mode 开启 Interactivity；配置 `/ongrid-bind` slash command，绑定步骤发送 `/ongrid-bind <口令>`。配置的 allow_from 需包括审批人 Slack user ID；身份范围取平台回调中的真实 team ID，不取本地 app_id 标签。卡片在原 thread_ts 下发送。
- Telegram：getUpdates 接收 message 和 callback_query；审批人的数字 user ID 必须在 allow_from。动作 callback_data 仅为 43 字节随机令牌，不包含审批参数。没有额外公网 webhook。
- 飞书：长连接订阅 `card.action.trigger`，应用需具备发消息、更新消息卡片权限；使用 open_id + tenant_key 绑定。交互卡片状态更新使用 PATCH，回复线程通过 reply 接口发送。
- 钉钉：应用需开通卡片创建投放及更新权限。在 IM 机器人编辑页填写本应用的“审批卡片模板 ID”。模板字符串变量：`text`（完整脱敏详情和执行结果，必须用纯文本组件完整展示）、`approve_token`、`reject_token`、`review_url`，可选个人变量 `receipt`（回调接收提示）。批准/拒绝按钮分别在对应 token 非空时显示，回传参数名统一 `ongrid_token`，参数值引用对应 token 变量；`review_url` 为空时隐藏详情链接；原生审批在卡片内完成，不依赖网页。配置 STREAM 回调。不要使用固定 approve/reject 常量代替随机 token。仅支持 userid 模式。没有模板或有效投递目标时使用 Web 审批链接。

钉钉卡片主动发送使用应用凭证，不依赖 sessionWebhook 的过期时间；普通聊天仍保留原临时回复机制。群卡片禁止转发，服务端仍校验真实 spaceId、outTrackId 与动作令牌。所有平台在审批完成后移除原生决策按钮。

## 限流、结果不确定与 owner 失效

- 平台 429 延后通知，并把 Retry-After 写入机器人级投递门限；该机器人其他通知也暂停。普通临时失败退避；明确消息不可编辑时，下次补发最新状态；权限/机器人失效降为低频检查。
- 平台回调仅验证身份并写入 `im_approval_commands`。4 个后台 worker 有界执行，审批继续由 approvals 条件更新决定胜者。回调 ACK 不表示执行成功。
- commands 的 running 记录不会因重启而重新执行。若进程在执行后、落库前崩溃，核查 approvals 和外部系统；不能直接把 running 改回 queued。审批仍为 pending 时可在 Web 核对并决定；approved 时必须先查外部结果，不能重提相同动作。
- 通用工具内存执行闭包使用持久化 owner + 60 秒租约，存活等待器每 10 秒续约。协调器只使过期的 pending 失效；活跃的其他实例、历史无 owner 记录、approved 记录不被误判。该机制不提供重启续跑或跨实例执行器转发。
- 动作令牌 30 分钟有效，待审批卡片每 25 分钟换新令牌；最终判断仍以实际审批状态和执行租约为准。过期 challenge/token 每分钟清理，审批事实和决策收件箱保留审计。

指标：`ongrid_im_approval_deliveries_total{provider,outcome}`、`ongrid_im_approval_backlog{provider}`、`ongrid_im_approval_oldest_seconds{provider}`。只有固定平台/结果标签，没有用户、群、审批 ID。告警示例见同目录 `im-approval-alerts.yml`，部署前将 Runbook 地址换成实际文档地址并纳入 Prometheus rule_files。

回滚：先清空 `ONGRID_IM_NATIVE_APPROVAL_PROVIDERS` 并重启，停止原生决策入队；需要时再关闭 `ONGRID_IM_APPROVAL_DELIVERY`。保留增量表和审批审计，不删除或重排 running 命令。新版本继续使用 Web 审批；回滚旧二进制前排空在途请求并核查 approved 操作，避免把不确定副作用当成未执行。

官方接口依据：[Slack block_actions](https://docs.slack.dev/reference/interaction-payloads/block_actions-payload)、[Telegram Bot API](https://core.telegram.org/bots/api)、[飞书消息接口](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1)、[钉钉官方卡片回调示例](https://open-dingtalk.github.io/developerpedia/docs/explore/tutorials/stream/bot/go/card-callback/)。

## 飞书卡片内闭环（2026-11-01）

测试阶段只启用 `ONGRID_IM_APPROVAL_DELIVERY=true` 和 `ONGRID_IM_NATIVE_APPROVAL_PROVIDERS=feishu`。

- 原生卡片不包含 URL 按钮，也不要求先到 Web 查看。参数使用纯文本组件递归脱敏，批准/拒绝都有卡片内二次确认。
- payload 无法解析、嵌套过深或脱敏后超过 10 KB 时不显示批准，只保留拒绝；服务端回调再次检查，不能通过旧令牌绕过。
- 成功/拒绝/失败回写原卡片；执行输出过长或不是结构化数据时明确提示在当前对话询问结果。不把 approved 显示为执行成功。
- 私聊机器人 `/whoami` 获取应用与租户、用户不可变标识。管理员核对身份后，可用本人认证身份调用 `POST /api/v1/im/bindings/preauthorize`，JSON 为 `app_id`（平台内配置数字 ID）、`scope`、`platform_user_id`。接口仅绑定调用者本人，不接受 user_id，不覆盖已有绑定。
- 身份信息不应按昵称、最近发言人推断。`/whoami` 自身不会创建会话、调用模型或赋予权限；群聊或失效连接不会返回身份。
- 原有 Web 双向绑定兼容保留；管理员预授权支持不依赖终端 Web 跳转的审批使用方式。首次管理员仍需要可信的管理渠道授权。
- 飞书开放平台需使用长连接接收 `card.action.trigger`；仅能收到普通消息不等于卡片回调订阅已生效。发布应用后用真实点击验证。
- 旧的链接通知不会自动变成新原生卡片；Manager 重启后已丢失执行闭包的审批不能恢复批准，请重新发起操作。

飞书组件依据：https://open.feishu.cn/document/common-capabilities/message-card/add-card-interaction/interactive-components/button?lang=zh-CN


### 跨平台卡片详情与绑定提示（2026-11-01）

- 原生审批统一从现有审批记录读取并脱敏实际参数和结果；不再仅飞书展示详情。不能完整展示时禁止批准，保留拒绝；回调入口重复校验，不依赖按钮隐藏。
- Slack 使用 plain_text 分块展示参数，不解释命令中的 Markdown / 用户提及；绑定命令为 `/ongrid-bind`。
- Telegram 审批正文使用纯文本；详情超过 2500 字节不允许批准，给状态和结果预留空间；较长结果提示在当前会话继续询问。拒绝按钮独立于批准按钮显示。普通助手长回复仍走原有分页。
- 钉钉模板的 `text` 必须使用可完整显示的纯文本组件，不能折叠或截断操作范围；`receipt` 使用个人反馈区域。批准和拒绝按钮分别根据各自 token 是否为空控制显示。需要在真实应用中核验模板，代码不能替代平台模板配置。
- 四平台回执区分未绑定、无权限、过期、已处理、参数不完整和暂时故障，支持中英文；不再提示用户用 `/whoami` 完成绑定。
- 本轮不改变平台开关，不声称完成真实 Slack / Telegram / 钉钉机器人验收。
