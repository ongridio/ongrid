# 外部 MCP 本地验证记录

日期：2026-10-28，2026-10-29 补充权限选择验收。分支：`feat/external-mcp-pat`。基线：`684cde4cd1d3c3e71d41f806029c2bf9ba7f5471`。

本记录只覆盖本地实现。未部署生产，未完成豆包工作端到端验收；已在本机 WorkBuddy 5.9.3 验证连接及工具发现。

## 直达操作确认页（2026-10-29）

- 本机 Manager 与 Web 均为 `dev-external-mcp-confirm-20261029`，只更新镜像；保留配置、挂载和数据库。WorkBuddy 的 `mcp.json`、`settings.json`、`mcp-approvals.json`、`app/app-config.json` 指纹均未变化。
- 所有新写入先返回 awaiting_approval 与 `/approvals/{approval_id}`。真实 HTTPS / MySQL 验证：只打开链接不执行；点击一次“确认并执行”后自动显示完成；同键重试返回原记录，第二次确认被拒绝。真实浏览器拒绝后操作变为 cancelled，没有创建资源。
- 隔离测试拓扑节点和临时 PAT 已删除；审计记录保留。参数脱敏、`healthz` 与 `readyz` 均通过。
- 6 个受影响 Go 包的 race 测试通过。前端 12 项交互测试、构建与 lint 通过（0 error、43 条既有 warning）。17 项真实 Chromium 回归覆盖四档宽度、中英文、深浅主题及登录返回；实看深浅主题截图。回归文件：[直达确认页](../../web/e2e/approval-confirmation.spec.ts)，原审批列表回归保留。
- `make proto` 及新增字段所在的 approval proto lint 通过。`make arch-lint` 仍被两项先前已有问题阻断：生成的 K8s API 对 Edge API 的依赖、node_modules 下的 flatted Go 文件未归类；未修改这些无关规则。
- 本轮未重跑全量 Go 测试，未提交 PR、未部署生产，未验证 WorkBuddy 模型如何呈现新链接及真实 Kubernetes/主机动作。

以下是历史部署和策略的验证记录；其中“客户端确认后直接执行”已被上面的服务端确认流程替换。

## 已有本机环境更新（历史）

同日按用户要求更新现有 `ongrid-native-deps` Compose 项目的 Manager 和 nginx。当前实际环境为 OrbStack 容器；沿用全部既有配置、数据库、挂载与 Edge，只有两个镜像引用和三个 MCP 环境变量变化。

- 当时 Manager 版本：`dev-external-mcp-20261029.1`；Web 版本：`dev-external-mcp-20261029.3`，Linux ARM64。第二次更新新增 Token 编辑及删除，第三次统一名称为“MCP 访问 / MCP access”；第四次扩展能力目录和权限，第五次修正审批页面窄屏布局及参数可读性。2026-10-29 的 Web 更新增加批量权限选择并区分审批与审计名称；批量选择按用户要求改为单个“全选”复选框。Token 列表移除完整权限字符串，详细权限仍通过编辑弹窗查看和调整。随后单独更新 Manager，修复分页工具的 JSON Schema。
- 页面：`https://localhost:8443/settings/external-ai`；现有 PUBLIC_URL 对应的 MCP 地址已验证可达。
- 测试部署启用 MCP 与写入；代码及通用部署模板仍默认关闭。
- 更新前保存旧镜像标识、完整配置快照和约 10.8 MB 的数据库逻辑备份。
- `/healthz`、`/readyz` 与设置页正常；真实页面显示启用状态及四种 PAT 有效期。
- 使用两个 15 分钟有效的测试 PAT，通过真实 HTTPS 入口验证 initialize/通知、按权限发现工具、只读令牌伪造写请求被拒、设备查询、面板创建/更新/删除、同键重试、版本冲突和操作查询。
- 测试面板已删除，两个 PAT 均已撤销；撤销后的请求返回 401。数据库确认无残留测试面板或有效测试 PAT，四个操作的完成审计均已落库。

第二次更新后，通过真实 MySQL / HTTPS 接口验证：编辑名称和权限不更换 Token；保持数据库已有到期时间，也可改为不过期；新增权限可立即发现工具，收回权限后伪造写请求被拒；无效编辑不覆盖原值；删除后列表移除、旧 Token 返回 401，不能再编辑。数据库确认凭证记录已删除，创建、编辑、删除审计均保留。真实浏览器另验证编辑保存、保持到期时间、取消删除以及确认删除，测试 Token 已清理。

