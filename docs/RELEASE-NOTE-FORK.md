# Release Notes — v0.9.0 (Fork)

> 本 Release Note 对比 **ShawnXue-AI/ongrid-for-db** 与上游 **ongridio/ongrid** (`v0.9.0`) 的功能差异。
>
> Base commit (upstream): `73adefc` — Merge pull request #151 from ongridio/docs/v0.9.0-version-bump
> Fork HEAD: `4e3a2b0` — 47 files changed, +7315 / −158 (含文档)

---

## 新增功能概要

| 功能域 | 新文件 | 净增行数 | 复杂度 |
|--------|--------|---------|--------|
| 🗄️ **数据库管理 (Phase 1)** | 18 个新文件 | ~+3300 | ⭐⭐⭐ |
| 🤖 **AI 数据库分析工具** | 6 个新文件 | ~+1200 | ⭐⭐⭐ |
| 📡 **数据库监控扩展 (MongoDB/Redis)** | 3 个新文件 | ~+850 | ⭐⭐ |
| 🛠️ **AI Agent 改进** | 0 个新文件(修改现有) | ~+200 | ⭐⭐ |
| 🔌 **Edge Agent 技能增强** | 2 个新文件 | ~+770 | ⭐⭐ |
| 🎨 **UI 改进** | 3 个新文件 | ~+1500 | ⭐⭐ |
| 🧪 **测试/CI 修复** | 0 个新文件(修改现有) | ~+50 | ⭐ |
| 📚 **文档** | 1 个新文件 | ~+1013 | — |

---

## 1. 🗄️ 数据库管理 (Phase 1)

### 后端

#### 数据库业务层 (`internal/manager/biz/database/`)

| 文件 | 功能 |
|------|------|
| `repo.go` | 数据库实例 CRUD 仓库接口 |
| `topology.go` | 数据库拓扑关系 — 连接 DB 实例 → Edge → Device 链路 |

#### 数据层 (`internal/manager/data/database/`)

| 文件 | 功能 |
|------|------|
| `store/store.go` | 数据库实例持久化 (GORM) — `database_instances` 表 CRUD |
| `store/provider.go` | 数据库供应商配置 (MySQL/PostgreSQL/Redis/MongoDB) |
| `store/migrate.go` | 数据库相关表的数据迁移 |
| `credentials/store.go` | 数据库凭证安全存储 — 加密存储连接字符串，支持按需解密 |

#### 模型层 (`internal/manager/model/database/`)

| 文件 | 功能 |
|------|------|
| `model.go` | `DatabaseInstance` 模型 — 类型/主机/端口/版本/状态/配置/凭证引用 |

#### HTTP API (`internal/manager/server/database/`)

| 文件 | 功能 |
|------|------|
| `http.go` | 完整 RESTful API (874 行) — 列表/详情/创建/编辑/删除/测试连接/拓扑 |
| `query_executor.go` | SQL 查询执行端点 — 安全转发的只读查询执行 |

#### 种子数据 (`internal/manager/data/alert/store/seed_rules.go`)

- 新增数据库相关的内置告警规则种子

### 前端

| 文件 | 功能 |
|------|------|
| `web/src/pages/Databases.tsx` (385 行) | 数据库列表页 — 搜索/筛选/状态总览 |
| `web/src/pages/DatabaseDetail.tsx` (937 行) | 数据库详情页 — 连接信息/指标/拓扑/慢查询可视化 |
| `web/src/api/databases.ts` (160 行) | 数据库 API 客户端 |
| `web/src/components/Sidebar.tsx` (+4 行) | 侧边栏添加"数据库"导航项 |

---

## 2. 🤖 AI 数据库分析工具

上游缺失的三个数据库 AI 工具现已补全并注册为 BaseTool：

### AI 工具

| 工具 | 文件 | 功能 |
|------|------|------|
| `query_database` | `aiops/tools/query_database.go` | 只读 SQL 查询工具 — AI Agent 可对远端数据库执行 SELECT/SHOW/EXPLAIN |
| `inspect_schema` | `aiops/tools/inspect_schema.go` | 数据库 Schema 检查 — AI Agent 可查看表结构/索引/视图 |
| `database_query_basetool` | `aiops/tools/database_query_basetool.go` | 统一 BaseTool 实现 — 设备解析 → 凭证注入 → SQL 执行管线 |

