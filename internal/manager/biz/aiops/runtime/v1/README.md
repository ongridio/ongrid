# Agent Runtime v1

来源：[Ongrid v0.7.158](https://github.com/ongridio/ongrid/releases/tag/v0.7.158)，提交 `04b2796319785df840d7a472569c380e0871730b`。

提取该 release 的 `biz/aiops/chatruntime/` 与 `biz/aiops/graph/`，包括测试、testdata、prompt、Worker 和回调；不提取 legacy `agent/`。原文件清单与 Git blob 记录在 [source.json](source.json)。

适配：两个包的内部 import 改为本版本路径。v1 的两个仓储测试替身补充当前接口要求的 `UpdateSessionModel`，返回未接线错误，生产逻辑未修改。

**不再支持执行或切换。** 源码保留归档并参与编译、race 测试，便于历史追溯；不注册运行时和审批执行器。

当前与主模块共享 Eino/LLM 依赖、领域模型、业务工具和存储接口，因此不是旧 release 的完整可执行镜像。
