# RFC-011 · v3 聊天窗口与自动摘要

v3 每次调用模型前按 token 预算组装请求。一段聊天可以超过 50 条消息。消息、工具调用和证据仍保存在原会话，用户能在原位置查看。v1、v2 使用原有回放规则。

## 窗口如何确定

Manager 读取供应商模型目录中的总窗口、输入和输出限制。目录缺少容量时，官方端点可匹配版本化的型号规格。管理员可以在「设置 → LLM 模型 → 部署上下文上限」为具体模型填写实际部署容量。已知的更小限制优先。同名代理和自建模型需要自己的元数据或配置。

自动档默认使用 256,000 token。模型或部署上限更小时按该上限使用。用户可以在已知上限内手动选择窗口，包括大于 256K 的档位。查不到容量时，界面会标明使用默认值。聊天用量和自动摘要使用同一预算。在输入输出共享的模型上，先扣除输出预留，再受独立输入限额约束。计数余量最多 4,000 token。独立输入配额单独计算。输入文本和工具定义采用本地估算，图片按尺寸估算。供应商返回的输入用量单列显示。

Gemini 官方端点通过原生模型目录获取输入和输出上限。OpenAI 兼容端点读取目录提供的容量字段。OpenRouter 同时参考首选部署的上下文和输出限制。DeepSeek 官方端点支持通过当前型号查询旧版 Flash 别名的容量。参考 [Google 模型目录](https://aistudio.google.com/api-reference/models)、[OpenRouter 模型目录](https://openrouter.ai/docs/guides/overview/models)、[DeepSeek 型号别名](https://api-docs.deepseek.com/quick_start/pricing/)、[Kimi 窗口规格](https://platform.kimi.com/docs/pricing/chat) 和 [MiniMax M2.7 模型配置](https://huggingface.co/MiniMaxAI/MiniMax-M2.7/blob/main/config.json)。

## 压缩与恢复

1. 每一步将指令、已绑定工具、会话摘要、历史、当前问题和新工具结果一起计数。达到可用输入的 80% 时，先精简有可回读结果 ID 的旧工具预览，并保留采集和执行状态。
2. 若仍达阈值，选取完整旧交互，目标是把预计输入降至约 55%。使用同一模型、独立超时和禁用工具的请求生成增量摘要。摘要必须非空、缩短输入，且不得引入来源中不存在的 UUID。当前用户原文和仍在执行的调用保持原样。历史配置草案随所属对话归档，紧邻的上一轮草案仍可直接确认。
3. 历史摘要、首个保留消息的位置与版本一起提交。同一轮已完成的工具调用若有完整、可读的原件，可用调用参数、短摘录和结果 ID 替换原文，后续步骤复用。只有预计输入实际减少才采用这份记录。进程重启后从最近已提交的历史摘要继续。游标只改变模型回放。原始历史与审批数据继续由现有存储管理。
4. 摘要请求遇到可恢复的连接错误时，在本轮重试失败的片段一次。重试仍失败、摘要不完整或校验失败时，已持久化的历史可以改存精简摘录。摘录取自消息首、中、尾，中段优先保留修订与授权限制。模型可用 `read_archived_chat` 按关键词或消息 ID 回查本会话已归档的用户和助理原文。连续降级时为新摘录预留空间，旧消息仍可按关键词回查。历史工具输出通过 `read_tool_result` 读取。摘要独立保留首批和近期结果 ID，超出内联范围或旧摘要漏记的 ID 可用 `read_archived_chat(list_tool_results=true)` 按工具名分页找回。回查本轮较早的工具结果时加上 `current_turn=true`，再把返回的 ID 交给 `read_tool_result` 读取原件。
5. 界面显示最近一次成功保存的是摘要还是摘录，以及保存时间和压缩前后的预计输入。若无法安全保存，本轮整理延期，继续使用上次有效的视图。固定指令和最新输入若已超出硬预算，返回占用明细。供应商明确在生成前拒绝上下文长度时，本次模型请求最多再尝试一次缩小后的输入。

## 接口与兼容

模型目录返回每个型号的容量来源、建议值和允许的档位。创建会话或更新模型时传 `context_window_tokens`，0 表示自动。主聊天和侧栏都接收 `context_usage` SSE，通过其中的状态区分正在压缩、已完成、推迟和超限。刷新可通过有会话归属检查的 `GET /v1/chat/sessions/{id}/context` 恢复最近快照。

GORM 在 `chat_sessions` 增加可空摘要与快照、窗口偏好、运行归属和修订号。旧会话无需回填。首次整理超长历史需扫描并归纳旧消息，耗时取决于历史长度及模型。SQLite 按创建时间和消息 ID 做稳定分页。MySQL 使用相同的游标，由部署方按现有升级流程评估 DDL 锁表时间。降级的 Manager 会忽略新增列。

型号规格可核对 [OpenAI](https://developers.openai.com/api/docs/models/gpt-6.2-astra)、[Claude](https://platform.claude.com/docs/en/models/overview)、[DeepSeek](https://api-docs.deepseek.com/api/list-models/)、[Gemini](https://ai.google.dev/gemini-api/docs/models/gemini-2.5-pro)、[小米 MiMo](https://mimo.mi.com/docs/en-US/tokenplan/integration/opencode) 和 [智谱 GLM](https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash)。
