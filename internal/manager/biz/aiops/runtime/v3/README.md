# Agent Runtime v3

从草稿 PR #503 的 `cc25f29f` 迁移当前 `chatruntime/`、`graph/` 及其测试到本目录。只调整 import 与测试夹具路径，保留 tracing scope 名称和现有行为。

当前生产装配调用本版本。后续策略变更在本目录实施，不修改 v1/v2 的历史行为基线。业务工具、领域模型、存储和外部 agents/skills 资源仍然共享；跨版本路由与版本化装配尚待实施。