这些结果证明本机运行实例的接入链路，不代表豆包工作、WorkBuddy 或真实 Kubernetes/主机运维动作验收完成。

## 客户端确认策略（历史，已替换）

按用户要求取消所有新外部 MCP 写请求的 Ongrid 二次审批，人工确认由 WorkBuddy 等客户端负责。专用资源工具、主机/Kubernetes 动作、通知/页面工具和平台 API 均经同一个操作入口按 PAT 权限执行。服务端不证明客户端是否弹出确认窗口；预检查、版本校验、幂等、加密和审计保持有效。

- 本机 Manager 更新至 `dev-external-mcp-20261029.2`，Web 更新至 `dev-external-mcp-20261029.4`。仅更新两个镜像引用，保存配置和回滚快照；环境变量、挂载及 WorkBuddy 配置未改。
- biz/service/server 三个 externalmcp 包及 `cmd/ongrid` 的 race 测试通过。回归确认 API 写入直接入队、不创建 Approval、请求仍加密、幂等重试不重复执行；旧审批的恢复和取消仍有效。
- 真实 HTTPS / MySQL：只读 PAT 隐藏写工具且伪造写请求被拒；操作发现和 dry_run 返回 `approval_required=false`，预览不写入。独立临时拓扑节点创建及删除均无需平台审批即可成功，读回验证通过，同键重试返回原 Operation ID。
- 两个临时 PAT 已删除，旧凭证访问返回 401；临时节点无残留。`/healthz` 与 `/readyz` 均为 200。
- 前端 8 项测试、构建与 lint 通过（0 error、43 条既有 warning）。真实浏览器完成 390/768/1280/1920px × 中英文 × 深浅主题的页面及创建/编辑检查，共 32 个弹窗场景；确认说明可读且无溢出。本机真实页面已核对新文案，四套 Skill 格式校验通过。
- 历史 awaiting_approval 不因升级自动执行；原 PAT 可取消后，再经客户端确认并使用新幂等键提交。本次未批准、取消或执行用户先前提交的真实删除请求。
- 未通过 WorkBuddy 执行真实删除/重启，未验证真实 Kubernetes/主机副作用，也未提交 PR。以下原“审批执行”验收记录属于此前版本，不代表当前新请求仍要求二次审批。

## WorkBuddy 连接修复（2026-10-29）

- WorkBuddy 5.9.3 日志显示首次发现工具失败：`list_monitor_panels` 和 `query_alert_rules` 的 `inputSchema.required` 为 `null`，客户端要求数组；紧随其后的连接尝试返回 HTTP 429，遮蔽了最初的 schema 错误。
- 在共用 `objectSchema` 入口将无必填项编码为 `[]`；回归覆盖所有资源工具的公开 schema。修改前两个分页工具的测试失败，修改后 service/biz/server 三个 externalmcp 包的完整 race 测试通过。
- 只更新 Ongrid Manager 镜像，保留原有 IP/PAT/owner 限流、并发与写入限制。未修改 WorkBuddy 的地址、Token 或任何配置。
- 使用 WorkBuddy 已保存配置手动重连后，管理窗口显示 Ongrid `51/51 个工具已启用`；健康与就绪检查均返回 200。此结果只证明连接和发现，未通过 WorkBuddy 执行真实资源动作。
- 原有连续突发请求仍可能返回 429，应遵守 `Retry-After`；本次未提高限额，也不把单次重连成功当作任意重连压力验收。

## 平台能力扩展验收

固定目录覆盖 34 个 API 模块、284 个操作；当前本机按实际装配路由可发现 283 个操作。管理员的完整权限 PAT 可发现 51 个 MCP 工具，设置页提供 97 项权限；旧 PAT 保持原有权限。能力定义、排除项和语义限制见[覆盖说明](../design/external-mcp-coverage.md)。

使用本机真实 HTTPS、MySQL 和两个短期 PAT 验证：

