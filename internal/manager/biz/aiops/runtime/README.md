# Agent Runtime 版本源码

| 目录 | 基线 | 当前接线 |
| --- | --- | --- |
| [v1](v1/README.md) | GitHub 最早正式 release `v0.7.158` 的 Eino Graph | 编译、测试；未启用 |
| [v2](v2/README.md) | `v0.18.2` 的 Eino Graph | 编译、测试；未启用 |
| [v3](v3/README.md) | PR #503 当前统一运行时 | 所有现有调用方使用 |

编号是本次引入的运行时版本，不是 release 当年的命名。legacy 手写循环是并存的历史旁支，不属于这里的 v1。

每个版本独立持有 `chatruntime`（上下文、prompt、Worker、目录策略）和 `graph`（Eino 编排、工具适配、回调）。禁止版本间 import，`isolation_test.go` 在标准 Go 测试中检查该边界，并验证三版本指标注册共存。业务执行能力与基础设施继续共享；尚未声称三版本已可切换。

验证：Linux 环境执行 `make test-agent-runtime`。不新增 schema/API/设置，不改变当前运行时选择；需要撤销本次隔离时回退该源码迁移提交即可。版本路由及管理设置的后续方案见 [RFC-009](../../../../../docs/rfc/RFC-009-agent-runtime-versions.md)。
