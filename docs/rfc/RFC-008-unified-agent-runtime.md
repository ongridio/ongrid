# RFC-008：统一 Agent runtime 与可执行工具目录

- 状态：代码与自动化验证持续收尾；草稿 PR，禁止自动合并/部署
- 日期：2026-10-14
- 关联：#499、#498、ADR-035
- 问题分析基线：v0.18.1 / a6bc1039；实现已同步至 a90d4b19（v0.18.2 之后）

## 背景与目标

Graph 默认运行路径与旧循环维护独立工具列表，安全 Skill 注册未进入 Graph；工作流目录和执行器也不一致。目标是保留 Graph/Eino 为唯一循环，以同一组可执行工具派生聊天、专家、搜索和工作流视图。HTTP、SSE、IM、会话存储兼容，不更换模型框架，不扩大权限。

## 方案

1. 抽取 chatprotocol 中的请求、回复、事件类型；旧循环迁移期间仅保留类型别名。
2. 在 BaseTool 工厂接入安全 Skill 适配器，复用 SkillRunner 的校验、设备路由、审计；过滤由 BaseTool 反向导出的展示 Skill，防止循环桥接和重复注册。
3. 用完整工具组装入口包含 host-files；运行时动态注册及 MCP 更新同步执行视图和搜索目录。每回合使用一致快照，执行时检查开关，子任务继承。工具来源被移除后不得通过旧引用发起新调用。
4. 保留只读、写开关、审批和审计控制；联网开关同时限制暴露与执行。工作流的明确发布授权与聊天交互审批不互相替代。
5. 退役旧循环与内核分支；旧环境变量仅告警，统一 Graph。初始化失败时聊天不可用，不隐式切换执行语义；通过修复配置并重启恢复，其他 Manager 功能不随之停止。

接口在消费方定义，不引入跨域依赖。模型接口不变；目录只管理已支持的可执行工具，不把 Markdown 技能元数据当工具。

## 能力基线

| 能力 | v0.18.1 | 迁移验收 |
| --- | --- | --- |
| web_search、四类 probe、netns、journal、tail | 旧注册表可用，Graph 缺失 | Graph 可发现、可执行；关联网硬拒绝 |
| 外部安全 subprocess Skill | 已加载但 Graph 不可见 | 复用 Skill 服务并接入工具目录 |
| host_find_large_files/du_summary/stat_file | 聊天及工作流执行器存在，画布缺失 | 目录和执行一致 |
| 专家、MCP、审批、页面、通知 | Graph 已接入 | 保持权限、动态更新、停止语义 |
| HTTP/SSE/IM 与历史会话 | 共享旧包类型 | 协议与存储不变，历史回放测试 |
| legacy 显式配置/初始化回退 | 仍存在 | 告警迁移，禁止静默回退 |

## 备选方案

- 仅修 web_search：范围小，但留下外部 Skill 和工作流同类缺口，不采用。
- 永久维护两个循环：保留切换，但继续产生工具与权限差异，不采用。
- 统一目录后移除旧循环：分阶段可验证，采用。

## 交付与验收

2026-10-14 开始；完成时间以验证结果为准，按 1–3 天粒度拆分：共享协议与设计、目录与适配、执行约束与生命周期、综合回归、旧循环清理。

- 注册基线覆盖真实内置 Skill 和外部测试 Skill，不只比较名称数量。
- 默认工具目录、工作流目录/执行、专家可用范围一致；有意排除项显式列出。
- 覆盖联网开关、动态更新、并发、只读权限、审批、取消、历史回放与 IM；Go 测试带 -race。
- 不依赖真实 LLM 的确定性回归为主；发布前在有凭据的环境做真实模型、MCP 与升级冒烟。
- 检查生产是否显式配置 legacy：本地源码不能证明线上使用情况。

## 风险、发布与回滚

主要风险为漏迁工具、重复桥接、动态目录陈旧、权限弱化、共享类型误删。通过工具执行测试和调用边界硬校验控制。

各阶段保持可编译可测试，使用独立提交审查；部署先金丝雀验证聊天与历史会话。无需数据库迁移，回滚恢复上一版 Manager 镜像及对应 Compose 配置，不使用永久双循环。线上盘点、真实环境冒烟未经执行不得宣称完成。

## 实施验证（2026-10-14）