- initialize、工具与操作发现、具体操作参数 schema、只读查询；只读 PAT 不显示写工具，伪造写调用被拒绝。
- `call_api_write` 创建临时拓扑节点，审批前查询确认不存在。Operation 请求为密文；管理员审批内容显示普通参数，测试密码字段被隐藏，密文不返回审批页。
- 批准后 Operation 成功，结果明确为 `accepted_by_api`，另用资源接口读回确认节点存在；同一幂等键重试返回同一操作。
- 通过 MCP 提交删除，批准后成功，资源读回确认无测试节点残留。
- 删除两个测试 PAT，列表不再显示，使用原凭证访问均返回 401。只保留操作与审计记录。

两次扩展部署均保存旧镜像与 Compose 配置；能力扩展只改 Manager/Web 镜像，最终 UI 修复只改 nginx 镜像。已核对现有环境变量、挂载与其他服务未改变。健康与就绪检查均返回 200。

真实 Chromium 对 MCP 页面、97 项权限的编辑弹窗及审批页面完成 390/768/1280/1920px × 中英文 × 深浅主题检查，覆盖长名称、有效与到期 Token、权限首尾、操作按钮和参数显示。修复了审批正文被按钮挤压及状态筛选裁切；[浏览器回归](../../web/e2e/approvals-responsive.spec.ts)保留正文宽度、内容溢出和全部按钮边界断言，共 16 项通过。页面检查使用模拟 API；上述协议与副作用检查使用本机真实 API。

## 已通过

2026-10-29：创建和编辑弹窗使用单个“全选”复选框，勾选选中全部可见能力，再次点击清空；部分选中时显示半选状态，支持键盘空格切换。清空后禁止提交，点击批量选择不会保存。原 `approvals:read` 与 `audit:read` 是不同权限，分别显示为“操作审批”和“审计日志”。8 项前端回归、构建与 lint（0 error、43 条既有 warning）通过。真实浏览器完成 16 组宽度/语言/主题组合，每组同时检查创建和编辑，共 32 个弹窗场景；全部 97 个能力选中/清空、半选状态、名称唯一及布局边界通过，未签发或修改真实 Token。

| 检查 | 环境与结果 |
| --- | --- |
| PAT 生命周期 | Go race；创建、不过期、自定义到期、编辑权限及原凭证复用、审计失败回滚编辑、删除、旧撤销记录保持失效、跨账号隔离、账号禁用及降权；收回权限阻断排队写任务 |
| 持久操作与协议 | SQLite 和独立 MySQL 8.0；幂等并发、参数冲突、版本冲突、事务与审计回滚、审批恢复、PAT 归属、预检查单次消费、远端结果不确定时不重放、写暂停后查询与保留策略 |
| HTTP / MCP | SDK initialize、工具发现与调用、通知、Origin、认证、响应 no-store、权限过滤、复用会话标识不能串用其他 PAT |
| 受影响后端 | macOS Go 1.25.11 与 Linux Go 1.25 容器；受影响包 `go test -race` 通过，含共享工具、alert/device/monitor 路径 |
| 扩展能力与审批 | Go race；目录按身份和权限过滤、禁止路径跳转与伪造 Header、超大响应、dry-run 不调用 handler、静态路由覆盖清单、审批请求加密与恢复、递归脱敏、PAT 不得读取解密参数 |
| 前端行为 | `npm test -- src/pages/settings/ExternalAI.test.tsx`：8 项通过；覆盖不过期及一次性明文、创建/编辑失败保留输入、修改权限并保持精确到期时间、取消编辑、取消与确认删除、扩展权限依赖及创建/编辑批量权限选择 |
| 前端构建 | `npm run build` 通过；Vite 有大 chunk 提示 |
| 前端 lint | `npm run lint`：0 error、43 warning；告警不在本次新页面中 |
| 页面 | 真实 Chromium + 模拟 API；390/768/1280/1920px × 中英文 × light/dark，共 16 组；页面、编辑弹窗和删除确认无页面级横向溢出，编辑/删除按钮位于所属行内，覆盖长名称、有效/到期状态及全部权限；首次实现另验证创建弹窗 |
| Proto | `make proto` 完成；使用 Makefile 同样的 Buf STANDARD/PACKAGE_DIRECTORY_MATCH 例外对 `api/iam/v1/iam.proto` 检查通过；生成目录沿用仓库忽略规则 |
| Skill | 四套 `integrations/external-ai/skills/` 通过 skill-creator 的 `quick_validate.py` |
| 部署配置 | 两份 Compose 使用虚构占位环境变量执行 `config --quiet` 通过；`make verify-compose-images` 与 `bash scripts/test-compose-release-package.sh` 通过 |
| 差异 | `git diff --check` 通过 |

