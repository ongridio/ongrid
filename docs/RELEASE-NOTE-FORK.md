# Release Notes — v0.9.0 (Fork)

本 Release Note 对比 **ShawnXue-AI/ongrid-for-db** 与上游 **ongridio/ongrid** (v0.9.0) 的功能差异。

Upstream base: `73adefc` — Merge pull request #151 from ongridio/docs/v0.9.0-version-bump
Fork HEAD:     `14362e3` — 34 commits, 20 个新文件, +5750 / −158 行代码 (不含文档)

---

## 新增功能概要

此 Fork 在上游 v0.9.0 基础上共新增 8 大功能域：

- **🗄️ 数据库管理 (Phase 1)** — 18 个新文件, ~+3300 行
- **🤖 AI 数据库分析工具** — 6 个新文件, ~+1200 行
- **📡 数据库监控扩展 (MongoDB/Redis)** — 3 个新文件, ~+850 行
- **🛠️ AI Agent 改进** — 修改现有文件, ~+200 行
- **🔌 Edge Agent 技能增强** — 2 个新文件, ~+770 行
- **🎨 UI 改进** — 3 个新文件, ~+1500 行
- **🧪 测试/CI 修复** — 修改现有文件, ~+50 行
- **📚 文档** — 1 个新文件, ~+1000 行

---

## 1. 🗄️ 数据库管理 (Phase 1)

上游完全没有数据库管理功能。此 Fork 从零实现了完整的数据库管理子系统，覆盖后端业务层、数据持久化、REST API、安全凭证存储和前端交互页面。

**后端** — 新增 `internal/manager/biz/database/` 目录，包含两个文件：
- `repo.go` — 数据库实例的 CRUD 仓库接口
- `topology.go` — 数据库拓扑关系，将 DB 实例与 Edge、Device 串联起来

**数据层** — 新增 `internal/manager/data/database/` 目录：
- `store/store.go` — 基于 GORM 的 `database_instances` 表 CRUD 实现
- `store/provider.go` — 数据库供应商配置，覆盖 MySQL、PostgreSQL、Redis、MongoDB
- `store/migrate.go` — 数据库相关表的数据迁移
- `credentials/store.go` — 数据库凭证安全存储，连接字符串加密存储，支持按需解密

**模型层** — 新增 `internal/manager/model/database/model.go`，定义了 `DatabaseInstance` 模型，包含类型、主机、端口、版本、运行状态、配置参数和凭证引用等字段。

**HTTP API** — 新增 `internal/manager/server/database/` 目录：
- `http.go` (874 行) — 完整的 RESTful API，支持数据库实例的列表、详情、创建、编辑、删除、连接测试和拓扑查询
- `query_executor.go` — SQL 查询执行端点，提供安全转发的只读查询执行

**种子数据** — 在 `internal/manager/data/alert/store/seed_rules.go` 中新增了数据库相关的内置告警规则种子。

**前端** — 新增三个前端文件和一个导航入口：
- `web/src/pages/Databases.tsx` (385 行) — 数据库列表页，支持搜索、筛选和状态总览
- `web/src/pages/DatabaseDetail.tsx` (937 行) — 数据库详情页，包含连接信息、运行指标、拓扑视图和慢查询可视化
- `web/src/api/databases.ts` (160 行) — 数据库 API 客户端
- `web/src/components/Sidebar.tsx` — 侧边栏新增"数据库"导航项

---

## 2. 🤖 AI 数据库分析工具

上游没有 `query_database`、`inspect_schema` 等数据库查询工具，AI Agent 无法直接与数据库交互。此 Fork 补全了三个核心 AI 工具，全部注册为 BaseTool：

- `query_database` — AI Agent 可对远端数据库执行只读 SQL 查询（SELECT / SHOW / EXPLAIN）
- `inspect_schema` — AI Agent 可查看数据库表结构、索引和视图
- `database_query_basetool` — 统一的 BaseTool 实现，封装了设备解析 → 凭证注入 → SQL 执行完整管线

**AI 分析增强：**

AI Agent 在分析数据库问题时，不再需要用户手动提供连接参数。Agent 收到 `database_id` 后，自动从加密凭证库解析连接参数并建立连接。首次连接成功后，凭证自动加密存储供后续使用。

每次 SQL 查询的结果会附带 AI 分析，帮助理解慢查询的根因。AgentTool 新增了去重机制——相同的 `(subagent_type, prompt)` 在 90 秒内返回缓存结果，防止 ReAct 循环。

**分析 Prompt 优化：**

- AI 分析 prompt 携带 `database_id` 和 `edge_id`，提供完整的上下文信息
- 修复了嵌套 Worker 中 `tool_call_id` 被父图覆盖的问题
- AI 分析页面跳转从 `/chat/new` 链接改为 `createSession + navigate` 方式

**数据库发现：**

Edge Agent 通过新增的 `MethodPushDBInstanceInfo` 隧道消息类型，自动发现并上报数据库实例元数据（版本、配置参数、连接状态），Manager 侧接收后存入 `database_instances` 表。

`internal/edgeagent/plugins/dbcli/discover.go` (283 行) 实现了 Edge 侧的数据库自动发现逻辑，扫描已知端口并进行协议检测。

---

## 3. 📡 数据库监控扩展

**MongoDB 支持：**

