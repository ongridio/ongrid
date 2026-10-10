# PRD-009：Chat Token Streaming

## 背景

当前 Chat 页面已经使用 `POST /api/v1/chat/sessions/{id}/messages/stream` 建立 SSE 连接，但模型调用链路仍是阻塞式：

1. `llm.Client` 只暴露 `Chat()`，底层调用非流式 chat completion。
2. `clientChatModel.Stream()` 先执行完整 `Generate()`，再把最终结果包装成单 chunk stream。
3. coordinator graph 使用 `g.Invoke()`，不会触发模型的 `Stream()`。
4. graph callback 已定义 `assistant_delta`，但 runtime 适配层明确丢弃该事件。
5. 前端只处理最终 `assistant`、工具事件和 `done`，没有增量渲染。

结果是：工具卡片可以流式出现，但一段没有工具调用的模型回答会在完整生成后一次性展示。

## 目标

- 主会话 SSE 请求支持模型回答的 token/chunk 级流式输出。
- 前端先渲染 pending assistant 气泡，持续追加 `assistant_delta`，最终用持久化后的完整 `assistant` 帧修正。
- 流式模式下继续正确聚合 tool call 分片，保持工具调用、审批、审计和持久化行为。
- 继续记录 token usage、预算和 Prometheus 指标。
- 保留非流式调用路径，后台 worker、blocking API 和 legacy kernel 不受影响。
- 提供 `ONGRID_CHAT_TOKEN_STREAM` 功能开关，可随时回滚。

## 非目标

- 不改变 blocking `POST /messages` 的 JSON 响应格式。
- 不改变 `.proto` 或数据库 schema。
- 不在第一版把 specialist worker 的最终输出逐 token 转发到父会话。

## 事件协议

SSE 新增向后兼容事件：

```text
event: assistant_delta
data: {"session_id":"...","iteration":1,"kind":"reasoning|content","content":"增量文本"}
```

事件顺序：

```text
: ok
event: assistant_delta        # 0..n
event: assistant              # 每轮持久化后的完整 assistant 消息
event: tool_start
event: tool_end
event: assistant_delta        # 下一轮模型输出
event: assistant
event: done
```

前端使用 `iteration` 区分不同模型轮次，使用 `kind=reasoning|content` 区分思考段和回答段。最终 `assistant` 帧携带真实 `message_id` 和完整 `content`，用于替换本地 delta 拼接结果。老前端会忽略未知 `assistant_delta` 事件，不会破坏兼容性。

## 技术方案

### 1. LLM 流式客户端

在 `internal/pkg/llm` 增加可选 `StreamingClient` 能力接口，不强制所有 `Client` 实现都支持流式。`openaiClient` 复用现有 resolver、预算、指标和请求转换逻辑，通过 OpenAI-compatible `CreateChatCompletionStream` 读取增量：

- `delta.Content` 立即向下游输出；
- `delta.ReasoningContent` 作为 `kind=reasoning` 增量发送，前端折叠展示并保留完整思考过程；
- `delta.ToolCalls` 按 `index` / `id` 聚合 ID、函数名和参数片段；
- 流结束时输出 usage 与聚合后的完整消息；
- 上游不支持 usage chunk 时使用估算值，不让成功回答失败；
- `Close()` 幂等，取消和 EOF 都会释放上游连接。

`MultiClient`、timeout wrapper 和 provider 注入 wrapper 同步实现流式路由。timeout wrapper 的 cancel 必须绑定返回 reader 的生命周期，不能在函数返回时立即 cancel。

### 2. Eino ChatModel 适配

`clientChatModel.Stream()` 检测底层 `StreamingClient`：

- 支持时把 `ChatStream()` 转换为 Eino `schema.StreamReader[*schema.Message]`；
- 不支持时保留现有单 chunk 降级行为。

文本 chunk 直接向下游输出；完整 tool calls 在聚合完成后输出，避免 Eino 把分片误认为多个独立调用。

### 3. Graph 与 runtime

`BuildReActGraph` 配置自定义 `StreamToolCallChecker`，完整读取模型输出后再判断是否存在 tool call，避免默认“只看第一个 chunk”的策略漏掉先文本后工具调用的模型。

`chatruntime.Runtime` 增加 `TokenStreaming` 配置，由 `ONGRID_CHAT_TOKEN_STREAM` 注入：

- SSE 请求且开关开启时使用 `g.Stream()`；
- blocking 请求、后台 worker 和开关关闭时继续 `g.Invoke()`；
- graph stream 建立失败时降级回 `g.Invoke()` 并记录日志。

runtime 消费 graph 输出 stream 到 EOF，保证 callback、持久化和工具调用完成后再返回最终 Reply。

Eino ReAct 的 `StreamToolCallChecker` 必须完整读取模型输出后才能决定 tool-call 分支。为保证不被该分支消费时序阻塞，SSE 请求在 ChatModel 外增加 direct stream tap：每读到 `reasoning_content` 或 `content` chunk，先立即发出 `assistant_delta`，独立 copy 再交给 ReAct 图。callback 中的重复 delta 在 token streaming 开启时被抑制。

### 4. Callback、持久化与指标

stream 模式下 Eino 会给每个 handler 分发独立的 stream copy：

- SSE handler 保留 stream drain 以覆盖非直通路径；
- persistence handler 聚合自己的 stream copy，在 EOF 后沿用现有 assistant 持久化逻辑；
- budget / metrics 从最终 usage 或估算 usage 记录；
- audit / alert guard 保持现有守卫行为。

