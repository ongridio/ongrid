# AGENTS.md

> 本项目遵循 [gospec](https://github.com/singchia/gospec) — Go 后端项目 SDLC 全流程规范。
>
> 本文件由 `scripts/install.sh` 自动生成。完整规范见 gospec 仓库。

## Agent 必读

任何编码 / 设计 / API / 数据 / 测试 / CI / 部署 / 监控 / 安全 / 文档 任务，**先按 gospec 规范走**。

涉及前端页面、组件、样式或交互时，**同时必读 [Ongrid 前端设计语言与开发规范](docs/design/frontend-design-language.md)**，并遵守下方「前端 UI」约束。该规范来自 [UI 统一 PR #395](https://github.com/ongridio/ongrid/pull/395)，适用于整个控制台，包括新增功能、既有页面修改及弹窗；不能仅凭单个历史页面的写法实现 UI。

### 第一步：找到 gospec 任务路由表

按以下顺序查找 spec 入口：

1. `~/.claude/skills/gospec/spec/spec.md`（个人安装，推荐）
2. `.claude/skills/gospec/spec/spec.md`（项目级安装）
3. 上面都不存在 → 重新安装：
   ```bash
   git clone https://github.com/singchia/gospec ~/.claude/skills/gospec
   ```

### 第二步：路由 → 加载

读 `spec/spec.md` 顶部的"任务路由表"，找到当前任务对应的 1-3 个子文件，**只读必要文件**，不要顺序读完整个 spec。

### 第三步：实施 + 自查

按子文件指引实施，结束前对照文件末尾的"自查清单"逐项核对。

### 第四步：PR 前对照 review 清单

提交 PR 前对照 `spec/07-code-review.md` 自查清单。

---

## 核心约束（无需读 spec 也要遵守）

> 这些是任何任务都要守的红线。不论 agent 是否加载了完整 spec，都不能违反。

### 架构
- **单服务**：`cmd → web → controlplane → repo → model`，禁止跨层调用
- **monorepo**：`internal/<domain>` 之间禁止直接 import，必须通过 API / 事件 / `internal/shared/`
- 接口在消费方定义，禁止循环依赖
- `utils/`、`lerrors/` 不依赖任何业务包
- 依赖通过构造函数注入，不使用全局变量
- 每个目录都被 CODEOWNERS 覆盖

### 编码
- 禁止 `_ = fn()` 忽略错误（确实想丢弃必须注释说明）
- 共享状态必须加锁，测试必须带 `-race`
- 错误用 `%w` 包装；不重复记录（要么处理要么传播）
- 所有涉及 IO 的函数第一个参数为 `context.Context`
- `init()` 仅允许做注册（pprof / metrics collector / driver），禁止做 IO 或可能 panic
- 禁止全局可变变量（只读单例 / collector 除外）
- 避免 `any` / `interface{}` 出现在公共 API 边界（解码 / SDK 适配等不可避免时就近注释）

### 前端 UI（全局统一约束）

- **规范优先**：[前端设计语言与开发规范](docs/design/frontend-design-language.md) 是公共设计规则的集中维护入口，组件 API 以 `web/src/components/ui/` 实现为准。未写明的布局参考设备、日志、监控等成熟页面的对应区域；历史页面的不一致不能作为新规范。新增或修改的 UI 必须融入现有控制台，不另建一套风格。
- **统一组件体系**：沿用仓库已有的 **shadcn/ui 风格与派生组件实现**，复杂交互基于 **Base UI**，统一封装在 `web/src/components/ui/`。页面复用这些组件，保留 Ongrid 的 Tailwind 3、主题变量和组件 API；新增控件先检查已有实现，缺失时在公共层按同一体系补齐，不在业务页面另写一套。
- **页面骨架**：复用 `PageHeader` / `Card` / `EmptyState` / `PaginationFooter`；统一页头、操作区、筛选栏、列表及分页。相关信息优先一个 `Card` 加 `divide-y`，不为每行重复套卡片。长文本使用 `min-w-0`，窄屏允许操作换行，密集表格只在容器内横向滚动。
- **表单与筛选**：从 `@/components/ui` 复用 `Input` / `Textarea` / `Select` / `Checkbox` / `Switch` / `Radio` / `Slider`。表单用 `Label` 关联字段，横向筛选用 `FilterField`，列表搜索用 `Input type="search"`；不手写另一套输入框、下拉框或开关。
- **尺寸与按钮**：常规控件沿用默认 36px，密集行内操作用 `Button size="sm"`（28px），独立图标操作用 `size="icon"`。主要操作用 `primary`，次要操作用 `outline` / `ghost`，危险操作用 `danger` / `dangerGhost`；不通过局部 `h-*`、`py-*`、字号和圆角拼出第三套常规尺寸。
- **弹层与导航**：复用 `Modal` / `Dialog` / `DropdownMenu` / `Hint` / `Popover` / `Tabs`，确认和输入请求用 `useDialogs`。弹窗使用预设尺寸，长内容在正文区滚动；不重写浮层定位、Escape、焦点管理或使用浏览器原生弹窗替代公共交互。监控类时间筛选复用 `TimeRangePicker`。
- **样式归属**：公共外观与状态在 `web/src/styles/index.css` 的 `.og-*` 和主题变量中维护。页面 `className` 主要补布局、宽度及代码字体，避免重复覆盖公共控件的背景、边框、圆角、字号和聚焦样式；优先扩展已有公共组件，不引入第二套组件库或主题。
- **语义配色**：新代码使用 `bg-bg` / `bg-card` / `border-border`、`text-text` / `text-text-muted` / `text-text-faint`。主操作沿用 indigo；成功 / 降级 / 异常 / 信息使用 emerald / amber / red / sky，状态复用 `Chip tone`，状态点用 `-500`。品牌 `--accent` 仅用于品牌区域，不铺成大面积操作区。
- **克制与图标**：正常态用小圆点加灰字，让异常突出；禁止 `animate-pulse`、发光阴影、`hover:scale` 和花哨的刷新徽章。通用图标用 `lucide-react`，品牌图标复用现有 `components/icons`；沿用公共字体与文字层级，不把紧凑元数据字号用于主要操作和必要提示。
- **明暗主题**：不得写死只适配深色的页面配色；检查 light / dark 的默认、悬停、聚焦、禁用、选中、错误及 Portal 浮层状态。维护旧 zinc 类时确认 light 覆盖，尤其透明度变体；不要局部叠加透明度降低提示可读性。
- **交互与可访问性**：导航用链接，动作用按钮；保留键盘操作和焦点提示，图标按钮提供可访问名称。区分加载、空数据、筛选无结果和失败；提交中防重复，失败保留输入，错误提供重试或修正入口，取消不能执行动作。
- **i18n**：所有用户文案使用 `tr('中文', 'English')`，包含占位符、空态、错误、Tooltip 和可访问名称；不在同一字符串中英并排，检查较长英文与长选项是否挤压布局。
- **验收与维护**：按设计规范末尾清单检查布局、交互、窄屏、中英文及明暗主题；视觉改动提交前必须通过真实浏览器截图实看，light / dark 各一张，纯文档变更无需截图。运行相关交互测试、构建与 lint，如实记录未验证项和既有失败。公共规则变更在同一 PR 更新设计规范与相关测试，避免页面各自演变。

### API
- 所有 API 变更先更新 `.proto`，禁止改生成代码
- Handler 必须有 Swagger 注释：`@Summary`、`@Router`、`@Success` 缺一不可
- 响应格式统一：`{code, message, data}`
- 破坏性变更走新版本，原版本只允许加非破坏性内容

### 测试
- 新功能必须有单元测试
- CI 强制启用 `-race`
- E2E 测试必须清理数据

### Git
- 提交格式：`<type>(<scope>): <desc>`（Conventional Commits）
- 禁止提交敏感信息（密码、密钥、token）
- 禁止 force push main/master

### 可观测性
- 所有对外服务必须暴露 `/healthz`、`/readyz`、`/metrics`
- 日志结构化（slog / zap）+ `trace_id`，ERROR 包含完整 error chain
- 高基数字段（user_id、email、url）禁止作为 Prometheus label
- 敏感字段禁止明文入日志

### 安全
- 密码必须用 bcrypt / argon2id，禁止 MD5 / SHA1
- SQL 全部参数化，禁止字符串拼接
- 密钥禁止进代码仓库 / 镜像 / 日志
- 容器以非 root 用户运行
- 多租户接口强制 `tenant_id` 过滤
- CI 必须包含 `govulncheck` + 依赖 / 镜像漏洞扫描

### 运维
- 任何变更必须有回滚方案
- 告警规则必须配 Runbook 链接
- 高风险变更走金丝雀或 feature flag
- P0 / P1 事故必须产出 blameless postmortem

### 数据存储
- **MySQL**：生产 schema 变更走 migration 文件；大表用在线 DDL 工具；变更兼容滚动发布（expand-contract）
- **Redis**：所有 key 必须设 TTL；禁止大 key（value > 10KB / 集合 > 5000）；分布式锁必须有 owner 校验
- **ClickHouse**：必须 Replicated engine；写入必须批量；ORDER BY 从低基数到高基数
- **InfluxDB**：tag 必须低基数（user_id / url 等禁止做 tag）；bucket 必须有 retention
- PII 字段加密存储，测试环境禁止生产数据明文

---

## 需求载体选择

不是所有变更都要写 PRD。按变更类型选载体（详见 `spec/01-requirement/`）：

| 变更类型 | 载体 |
|---------|------|
| Bug / 小改 / 配置 / 文档修复 | Issue（issue tracker） |
| 重构 / 升级依赖 / 性能优化（用户不感知） | RFC（`docs/rfc/RFC-XXX-*.md`） |
| 用户可感知的功能 / 业务变更 | PRD（`docs/requirements/PRD-XXX-*.md`） |
| 跨多个 PRD 的战略 | Epic（`docs/requirements/EPIC-XXX-*.md`） |

---

## 输出语言

默认中文（代码注释、文档、commit message）。

---

完整规范、所有子主题的具体细节、模板和自查清单见 `spec/spec.md` 的任务路由表。