新增 MongoDB 有线协议版本检测，自动识别 MongoDB 版本。`databasemetrics` 导出器规格文件扩展了 44 行，支持 MongoDB 的连接数、操作吞吐量、内存使用和复制集等自定义指标。

**Redis/MongoDB 探测：**

新增 `host_probe_database` 技能 (`internal/skill/builtin/probe_database.go`，524 行)，支持 TCP 连接 + 协议握手 + 版本检测，覆盖四种主流数据库：
- MySQL (3306 端口)
- PostgreSQL (5432 端口)
- Redis (6379 端口)
- MongoDB (27017 端口)

**dbcli 层：**

`internal/edgeagent/plugins/dbcli/pool.go` (140 行) 实现了连接池管理，包含 MySQL driver 注册、连接复用和超时控制。`discover.go` (283 行) 实现了实例发现——扫描已知端口、协议检测和元数据上报。

**PromQL 和指标扩展：**

修复了 MySQL driver 注册问题，新增了数据库探测端点。`db_exec_query` 技能 (247 行) 支持在 Edge 侧执行只读 SQL（SELECT / SHOW / EXPLAIN / DESCRIBE / WITH）。

---

## 4. 🛠️ AI Agent 改进

**协调员工具修复：**

上游的协调员工具白名单缺少 `list_metric_catalog`——工具已注册在运行时工具包中，但协调员无法调用。此 Fork 将其添加到 `coordinatorToolNames`。同时，基础提示词中明确提及了 `apply_config_change` 工具名，而不仅仅是描述"确认 apply"这个概念。

**测试守卫：**

`cmd/ongrid/coordinator_tools_test.go` 新增了两个测试用例，防止回归：
- `TestCoordinatorRosterHasCodeTools` — 确保协调员工具白名单始终包含代码阅读工具（`list_repo_sources`、`read_source`、`grep_source`）和基线工具
- `TestBasePromptRequiresMetricCatalogBeforeAlertDraft` — 确保基础提示词包含完整的告警配置流程描述（`list_metric_catalog` → `draft_config_change` → `apply_config_change`）

**其他修复：**
- 错误信息中的双引号转义，防止 LLM JSON 解析失败
- `binary.PutUint32` 中 `-1` 字面量的类型安全修复，改用 `^uint32(0)`
- Go 依赖清理 (`go mod tidy`)

---

## 5. 🔌 Edge Agent 技能增强

**新技能：**

`host_probe_database` — 数据库连通性探测技能，支持协议版本检测，Class 为 safe（只读）

`db_exec_query` — Edge 侧只读 SQL 执行技能，支持 SELECT / SHOW / EXPLAIN / DESCRIBE / WITH，Class 为 safe（只读）

**Edge 配置：**

`cmd/ongrid-edge/main.go` 扩展了 29 行，注册了 `dbcli` 插件和 `pool.go` / `discover.go`。数据库指标导出器 (`databasemetrics`) 扩展为支持更多数据库类型和指标。

---

## 6. 🎨 UI 改进

**新页面：**
- `Databases.tsx` (385 行) — 数据库管理列表页
- `DatabaseDetail.tsx` (937 行) — 数据库详情页，含慢查询可视化、拓扑视图和连接信息

**修复：**
- 设备页面新增搜索功能 (`fix(ui): add search to device page`)
- 修复数据库实例类型筛选器功能 (`fix(ui): fix DB instance type filter`)
- 修复 UTF-16 编码导致 Edges.tsx 中文乱码的问题 (`fix(ui): restore Edges.tsx Chinese characters`)
- 对齐数据库 UI 与全局设计规范 — PageHeader、Card、Button、borders (`style: align database UI with app-wide patterns`)
- 修复 `usePoll` 布尔参数和 `Modal open` prop 的 TypeScript 类型错误 (`fix: resolve frontend TS errors`)

---

## 7. 🐛 Bug Fixes & Code Quality

**Code Review 第二轮修复（10 个问题）：**
- 恢复 `inspect_schema.go` 和 `query_database.go` 中被截断的函数体（+86 行 +63 行）
- 修复 `Databases.tsx` 中 `useState` 结束位置错误的 TypeScript 语法问题
- 补充丢失的 `strconv` import
- 移除 `topology/model.go` 中的多余字符 't'
- 移除 `dbcli` 中未使用的 `encoding/json` import
- 修正 `SetDBQueryExecutor` 初始化顺序（移到 `fbClient init` 之后）

**版本号更新：** v0.8.7 → v0.8.9 → v0.9.0

---

## 8. 📚 文档

`docs/AI-FUNCTIONAL-REQUIREMENTS.md` — 约 850 行的 AI 功能需求文档，包含 13 个 Mermaid 流程图，详细介绍了多智能体架构、协调员调度、工具生态系统、告警配置流程、事件调查、RAG 知识库、技能系统、LLM 基础设施和边缘智能体等子系统。

---

## Quick Stats

```
Upstream base: 73adefc (ongridio/ongrid v0.9.0)
Fork HEAD:     14362e3
Commits:       34 commits beyond upstream
New files:     20 (non-doc)
Changes:       +5750 / -158 (non-doc)

Languages affected:
  Go (backend):       ~3200 lines (17 files)
  TypeScript/TSX:     ~2000 lines (7 files)
  Markdown:           ~1000 lines (1 file)
  YAML:               ~10 lines  (1 file)
```