## 全量检查的限制

本轮 Linux 隔离容器执行 `go test -p 2 -timeout 180s ./...`，仅 `edge/TestFileBundleResolverReportsInaccessibleBundle` 失败：容器 root 可读原本不可读的测试目录。该项在非 root 宿主机用 `-count=1` 重跑通过。首次验证中知识库测试需要的宿主机绝对路径缺失问题，本轮已通过提供同路径源码解决。不能将分环境结果写成单次全量通过；未运行全仓库 race、完整 integration/e2e 标签套件或线上 E2E。

默认 Playwright 配置指定 Google Chrome，本机未安装该浏览器，首次启动失败；改用已安装的 `E2E_BROWSER_CHANNEL=chromium` 后 16 项回归通过，未安装或修改浏览器依赖。

原样 `make arch-lint` 有两项告警：生成的 `api/gen/manager/k8s` 依赖同组件的 `api/gen/manager/edge`，以及扫描到 `web/node_modules/flatted/golang`。临时配置仅允许 api 组件自身依赖并排除 node_modules 后通过；仓库架构配置未修改。这个结果不能替代原始架构检查通过。

## 可复现入口

```sh
go test -race ./internal/iam/... ./internal/manager/biz/externalmcp \
  ./internal/manager/service/externalmcp ./internal/manager/biz/approval \
  ./internal/manager/biz/audit ./internal/pkg/dbx ./internal/pkg/config ./cmd/ongrid

# 使用专用测试数据库账号。测试创建随机库并在退出时删除，不使用生产 DSN。
ONGRID_MCP_TEST_MYSQL_DSN='<isolated MySQL DSN with parseTime=true>' \
  go test -race -count=1 ./internal/manager/biz/externalmcp

cd web
npm test -- src/pages/settings/ExternalAI.test.tsx
npm run build
npm run lint

# 指向已启动的隔离前端预览；使用模拟 API，不运行真实账号登录 setup。
E2E_BASE_URL=http://127.0.0.1:4187 E2E_BROWSER_CHANNEL=chromium \
  npm run test:e2e -- e2e/approvals-responsive.spec.ts --project=chromium --no-deps
```

Linux-only 共享工具包在 Linux 环境运行 race；不要用 macOS 的编译限制判定为业务回归。页面截图和尺寸断言来自模拟接口，只证明前端显示及布局。

## 发布前仍需验证

- 真实客户端版本、网络来源与私有部署可达性，握手、工具调用和四套 Skill 的使用。
- 实际 Kubernetes controller、主机服务重启及 Grafana 同步等副作用与读回。
- 多实例容量、后端查询成本及审计/数据库故障演练；本地并发回归不代表容量结论。
- 远端 CI、依赖漏洞与镜像检查。远端状态以当前 PR 检查为准，尚未发布。

接入与写入默认关闭；部署操作、验收步骤和回滚限制见[接入指南](../install/external-mcp.md)。

## 按风险确认（2026-10-29）

