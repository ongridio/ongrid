# Agent Runtime 版本源码

| 目录 | 基线 | 当前接线 |
| --- | --- | --- |
| [v1](v1/README.md) | GitHub 最早正式 release `v0.7.158` 的 Eino Graph | 源码归档；不再支持 |
| [v2](v2/README.md) | `v0.18.2` 的 Eino Graph | 可选回退版本；独立装配 |
| [v3](v3/README.md) | PR #503 当前统一运行时 | 当前 release 默认 |

编号是本次引入的运行时版本，不是 release 当年的命名。legacy 手写循环是并存的历史旁支，不属于这里的 v1。

每个版本独立持有 `chatruntime`（上下文、prompt、Worker、目录策略）和 `graph`（Eino 编排、工具适配、回调）。禁止版本间 import，`isolation_test.go` 在标准 Go 测试中检查该边界，并验证三版本指标注册共存。业务执行能力与基础设施继续共享；仅 v2/v3 可切换，v1 不再支持。

验证：Linux 环境执行 `make test-agent-runtime`，真实 Manager + MySQL 的 `TestRuntimeVersions_*` 覆盖切换、动态 MCP、审批、Skill 与 persona 热加载。管理员在「设置 → 助理」第二张卡片选择；新会话采用所选版本，既有会话/子任务/待审批动作不迁移。历史空版本固定使用 v3，不随设置改变。

回到 `release` 只影响新会话，不能据此卸载仍承载既有会话的 v2 引擎。旧镜像回滚限制和实现记录见 [RFC-009](../../../../../docs/rfc/RFC-009-agent-runtime-versions.md)。