`assistant_end` 的 `message_id` 不能依赖 handler 注册顺序。persistence 完成后通过 relay 的完成信号通知 SSE，再发送最终 assistant 帧，避免 delta handler 先 EOF 导致 message ID 缺失。

### 5. HTTP 并发安全

delta drain 与工具 callback 可能并发调用 SSE emitter。HTTP 层的 emit 使用互斥锁序列化 `writeSSE`，避免多个事件帧交叉写入。

### 6. 前端渲染

`web/src/api/chat.ts` 新增 `AssistantDeltaStreamEvent`、`onAssistantDelta` 和 `assistant_delta` 分发。

`ChatThread` 使用 `assistant-delta-{sessionID}-{iteration}` 作为临时消息 ID：

- 第一个 delta 创建 pending assistant 气泡；
- `kind=reasoning` 追加到思考段，`kind=content` 追加到回答段；
- 最终 `assistant` 帧用真实 `message_id` 替换临时气泡，并用完整内容覆盖；
- 思考过程在最终帧替换后保留，流式期间默认展开，完成后自动折叠且可再次展开；
- 历史消息以 `reasoning_content` 返回时，前端将其归一化到思考段，刷新后仍可查看；
- 错误时保留已展示内容并标记失败；
- 切换会话后不再更新旧会话；
- tool-only 轮次不创建空文本气泡。

## 配置与回滚

```text
ONGRID_CHAT_TOKEN_STREAM=false
```

默认关闭。设置为 `true` 后仅影响 graph kernel 的 SSE 主会话请求。

回滚方案：

1. 设置 `ONGRID_CHAT_TOKEN_STREAM=false`；
2. 重启 Manager；
3. 系统回到 `g.Invoke()` 和整段 assistant 输出。

流式建立失败时单次请求自动降级为 `g.Invoke()`，不需要重启。

## 验收标准

### 后端

- 纯文本回答按多个 delta 输出，最终仅持久化一条 assistant 消息。
- 文本后接 tool call 时，工具调用参数分片能聚合为完整调用。
- 多 tool call、工具失败、多轮模型输出行为正确。
- 流式中途取消不会泄漏上游连接。
- SSE 断开后已有持久化语义不被破坏。
- usage、预算和 Prometheus 指标仍被记录。
- 相关测试通过 `go test -race`。

### 前端

- delta 逐段渲染，无整段等待。
- 最终 `assistant` 帧不会导致重复气泡或重复文本。
- tool card 与 assistant delta 可以交错展示。
- 错误时保留已输出内容。
- 刷新页面后能看到持久化的完整回答。
- 思考过程在回答完成后仍可展开查看，不会随最终帧替换而消失。
- light / dark 主题、中英文文案、停止操作和会话切换通过验证。

## 测试范围

```text
internal/pkg/llm
internal/manager/biz/aiops/graph
internal/manager/biz/aiops/graph/callbacks
internal/manager/biz/aiops/chatruntime
internal/manager/service/aiops
internal/manager/server/aiops
web/src/api/chat.test.ts
web/src/lib/chatStreamMessages.test.ts
web/src/components/MessageBubble.test.tsx
```

必须覆盖纯文本流、tool call 分片、多轮输出、错误、取消、SSE 并发、持久化、usage 与前端去重。

## 实施记录（2026-10-06）

- 新增 `internal/pkg/llm/stream.go`：OpenAI-compatible token 流、tool call 分片聚合、usage、指标、路由和 timeout 生命周期。
- `clientChatModel.Stream()` 真实透传 provider stream；不支持流式的底层 client 自动回退单 chunk。
- ReAct graph 使用完整 `StreamToolCallChecker`；coordinator SSE 请求在开关开启时走 `g.Stream()`，blocking / worker 保持 `g.Invoke()`。
- Persistence / SSE / budget / metrics callback 均消费各自的 stream copy；最终 assistant 持久化后再发 `assistant_end`。
- HTTP SSE writer 加互斥锁，前端新增 `assistant_delta` 解析和 pending 气泡增量渲染。
- 增加 ChatModel direct stream tap，`reasoning_content` 与 `content` 均在 ReAct 分支消费前直发 SSE，避免 tool-call checker drain 导致最终一次性输出。
- SSE 写入具备请求生命周期保护：HTTP 请求结束后丢弃后续事件，`writeSSE` 防 ResponseWriter 关闭 panic，runtime 返回前等待异步 delta drain 收尾，避免后台 Agent 任务击穿进程。
- 前端最终帧合并保留流式 reasoning；思考过程完成后默认折叠但保持可见，支持再次展开，并兼容历史接口的 `reasoning_content` 字段。

验证记录：

```text
go test ./internal/pkg/llm ./internal/manager/biz/aiops/graph ./internal/manager/biz/aiops/graph/callbacks ./internal/manager/biz/aiops/chatruntime ./internal/manager/service/aiops ./internal/manager/server/aiops
go test -race ./internal/pkg/llm ./internal/manager/biz/aiops/graph ./internal/manager/biz/aiops/graph/callbacks ./internal/manager/biz/aiops/chatruntime ./internal/manager/service/aiops ./internal/manager/server/aiops
npm run typecheck --prefix web
npm test --prefix web -- --run src/api/chat.test.ts
npm run build --prefix web
```

真实浏览器使用 mocked SSE 验证 delta 后最终帧替换无重复，截图：

```text
.cache/design-review/chat-stream-light.png
.cache/design-review/chat-stream-dark.png
```

全量 `npm run lint` 仍有仓库既有 9 个 error；本次 touched 前端文件定向 ESLint 通过（仅 `ChatThread.tsx` 保留既有 Hook warning）。
