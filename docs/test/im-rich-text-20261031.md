# IM 富文本对齐（2026-10-31）

本轮处理 Agent 文本回复的跨平台展示差异，不修改审批状态机或 IM 账号权限。

## 已实现

- Slack：沿 Markdown AST 块边界生成 Block Kit section，不再按空行拆断代码围栏；超长块转为转义的纯文本分段；达到 section 上限时提示查看完整会话。
- Telegram：继续使用 HTML；长回复回退按转义后长度计量，不拆开字符实体，并保留截断标记。
- 钉钉：新增 GFM → 机器人 Markdown 适配；标题、强调、链接、列表、引用、代码和任务状态保留可读结构；表格转为带列名的数据行；不支持的样式降级为文字。
- 飞书：保留原生 post/md 的 CommonMark/GFM 能力；超长消息退回 text 时移除展示语法，避免直接显示 Markdown 标记。
- 共用转换：保留嵌套列表中的子列表、段落和代码块。

## 验证

执行 `go test -race ./internal/manager/biz/imbridge/... ./internal/manager/service/imbridge/...`。
覆盖语法结构、中文长文本、HTML 转义、危险链接、带空行代码、长块拆分、四个平台分页 payload，以及部分发送失败重试、重复 Flush、回复变短和钉钉一次发送语义。

本轮未向真实 IM 会话发消息，平台客户端外观仍需真实机器人验收。不把单元测试等同于平台端 E2E。

## 尚存边界和后续阶段

- 已补公共分页发送：四个平台按各自预算分页；短回复保留富文本，超长回复转成可读文字分页，保留链接目标。飞书 Stream 与 Webhook 都接入。分页内仍经过平台格式转换。
- 公共消息层记录每页 ID 和已发送内容；重试不重复成功页，回复变短后覆盖旧尾页；钉钉在最终回答阶段一次发送整组分页。状态仅在当前进程内保存，不保证进程崩溃后的跨重启恰好一次。
- 尚需改进：超长回复目前整体降级，后续可进一步仅降级单个超大块；未实测平台频率限制，不能声称支持任意长度突发发送。
- 下一阶段：统一展示审批待办，复用既有审批记录；加入平台按钮回调、操作人绑定和权限校验、过期/重复点击处理、审批结果回写与 Agent 恢复。
- 再后续：任务进度、产物附件、取消操作、线程回复和 Telegram Topic 隔离。

回滚：恢复本轮 imformat 和 provider 适配代码并重新构建 Manager；无数据库或配置迁移。

参考：
- 飞书 post/md：https://open.feishu.cn/document/server-docs/im-v1/message-content-description/create_json?lang=zh-CN
- 钉钉机器人回复：https://open-dingtalk.github.io/developerpedia/docs/learn/bot/appbot/reply/

## 审批通知阶段

已接收 `approval_pending`，向 IM 发送独立待审批通知（编号、工具名、管理员审批说明），不发送命令参数和凭证引用。同一运行内按审批 ID 去重；工具结束及最终 Flush 时读取既有 approvals 记录，更新已批准/已执行/已拒绝/失败状态。不能编辑消息的钉钉发送状态通知。查询失败不抑制最终助手回答。

状态读取位于 service 适配器，只允许查询当前会话且 ProposedBy 匹配会话服务账号的记录。没有增加审批表，也没有新增任何 approve/reject 路径。全套 imbridge/service/server `-race` 测试和 cmd 装配编译通过。

尚待身份方案选择：IM 用户绑定 Ongrid 账号后按管理员权限直接审批，或先跳转已登录的 Ongrid 审批页面。当前 IM 账号均使用统一服务账号，不能把 IM 发送者等同于该管理员。通知阶段尚未部署，未进行真实平台消息验收。