- 已完成共享协议抽取、安全 Skill 与 host-files 目录接入、联网权限继承、动态工具旧引用撤销，以及旧循环和内核切换分支移除。
- `make test-agent-runtime`：在本地已有 Linux/amd64 构建镜像中通过，Go 1.25.12，启用 `-race`；覆盖 AIOps 全部子包、Service、HTTP、IM、Skill 和 Manager 装配。
- 新增确定性用例：真实 Graph 调用的联网开关、同步/后台 worker 权限继承、动态工具更新并发与旧引用拒绝、真实外部 subprocess Skill 执行、工作流文件工具目录与执行器一致性。
- `make build-ongrid`、`git diff --check` 通过。
- 首轮架构检查因工具缺失未完成；后续补装后暴露配置与依赖问题，现已修复，见下方收尾记录。
- 尚未执行生产 legacy 配置盘点、真实模型/MCP 集成冒烟、金丝雀升级与镜像回滚演练；这些是发布验收项，不能用单元测试替代。
- 旧 Registry 中供审批兼容及既有测试使用的闭包适配器暂留；生产聊天只调用 Graph，告警初查也已改用 BaseTool。退役旧循环不等于删除历史审批协议。

### 集中复核补充

- 修复告警初查的批量参数迁移：使用 `incident_ids`，展开对应结果，拒绝失败/错配/空 bundle；用真实 `CorrelateIncidentTool` 验证诊断链路。
- 修复安全 Skill schema 合并丢失原有 `required` 字段；覆盖全部内置 host-scope safe skills。
- 工具注册整批校验名称唯一性、来源前缀以及目录/执行列表一致性；失败保留原有工具与目录。ToolSearch 读取本回合对应执行版本的完整 schema，不混用动态刷新的新参数。
- 显式空工具视图清除继承的父回合权限列表。
- 上述新增用例及 `make test-agent-runtime` 的 Linux race 回归通过。`be50baba` 的 GitHub CI 已通过全仓 build/vet/race、部署清单及前端检查；后续架构修复提交仍须重新检查 CI。
- 本地全仓测试尝试未通过：只读模块缓存缺少 APM 示例依赖，且 Docker amd64 仿真环境的 autoapm 进程发现测试失败。不能把本地全仓测试记为通过；原生 Ubuntu CI 的同项已通过。
- 告警初查使用启动时的 BaseTool 快照，避免后台任务与后续 Registry 装配并发读写；Manager 装配包 `go test -race ./cmd/ongrid` 通过。


## 架构与验收收尾（2026-10-16）

- 修复 `.go-arch-lint.yml`：补齐共享 Skill、Edge 运行时和测试装配分类；删除不存在的 Edge data 目录；显式允许同层内部引用。Manager / Edge 命令分别装配，不能互相 import 对方业务域。
- 生产 import 边界保留：三个业务域相互隔离、共享包不依赖业务、Service/Biz 不依赖 Data。包内 `_test.go` 允许组装存储 fixture，集成测试归独立测试组件；第三方库版本/漏洞由依赖检查管理。
- `deepScan` 的实现类型流分析会将合法的消费方接口注入判为反向依赖，因此关闭该分析，使用 import 检查；不是给生产跨域依赖加白名单。新增 11 个正反向用例验证规则能拒绝越界，包括命令入口、共享包、业务层和服务层。
- 修复实际逆向依赖：成员查询 DTO 与审计过滤类型下移到模型层；跨域审计事件、枚举和请求槽移入 `internal/pkg/auditctx`；保留旧 Manager 类型别名/函数封装以兼容调用方。IM 的 AIOps Service 适配器移至 Service 层。HTTP、数据库表和审计事件值不变。
- `make arch-lint test-arch-policy` 通过（固定 `go-arch-lint v1.19.0`）；缺少工具时目标明确失败，不再打印虚假的 skipping。CI 安装固定版本并执行同样入口。
- IAM、审计、IM、Manager 装配及 `make test-agent-runtime` 的 Linux race 回归通过。新增跨域审计事件在包装后的请求中仍正确持久化的回归。

### 能力验收矩阵

| 能力/入口 | 自动化证据 | 真实环境剩余验收 |
| --- | --- | --- |
| 默认聊天、安全内置 Skill | `TestGraphCatalogIncludesAndExecutesSafeSkills`；Graph 调用路径 | 真实模型正确选择并调用工具 |
| 联网开关 | `TestRuntimeWebSearchToggleControlsActualDispatch`：关闭时零调用；直接旧引用也拒绝 | 配置搜索服务，开/关各一轮 |
| Host Skill 参数 | `TestHostSkillSchemaPreservesRequiredParameters`：保留原必填项并添加 device_id | 测试 Edge 执行 DNS/TCP/HTTP/ping、日志与文件只读能力 |
| 外部安全 Skill | `TestGraphCatalogAdaptsExternalSubprocessSkill`：真实 manifest + 子进程 | 安装一个测试包，确认审计结果；危险类不自动注册 |
| 工作流文件工具 | `TestWorkflowPaletteAndInvokerIncludeHostFiles` | 画布选取、发布、运行、结果核对；K8s 审批动作仍明确排除 |
| 专家/后台子任务 | `TestWorkerInheritsWebSearchGate`；`TestSpawnWorker_*` | 真实专家路由、前后台联网权限一致 |
| MCP 生命周期 | `TestRuntimeRemovedOrReplacedToolRejectsStaleReference`；`TestToolSearchKeepsTurnSchemaAcrossRefresh`；`TestInvalidToolSourceUpdatePreservesExistingViews` | 实际 MCP 增删/禁用/替换，验证当前与下轮行为 |
| 只读/写开关/审批 | `TestFilterToolsForAgentRole_ViewerDropsSensitiveReadTool`；HumanApprovalGate/ReviewGate 测试 | 测试账号拒绝写操作；沙箱动作批准/拒绝/超时 |
| 停止与历史消息 | `TestStopWorker_Idempotent`；`TestBuildEinoHistory_*` | 停止长调用、重启后继续旧会话；核对 SSE 终止语义 |
| HTTP/IM 协议 | HTTP happy-path / stable DTO 测试；IM adapter/sender 回归 | 测试 IM 消息流完整、只发送一次终态 |
| 告警初查 | `TestInvestigatorUsesRealCorrelationToolContract`；失败/错配 batch 测试 | 测试告警生成正确初查记录 |
| 初始化失败 | `TestService_MissingRuntimeFailsExplicitly` | 确认聊天明确不可用、不切旧循环、其他 API 健康 |
| 架构与审计 | `make arch-lint test-arch-policy`；`TestSharedAuditEventSurvivesWrappedRequest` | 确认登录失败与用户修改的审计内容连续 |

