# Agent Runtime v2

来源：[Ongrid v0.18.2](https://github.com/ongridio/ongrid/releases/tag/v0.18.2)，提交 `ad9e52f670aa8a23976942954acbf103e1a6ef2d`。

提取该 release 的 `biz/aiops/chatruntime/` 与 `biz/aiops/graph/`，包括测试、testdata、prompt、Worker 和回调；不提取 legacy `agent/`。原文件清单与 Git blob 记录在 [source.json](source.json)。

v2 已接入管理员运行时选择，执行本目录的 Eino Graph、上下文策略和 Worker；不转发到 v3。`cmd/ongrid/runtime_builder_v2.go` 独立装配注册器、工具目录、装饰器和回调。来源清单记录的是提取时的原始 Git blob，后续兼容修改不改写来源。

为接入当前系统而回补的兼容与安全能力：

- 请求、回复、事件使用共享 `runtime/contract`；Worker 保持版本内私有锁和取消句柄，通过 Service 适配快照。
- 校验会话/请求/父任务版本，子任务持久化 v2；后台任务保留身份、版本及读写/联网权限，不继承请求取消和 callback。
- 工具搜索与执行目录同步更新；撤销/替换后拒绝旧执行句柄；联网开关在实际执行入口检查。
- 采用当前共享审批、业务工具和操作超时。审批按来源版本执行；MCP 批准前校验配置指纹。
- marketplace 热加载与 MCP 发现同时更新两套独立目录；不共享可变工具袋。

Skill 继续采用 v2 的自动加载策略（`always`/`keyword`）；没有 v3 的 `load_skill` 和资源快照机制。原解析器对未知模式按自动激活处理，因此 `on_demand` 在 v2 不具有按需语义。切换会改变这一行为，但不会把 v2 选项接到 v3。

当前仍共享 Eino/LLM 依赖、领域模型、底层业务工具和存储接口，因此不是旧 release 的完整镜像或逐字节行为复现。v2 仅接受必要兼容、安全修复；新增编排行为继续在 v3 开发。