本机 Manager 更新为 `dev-external-mcp-risk-20261029`，Web 保持原确认页版本。普通新建与名称/说明修改按[确认范围](../design/external-mcp.md#确认范围)免确认；删除、重启等高风险动作与旧待确认记录仍使用一次确认页。知识文档更新是完整覆盖写，当前不属于独立改名。

本轮 4 个 Go 包 race 通过（biz/service/server externalmcp 与 server/approval），覆盖普通创建和改名、删除确认、并发幂等、混合字段不放行、未知动作默认确认和旧待确认请求不自动执行。Manager 构建通过，`git diff --check` 通过。

真实本机 MCP 验证创建/改名无需审批且读回一致；重复键不重复执行；删除待确认时保留节点，打开详情不执行，确认后删除。临时节点与 PAT 已清理。仅替换 Manager 镜像，Web 容器、环境、挂载、端口与 WorkBuddy 四份配置未变，healthz/readyz 均为 200。证据在本机 `output/deploy/external-mcp-20261028/risk-confirmation-20261029/acceptance.json`。

本轮未重跑全量 Go、UI 或 WorkBuddy 模型端真实调用；未提交、建 PR 或部署生产。没有 UI 变更，因此不新增截图验收。既有全仓架构检查限制仍见前文。

## MCP 请求配额调整（2026-10-29）

本机 Manager 更新为 `dev-external-mcp-limits-20261029`。已认证只读查询、工具发现和轮询取消频率配额；写工具每 PAT 120/min、burst 20；每 PAT/owner/单实例 HTTP 并发为 10/20/40，总并发覆盖认证阶段。持久化 Operation worker 并发未变。完整边界见[预算说明](../design/external-mcp.md#结果审计与预算)。

server/biz externalmcp 与 iam/biz/integrationtoken 三个包的 race 通过。新增回归覆盖连续 100 次查询/发现、写操作 burst 与 retryable、认证失败不阻塞同代理合法 PAT、PAT/owner/实例并发边界、认证阶段总并发和取消请求释放名额。验证时发现 SDK 旧版协议不传递 HTTP 取消，已在无状态只读工具入口补充取消联动；持久化写入恢复语义保持不变。Manager 镜像构建通过。

真实本机 MCP 在 0.511 秒内完成 60 次连续发现/查询。60 次写 dry-run 在 0.435 秒内前 20 次放行、后 40 次返回 RATE_LIMITED 且 retryable=true，之后只读查询仍成功。40 次失败认证中前 30 次返回 401、后 10 次返回 429，同代理合法 PAT 仍成功。无业务资源创建，临时 PAT 已删除；审计记录保留。

仅替换 Manager 镜像，Web 容器、环境变量、挂载、端口与 WorkBuddy 四份配置语义未变，healthz/readyz 均为 200。证据和回滚说明在本机 `output/deploy/external-mcp-20261028/mcp-limits-20261029/README.md`。未重跑全量 Go、UI、WorkBuddy 模型端调用或生产容量压测；未提交、建 PR 或部署生产。所有额度均为单实例计数，多实例不共享。

## 客户端接入指南（2026-10-29）

「设置 → MCP 访问」增加 WorkBuddy / 豆包工作切换指引，包含当前官方入口、保存与启用步骤、配置示例和官方文档链接，不包含验证提示词。WorkBuddy JSON 使用当前服务地址，Token 始终为占位符；豆包工作展示 HTTP URL 和 Authorization 请求头。相对与绝对地址的复制行为有回归覆盖。

10 项前端测试、前端构建通过；lint 为 0 error、43 条既有 warning。16 项真实 Chromium 回归覆盖 390/768/1280/1920px × 中英文 × 深浅主题，每项检查两个客户端页签、长地址、长名称、控件边界和键盘切换，见 [接入指南回归](../../web/e2e/external-mcp-guide.spec.ts)。本机真实页面另核对两个页签和深浅主题，实看截图并恢复显示偏好。

本机仅更新 Web 为 `ongrid-web:dev-external-mcp-guide-20261029`；Manager 仍为 `dev-external-mcp-limits-20261029`，容器未替换。环境、挂载和端口保持一致，healthz/readyz 为 200。证据在本机 `output/deploy/external-mcp-20261028/mcp-client-guides-20261029/`。未新增实际客户端连接或重跑后端测试，未建 PR 或部署生产。

## 接入指南按需展示（2026-10-29）

指南默认隐藏；点击「MCP 访问」页头的「接入指南」按钮后，在公共 Modal 中查看 WorkBuddy / 豆包工作指引。保留配置复制与弹窗内复制反馈。相关回归覆盖默认隐藏、打开、关闭、Escape、焦点返回和客户端切换；布局检查继续覆盖四档宽度、中英文及深浅主题。

10 项前端测试、16 项 Chromium 回归及前端构建通过，lint 0 error、43 条既有 warning；本机页面和弹窗的深浅截图已实看。仅更新 Web 为 `ongrid-web:dev-external-mcp-guide-modal-20261029`，Manager 容器、环境、挂载和端口未变，healthz/readyz 为 200。证据与回滚方式在本机 `output/deploy/external-mcp-20261028/guide-modal-20261029/`。未提交、建 PR 或部署生产，未重跑后端与外部客户端调用。

## PR 前验证（2026-10-29）

按最终实现复核运行结果：IAM/PAT、MCP biz/service/server、审批、审计、设备、面板、告警、共享事务/配置与 Manager 装配的 race 测试通过；Linux 容器中的共享工具及受影响存储包 race 测试通过。Linux 全量 `go test -p 2 -timeout 180s ./...` 仅因 root 可绕过目录权限导致 `TestFileBundleResolverReportsInaccessibleBundle` 失败，该项在非 root 宿主机 `go test -race -count=1` 通过。仍不能记为单次全量通过。

PAT 设置页与操作确认页共 14 项前端测试通过；最终前端镜像构建通过，lint 为 0 error、43 条既有 warning。接入指南的 16 项 Chromium 回归覆盖四档宽度、中英文、深浅主题及键盘交互；最后的 MCP 图标调整另实看深浅主题截图，图标与模型项均为 14px，页面无横向溢出。

`make proto`、两份变更 proto 的同配置 Buf lint 与 `git diff --check` 通过。原样 `make arch-lint` 仍报告前文记录的两项问题。现有工作流未包含 govulncheck、依赖漏洞与镜像漏洞检查；本次不改动无关 CI 配置。未执行生产容量、真实主机/Kubernetes 动作或豆包工作端到端验收，入口和写入默认关闭。

## 审查修复回归（2026-10-29）

拓扑节点类型和关系类型使用 upsert，统一要求确认。真实拓扑 Handler 回归验证已有关系类型在确认前不变，确认后才更新方向、语义与故障传播设置；风险策略测试同时覆盖两个类型接口。

静默请求在恢复已有幂等记录后才检查当前截止时间。回归验证已完成和待确认操作过期后仍返回原记录；同键改参仍冲突；过期、超过 30 天和格式错误的新请求及过期 dry-run 被拒绝；无效请求不占用幂等键，过期后确认不会新增静默。两个集成回归在修复前失败，修复后相关 MCP 包的 race 检查通过。

本轮 `go test ./...` 仍受 macOS 上的 Edge 平台构建限制及既有 `TestValidateEntry_DestWhitelist` 大小写路径失败影响。`make arch-lint` 仍报已有生成 K8s→Edge API 依赖和 `web/node_modules/flatted` 未分类文件，共 2 项，与修复前一致；不能记为全量检查通过。

本轮未更新本机或生产部署，未重跑外部客户端与 MySQL 验收。

## 取消审批修复回归（2026-10-29）

取消 MCP 操作时，在同一事务内使关联的待审批记录失效；复用 `InvalidatePending`，不覆盖已经做出的审批决定。取消后的待审批计数归零，重复取消返回相同操作；模拟超过 30 天的 Operation 清理后，保留的审批详情仍返回 HTTP 200。注入审批更新失败时，Operation 状态、终态时间和取消审计一并回滚。

相关 MCP 与审批包的 Go race 检查通过，新增取消/清理/回滚测试及既有审批恢复测试通过。确认页优先展示 Operation 的“已取消”状态，4 项前端单元测试通过；17 项 Chromium 测试通过，覆盖 390/768/1280/1920px、中英文及深浅主题，并实看取消状态的 light/dark 截图。`npm run build`、`npm run lint`（0 error、43 条既有 warning）和 `git diff --check` 通过。

本轮重跑 `go test ./...` 仍有既有 macOS Edge 平台构建及 `TestValidateEntry_DestWhitelist` 路径大小写问题。未重跑 MySQL、外部客户端、全仓库 race 或生产验收；未更新部署。

## 接入指南暂时仅保留 WorkBuddy（2026-10-30）

豆包工作接入尚未验证通过，已从控制台接入指南和安装文档中移除其配置说明、入口与官方链接。指南弹窗直接展示 WorkBuddy，不再提供客户端切换；WorkBuddy 配置、复制及关闭行为保持不变。

10 项前端单元测试、16 项 Chromium 回归、`npm run build`、`npm run lint`（0 error、43 条既有 warning）及 `git diff --check` 通过。浏览器覆盖 390/768/1280/1920px、中英文、深浅主题、长地址、键盘开关弹窗和焦点返回，并实看页面及弹窗的 light/dark 截图。本轮仅调整指南，未重跑后端或真实客户端接入，未更新部署。
