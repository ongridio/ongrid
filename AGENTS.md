# AGENTS.md

本文件维护 Ongrid 的项目约定。修改规则时核对代码、CI 和关联文档；不要用外部模板覆盖本文件。

## 工作方式与规范入口

- 开始前检查当前分支、工作区改动和适用的子目录说明。保留用户已有工作，只修改本次任务涉及的内容。
- 修复前读完整调用链，搜索相关函数的调用方；优先在共同入口修复根因，复用已有组件、工具和依赖。
- 项目规则以本文件、[贡献指南](CONTRIBUTING.md)、[安全政策](SECURITY.md)及相关专题文档为准。发现冲突时指出具体条款，避免借小改动迁移架构、协议或数据模型。
- 前端任务必读[前端设计语言与开发规范](docs/design/frontend-design-language.md)；依赖边界查 [.go-arch-lint.yml](.go-arch-lint.yml)；构建与测试入口查 [Makefile](Makefile)、[前端脚本](web/package.json)和 [.github/workflows](.github/workflows)。
- [gospec 参考版本](https://github.com/singchia/gospec/blob/0eb883fb40b62b509125d35a1c27e7e6a238d390/spec/spec.md)固定为 `0eb883fb40b62b509125d35a1c27e7e6a238d390`。项目规则未覆盖时，按其任务路由表只读相关的 1–3 个专题；外部规范不覆盖项目规则。
- 如需读取本地 gospec，先查 `.claude/skills/gospec/spec/spec.md`，再查 `~/.claude/skills/gospec/spec/spec.md`，并核对上述版本。缺失或版本不符时可读取固定版本链接；不要自动安装或更新个人目录。无法读取时说明未核对项，继续不依赖它的工作。

## 架构与代码

| 位置 | 职责与依赖 |
| --- | --- |
| `cmd/ongrid`、`cmd/ongrid-edge` | 配置、依赖装配、启动和退出；Manager 装配 iam/manager，Edge 装配 edgeagent |
| `internal/<domain>/server` | HTTP 服务、路由、中间件、请求解析和响应 |
| `internal/<domain>/service` | 服务入口、校验与用例调用；不得直接依赖 data |
| `internal/<domain>/biz` | 业务用例和消费方 Repo 接口；不依赖 data 的具体实现 |
| `internal/<domain>/data` | 实现 biz 的 Repo 接口，处理存储和外部数据访问 |
| `internal/<domain>/model` | 领域与持久化数据结构，不依赖上层 |
| `internal/pkg` | 共享基础设施，不依赖 iam、manager、edgeagent |
| `web` | React 控制台，不是 Go 后端的一层 |

- iam、manager、edgeagent 之间禁止直接 import；跨域通过接口装配、API 或事件协作。允许的依赖以 `.go-arch-lint.yml` 为准，新增目录检查 [CODEOWNERS](CODEOWNERS) 覆盖。
- 接口定义在消费方，依赖通过构造函数注入。先复用现有实现，不为单一实现新增不必要的接口、工厂或配置层。
- 新增 IO 路径传递 `context.Context`，尊重超时与取消；goroutine 必须有退出条件。共享可变状态使用锁、原子操作或明确的所有权保护，并运行 race 检查。
- 错误补充上下文时用 `%w`；在处理边界记录，避免重复日志。不得静默丢弃错误，确需忽略时说明原因。
- `init()` 仅用于注册，不做 IO 或可能 panic 的初始化；禁止全局可变业务状态。公共 API 避免不必要的 `any`，解码或 SDK 适配例外需就近说明。

## API 与数据

- 修改 API 时同步对应的 `api/**/*.proto`、HTTP DTO、前端调用和相关测试；生成内容通过 `make proto` 更新，禁止手改生成代码。新增接口先检查所在模块的契约和生成方式。
- 保持现有 HTTP 状态码、响应结构和错误结构。例如 Edge 列表使用 `{items, total}`，不要在局部修复中加上 `{code, message, data}` 包装。破坏性变更需要版本或迁移方案。
- Handler 说明方法、路由、权限、请求和响应；接入 Swagger 的模块沿用其注释与生成流程。
- 数据库变更沿用 `internal/<domain>/data/**/migrate.go` 和 `dbx.RunMigrations` 入口。提交迁移或回填代码，检查幂等性、旧数据兼容和回滚限制；不要在普通修复中引入第二套迁移框架。
- MySQL schema 变更兼容滚动发布；大表变更评估锁表和在线 DDL。SQLite 是可选后端，修改共享存储逻辑时检查两种方言的适用范围。
- 仅在实际使用对应存储的路径应用其约束：Redis 缓存 key 设 TTL，持久状态说明生命周期，限制 key/value 大小，分布式锁校验 owner；ClickHouse 批量写入，复制与排序键按部署拓扑和查询设计；InfluxDB 控制 tag 基数并设置 retention。不要为满足模板引入存储依赖。

## 前端 UI

- [设计规范](docs/design/frontend-design-language.md)集中维护尺寸、配色、布局、弹层及表格操作列规则；组件 API 以 `web/src/components/ui/` 为准。公共规则变更在同一 PR 更新规范与相关测试。
- 复用现有 shadcn/ui 派生组件、Base UI 交互和 `.og-*` 样式，保留 Tailwind 3 与主题变量；不在页面另建组件库或主题。历史页面的不一致不能作为新规范。
- 用户文案统一使用 `tr('中文', 'English')`，覆盖占位符、错误、Tooltip 和可访问名称。使用语义色，检查 light/dark、中英文、窄屏及 Portal 浮层。
- 保留键盘操作、焦点提示、字段标签和图标按钮名称；区分加载、空态和失败，防止重复提交，失败保留输入，取消不能执行动作。
- 视觉变更提交前按设计规范实看真实浏览器截图，light/dark 各一张；无法完成时如实说明。纯文档和非 UI 变更无需截图。表格操作列还需检查横向滚动、不同权限和按钮数量。

## 安全与可观测性

- 保留现有认证、授权和资源归属检查，复用 `internal/pkg/auth`、`authzmw`、`tenantctx`。当前 `tenantctx` 表示调用者身份，不代表所有表都有 `tenant_id`；新增多租户能力需明确隔离模型并验证越权路径。
- 在信任边界校验输入；SQL 值必须参数化，动态标识符使用白名单。密码使用 bcrypt/argon2id；密钥、token、DSN 密码不得进入代码、镜像、日志或提交。
- PII 按项目数据保护要求加密存储，测试不使用生产明文数据。安全漏洞按 `SECURITY.md` 私密报告。
- 容器默认非 root；宿主机采集需要的权限遵循现有部署设计，权限变更验证最小能力集合。CI 必须包含 `govulncheck`、依赖与镜像漏洞检查，缺失时如实报告。
- 对外服务提供 `/healthz`、`/readyz`、`/metrics`；结构化日志保留错误链和请求 trace 上下文。Prometheus label 不使用 user_id、email、任意 URL 等高基数字段。

## 并发、容量与运维

- 涉及连接池、并发任务、队列或批量上报时，检查资源上限、超时、取消、释放和背压。数据库连接预算计入所有服务实例及其他客户端，不按 VM 数量直接配置连接数。
- 重试必须有边界、退避和抖动，并确认操作可安全重复；依赖拥塞时避免立即重注册或层层重试放大负载。容量结论需说明节点数、上报周期、资源配置和负载证据。
- 故障排查先确认版本、部署拓扑、报错服务和完整错误链；明确区分代码发现、根因推测与现场证据。
- 部署、配置和数据变更说明回滚方式与限制；高风险变更使用金丝雀或 feature flag。告警附 Runbook，P0/P1 事故恢复后记录无责复盘；仅改文档无需部署回滚方案。

## 验证与交付

先运行受影响路径的检查；新增行为和缺陷修复保留能覆盖关键行为的回归测试。按影响扩大范围，不能用局部通过替代项目要求的完整检查。

| 改动范围 | 验证入口 |
| --- | --- |
| Go 逻辑 | 对受影响包执行 `go test -race`；PR 前按贡献指南运行 `go test ./...`，全量 race 用 `make test-race`；CI 另有 build/vet |
| 依赖边界 | `make arch-lint`；工具缺失时该目标会跳过，不能记为检查通过 |
| 前端逻辑或 UI | 在 `web` 运行 `npm test -- <相关测试路径>`、`npm run build`、`npm run lint`；视觉验收另按上文执行 |
| Proto | `make proto` 并检查生成差异；`make lint-proto` 当前只覆盖 k8s/setting，其他变更文件需用相同 Buf 配置补充 `--path` 检查 |
| 集成或 E2E | 按影响运行 `make test-integration` / `make test-e2e`；使用隔离数据并清理；live 检查先确认目标环境与授权 |
| 部署或发布脚本 | 运行 Makefile 中对应的 Chart、安装包或发布脚本检查，说明未覆盖的平台与运行时验证 |
| 纯文档 | 检查差异、链接、路径、命令与现有规则的一致性，并运行 `git diff --check`；无需构建或截图 |

- 沟通默认中文，已有文档和注释延续所在文件的语言。**PR 标题、commit 标题和正文必须使用英文；标题遵循 Conventional Commits。** 规则见 `CONTRIBUTING.md` 和 [.github/workflows/commit-policy.yml](.github/workflows/commit-policy.yml)。
- 分支使用 `feat/...`、`fix/...`、`chore/...`，不加 `[codex]`；一个 PR 处理一个逻辑变更。所有变更通过 PR，不直接推送或 force push main/master。
- PR 使用 [.github/pull_request_template.md](.github/pull_request_template.md)，原样保留 Author confirmation，由 PR 作者确认贡献条款，不代替其他作者勾选。
- 提交 PR 前核对相关规则和测试，创建或更新后运行 `gh pr checks <number> --repo ongridio/ongrid`。检查失败、等待中和环境限制均需明确说明；代码验证、CI 通过、合并和发布是不同状态。

## 需求与文档范围

- 局部 Bug、小功能、配置、文档和范围明确的性能优化，在 Issue 或 PR 写清问题、方案与验证即可。
- 跨模块、影响兼容性或存在重要方案取舍时，补 RFC/ADR；较复杂的用户流程补 PRD，跨多个需求的战略用 Epic。按影响和不确定性决定，不仅按改动类别决定。
- 沿用仓库已有文档目录：设计决策放 `docs/design/`，部署说明放 `docs/install/`，需求和 RFC 分别放 `docs/requirements/`、`docs/rfc/`。README 保持简洁，多语言内容按贡献指南同步。