### 发布前仍需现场证据

1. 在指定测试环境记录 Manager 版本、模型/MCP 服务和测试账号；密钥留在环境配置，不写入报告。
2. 按矩阵执行真实模型、测试 Edge 和 IM 冒烟，记录会话/工具调用/审计 ID，清理测试数据。
3. 盘点部署中的 `ONGRID_AGENT_KERNEL=legacy`；确认已迁移到 Graph 及新版配置。
4. 在隔离部署演练旧镜像升级到候选镜像，再恢复旧镜像与其 Compose 配置，检查会话可读、审批可查询、健康检查恢复。无数据库迁移。
5. 仅在上述证据齐全后提交最终验收结论；当前不合并、不部署，不将模拟模型测试称为真实模型验收。

### 同步主分支后的复核（2026-10-16）

- 同步 `a90d4b19` 的 Manager/Edge 入口拆分与 Agent tracing：安全 Skill 在 Runtime/后台任务之前装配；MCP 的目录和执行器仍原子替换；没有恢复 legacy loop 或故障回退。
- 后台 Worker 仅复制联网/写权限、调用者身份快照和 trace ancestry，隔离请求取消、回调状态和可变请求槽；新增回归覆盖。
- 跟随主分支删除 examples 的变化，补充 Edge enrollment 与本地观测预览的精确架构分类；架构基线及 11 个边界用例通过。
- 原生 macOS 的相关包 race 通过；完整 `make test-agent-runtime` 在 Linux 构建容器通过。macOS 无法编译依赖 Linux cmdpolicy 的工具测试，不能替代 Linux 检查。
- E2E 复核发现原有测试仍要求关键词前置澄清、未处理抓包人工审批、假定动态日志工具不存在；按当前语义解析、审批和日志后端契约修正测试。附件目录改为测试临时目录并严格检查会话清理。
- 真实模型/MCP、线上配置盘点与升级回滚仍未执行，PR 保持草稿且禁止合并。

- 最终定向 E2E 通过（25.167s）：真实 Manager + 临时 MySQL，覆盖 `TestAssistantChat_*`、`TestMCP_CRUD`、`TestSkills_Registry`；模型及外部服务为测试替身，不能视为真实模型验收。

## 变更记录

- 2026-10-16：用户新增 v2/v3 显式切换与代码隔离需求，见 RFC-009 / PRD-009（提议）。原文“唯一实现、不保留双版本”的范围需随新方案调整；当前 PR 尚未交付版本选择。旧 legacy for-loop 仍退役。

### 审批退役修正（2026-10-16）

- 当前 Graph 使用的 shell/MCP/安装 Skill 执行器保留；删除 legacy K8s proposer、旧 Registry 注册/执行入口及专用批准后执行方法，Graph 的 dry-run/preflight/ReviewGate 路径不变。
- `K8sActionPayload` 仍供历史动作审计读取，已完成审批数据不删除。旧待审批动作在缺少执行器时明确落为 failed，要求重新提交。
- 更正此前分析：旧 K8s token 在创建审批前消费，旧 RunApproved 并不再次校验内存 grant；不能将其描述为重启必然失效。删除该执行路径是明确的退役政策。
- 取消/等待超时通过 pending 条件更新使审批失效，不覆盖已批准或已拒绝的结果；并发批准只执行一次。执行结果保存失败向上返回错误，不再静默成功。
- 本地审批/适配器/历史审计 race、完整 Agent Linux race、架构基线和 11 个边界 fixture 均通过。版本选择实现另见 RFC-009 提议，尚未完成。