### AI 分析增强

- **凭证自动解析** (`feat: resolve database connection params automatically from database_id`) — AI Agent 收到 `database_id` 后自动从凭证库解析连接参数，无需用户手动提供
- **首次使用存储凭证** (`feat: store db credentials on first use`) — AI Agent 首次连接数据库后自动加密存储凭证
- **智能 SQL 分析** (`feat: store db credentials on first use; add per-SQL AI analysis`) — 每次 SQL 查询结果附带 AI 分析，帮助理解慢查询根因
- **AgentTool 去重** (`fix: AgentTool duplicate output`) — 相同 `(subagent_type, prompt)` 在 90 秒内返回缓存，防止 ReAct 循环

### 分析 Prompt 优化

| 提交 | 改进 |
|------|------|
| `fix: include database_id and edge_id in AI analysis prompt` | AI 分析 prompt 中携带 `database_id` 和 `edge_id`，提供完整上下文 |
| `fix: replace /chat/new link with createSession+navigate` | AI 分析页面跳转优化 |
| `fix: prevent nested worker graph from overwriting parent tool_call_id` | 修复嵌套 Worker 的 `tool_call_id` 冲突 |

### 数据库发现 (Edge → Manager)

- `MethodPushDBInstanceInfo` (`internal/pkg/tunnel/messages.go`) — 新增隧道消息类型，Edge Agent 自动发现并上报数据库实例元数据（版本、配置参数、连接状态）
- `internal/edgeagent/plugins/dbcli/discover.go` (283 行) — Edge 侧数据库自动发现逻辑

---

## 3. 📡 数据库监控扩展

### MongoDB 支持

| 提交 | 改进 |
|------|------|
| `feat(aiops,database): add MongoDB wire-protocol version detection` | 新增 MongoDB 有线协议版本检测 |
| 隐含 | MongoDB 连接数/操作/内存/复制集等自定义指标 |
| `internal/edgeagent/plugins/databasemetrics/spec.go` (+44 行) | 扩展数据库导出器规格文件 |

### Redis/MongoDB 探测技能

| 文件 | 功能 |
|------|------|
| `internal/skill/builtin/probe_database.go` (524 行) | `host_probe_database` 技能 — TCP 连接 + 协议握手 + 版本检测 |
| 支持类型 | MySQL (3306), PostgreSQL (5432), Redis (6379), MongoDB (27017) |

### dbcli 层

| 文件 | 功能 |
|------|------|
| `internal/edgeagent/plugins/dbcli/pool.go` (140 行) | 连接池管理 — MySQL driver 注册 + 连接复用 + 超时 |
| `internal/edgeagent/plugins/dbcli/discover.go` (283 行) | 实例发现 — 扫描已知端口 + 协议检测 + 元数据上报 |

### PromQL & 指标扩展

- MySQL driver 注册修复 (`fix: register mysql driver`)
- DB 探测端点 (`add db probe endpoint`)
- `db_exec_query` 技能 (247 行) — 只读 SQL 执行，支持 SELECT/SHOW/EXPLAIN/DESCRIBE/WITH

---

## 4. 🛠️ AI Agent 改进

### 协调员工具修复

| 提交 | 改进 |
|------|------|
| `fix: add list_metric_catalog to coordinator tool roster` | `list_metric_catalog` 添加到协调员白名单 — 工具已注册但协调员无法调用的问题 |
| `fix: add ... apply_config_change to base prompt` | 基础提示词明确提及 `apply_config_change` 工具名 — 之前只描述了概念但未命名工具 |

### 测试守卫 (Test Guards)

`cmd/ongrid/coordinator_tools_test.go` 新增 4 个测试断言，防止回归：

