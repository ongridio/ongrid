# IM 审批 P1 实施与验收记录

日期：2026-11-01。范围：[HLD-003](../design/HLD-003-im-approval-interaction.md) P1a 和 P1b 的代码实现。未部署、未推送，不能视为 P1c 生产验收完成。

## 本次实现

- IM 消息生成 HTTPS 审批详情链接；读取既有 ONGRID_PUBLIC_URL，拒绝带凭证、路径、查询参数等不合法配置，禁止从外部请求拼接地址。
- `/approvals/:id` 查看、批准、拒绝和状态刷新；登录后回到原详情。链接访问不执行，批准前二次确认，重复提交锁定，超时后重新读状态。
- approved 展示为“已批准，等待执行结果”；页面隐藏 execution_token、凭证字段，解析嵌套 args_json 后再脱敏。
- `ONGRID_IM_APPROVAL_DELIVERY=true` 开启后台投递；默认关闭。原会话目标在运行前持久化，扫描补齐遗漏审批通知，使用数据库租约防止重复领取，落库消息引用和已显示状态。
- 四个平台复用既有 Sender。钉钉临时 webhook 使用现有 secretbox 加密存储、显式检查到期时间；过期后停止使用原地址。
- 通知只读 approvals；不增加批准 API，不更改执行状态机、实际审批人或 RuntimeVersion。
- 目标身份和会话固定；配置平台/AppID 改变后不将旧审批转给新机器人。过期回复密文清理；终态消息引用 30 天后清理，保留最小去重记录。

## 验证结果

| 检查 | 结果与覆盖 |
|---|---|
| IM 业务、四平台适配器、IM store、service 的 `go test -race` | 通过；包括已有富文本和分页回归 |
| approval biz/store、cmd/ongrid 的 `go test -race` | 通过 |
| `go test -race ./internal/manager/service/aiops -run 'Test.*(Approval\|HumanApproval)' -count=1` | 通过；v2/v3 MCP 审批后恢复、执行 token 单次消费、重启后 fail-closed |
| 新投递存储测试 | 通过；重启补偿、外部会话/提出者隔离、目标不可改投、多 worker 抢占、旧租约拒绝、租约过期恢复和保留清理 |
| 新投递业务测试 | 通过；四平台统一失败重试、终态通知、状态不变不重发、禁用应用和过期上下文禁止发送 |
| 前端 Vitest | 16 项通过；登录目标校验、只读打开、权限、取消、重复提交、超时查询、拒绝及脱敏 |
| Chrome headless E2E | 5 项通过；中英文 × light/dark，390/768/1280/1920px，共 16 张截图；登录回跳和实际确认弹框 |
| 前端 build | 通过；保留既有 bundle 大小警告 |
| 前端 lint | 全量无错误，有既有 warnings；本次新增文件定向检查无错误/警告 |
| 架构 lint | 未完成：go-arch-lint 未安装；人工核对新增依赖无 service→data 或跨 domain import |

浏览器 E2E 拦截本地 API 使用隔离 fixture，不连接真实 IM、LLM 或执行工具；不能据此声称真实平台回调/限流验收完成。测试无需清理线上数据。截图存于 `output/im-approval/`。

## 可复现入口

```bash
go test -race ./internal/manager/data/imbridge/... ./internal/manager/biz/imbridge/... ./internal/manager/service/imbridge/... ./internal/manager/biz/approval/... ./internal/manager/data/approval/... ./cmd/ongrid
go test -race ./internal/manager/service/aiops -run 'Test.*(Approval|HumanApproval)' -count=1
cd web
npm test -- src/pages/ApprovalDetail.test.tsx src/lib/loginReturn.test.ts
npm run build
npx playwright test --config playwright.im-approval.config.ts
```

Go 曾遇到沙箱禁止读取构建缓存，获得执行许可后重跑通过；macOS linker 提示既有 LC_DYSYMTAB warning，测试进程退出码为 0。

## 未完成与上线门槛

1. 四平台真实机器人批准/拒绝链路、限流、消息编辑失效和钉钉 webhook 到期后的现场验证。
2. MySQL migration/并发验证与生产容量验证；当前持久化测试使用 SQLite。
3. 平台错误分类与 Retry-After 支持、可靠投递专用 metrics/告警；当前使用退避、受控错误码和数据库积压排查。
4. runtime owner 丢失后的主动失效协调。当前保持已有 fail-closed，不凭本实例找不到 token 就失效其他实例的 pending。
5. P2 平台身份绑定、共用鉴权服务、原生批准/拒绝按钮。

回滚与操作步骤见 [Runbook](../runbooks/im-approval-delivery.md)。本次没有改变默认审批执行行为，可靠通知必须显式开启，真实平台验收前不默认启用。

## 工作区说明

实现位于 `<LOCAL_ARTIFACT_PATH>`，该目录原有 Git 元数据及部分未修改文件缺失。根据本地保存的 `5eb7ab1b` 提交，仅恢复 355 个缺失文件，没有覆盖目录中已有改动；未改动主工作区的业务文件。

本次改动前文件备份在 `<LOCAL_ARTIFACT_PATH>`。交付 patch 和文件清单存于 `output/im-approval-p1.patch` 与 `output/im-approval-p1-files.txt`，用于后续恢复正规分支后复核，不包含此前其他需求的已有改动。
