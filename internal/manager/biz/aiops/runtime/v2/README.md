# Agent Runtime v2

来源：[Ongrid v0.18.2](https://github.com/ongridio/ongrid/releases/tag/v0.18.2)，提交 `ad9e52f670aa8a23976942954acbf103e1a6ef2d`。

提取该 release 的 `biz/aiops/chatruntime/` 与 `biz/aiops/graph/`，包括测试、testdata、prompt、Worker 和回调；不提取 legacy `agent/`。原文件清单与 Git blob 记录在 [source.json](source.json)。

适配：两个包的内部 import 改为本版本路径。无其他生产逻辑修改。

当前与主模块共享 Eino/LLM 依赖、领域模型、业务工具和存储接口，因此不是旧 release 的完整可执行镜像。此目录参与编译和 race 测试，但尚未接入生产路由或管理员切换。接入前必须验证工具装配、权限、审批及后台任务生命周期，不能用历史策略绕过当前安全约束。