| 测试 | 守卫内容 |
|------|---------|
| `TestCoordinatorRosterHasCodeTools` | 确保协调员工具白名单包含代码阅读工具 (`list_repo_sources`, `read_source`, `grep_source`) 及基线工具 |
| `TestBasePromptRequiresMetricCatalogBeforeAlertDraft` | 确保基础提示词包含完整告警配置流程 (`list_metric_catalog` → `draft_config_change` → `apply_config_change`) |

### 其他修复

| 提交 | 改进 |
|------|------|
| `fix(aiops): escape double quotes in error message strings` | 错误信息中的双引号转义，防止 LLM JSON 解析失败 |
| `fix(aiops): use ^uint32(0) instead of -1 literal for PutUint32` | 修复 `binary.PutUint32` 中 `-1` 字面量的类型安全 |
| `fix: run go mod tidy after upstream merge` | Go 依赖清理 |

---

## 5. 🔌 Edge Agent 技能增强

### 新技能

| 技能 | Key | Class | 行数 | 描述 |
|------|-----|-------|------|------|
| `host_probe_database` | `host_probe_database` | safe | 524 | 数据库连通性 + 协议版本检测 |
| `db_exec_query` | `db_exec_query` | safe | 247 | 只读 SQL 查询 (Edge 侧) |

### Edge 配置

- `cmd/ongrid-edge/main.go` (+29 行) — 注册 `dbcli` 插件，包括 `pool.go` 和 `discover.go`
- 数据库指标导出器 (`databasemetrics`) 扩展支持更多数据库类型和指标

---

## 6. 🎨 UI 改进

### 新页面

| 页面 | 行数 | 功能 |
|------|------|------|
| `Databases.tsx` | 385 | 数据库管理列表页 |
| `DatabaseDetail.tsx` | 937 | 数据库详情页 — 慢查询可视化/拓扑/连接信息 |

### 修复

| 提交 | 改进 |
|------|------|
| `fix(ui): add search to device page` | 设备页面添加搜索功能 |
| `fix(ui): fix DB instance type filter` | 修复数据库实例类型筛选器 |
| `fix(ui): restore Edges.tsx Chinese characters` | 修复 UTF-16 编码导致的中文乱码 |
| `style: align database UI with app-wide patterns` | 对齐数据库 UI 与全局设计规范 (PageHeader/Card/Button/borders) |
| `fix: resolve frontend TS errors` | 修复 `usePoll` 布尔参数和 `Modal open` prop 的类型错误 |

---

## 7. 🐛 Bug Fixes & Code Quality

### 语法修复 (Code Review Batch 2)

| 提交 | 修复 |
|------|------|
| `fix: resolve code review findings (batch 2)` | 10 个安全/正确性/性能问题 |
| `fix: restore truncated function bodies in inspect_schema.go` | 恢复被截断的函数体 (+86 行) |
| `fix: restore misplaced useState closing in Databases.tsx` | 修复 TypeScript 语法错误 |
| `fix: add missing strconv import` | 补充丢失的 import |
| `fix: remove stray 't' character in topology model.go` | 移除 `model.go` 中的多余字符 't' |
| `fix: remove unused encoding/json import in dbcli` | 移除未使用的 import |
| `fix: move SetDBQueryExecutor after fbClient init` | 修正初始化顺序 |
| `chore: bump VERSION` | 版本号更新 (v0.8.7 → v0.8.9 → v0.9.0) |

### CRLF 警告修复
- 工具修复提交显式处理 Windows 换行符警告

---

## 8. 📚 文档

| 文件 | 内容 |
|------|------|
| `docs/AI-FUNCTIONAL-REQUIREMENTS.md` | ~850 行功能需求文档 + 13 个 Mermaid 流程图 |

---

## Quick Stats

```text
Upstream base: 73adefc (ongridio/ongrid v0.9.0)
Fork HEAD:     4e3a2b0
Commits:       34 commits beyond upstream
New files:     20 (non-doc)
Changes:       +5750 / −158 (non-doc)

Languages affected:
   Go (backend):   ~3200 lines  (17 files)
   TypeScript/TSX: ~2000 lines  (7 files)
   Markdown:       ~1000 lines  (1 file)
   Yaml:           ~10 lines    (1 file)
```
