# DeepSeek 思考模式工具调用修复

对应问题：ongridio/ongrid#400。

工具调用续接以及后续用户轮次需要回传提供方返回的 reasoning_content。本修复在 SDK、内部消息、Eino 和持久化历史之间保留原值；消息历史接口以 `reasoning_content` 返回该字段，供前端折叠展示，不回写到用户可见回答正文。

## 升级与回退

MySQL 生产环境先备份，再执行 db/migrations/20260910120000_add_chat_reasoning_content.up.sql，最后更新应用。脚本使用 MySQL 8.0.12+ 的 INSTANT 新增列；不支持时应由维护者安排在线 DDL，不自动降级为阻塞操作。若实例已通过 AutoMigrate 或手工方式增加该列，先核对列为 nullable LONGTEXT，避免重复执行 ADD COLUMN。

普通应用回滚保留新增列，以免丢失历史数据。配套 down.sql 仅用于所有实例已回退、备份完成且确认不再需要该列时的显式结构回退；它会删除思考历史，不能用于正常滚动回滚。

旧消息的原始思考内容不可恢复。兼容逻辑仅在模型名称包含 deepseek 且请求携带工具时，为缺失值补显式空字符串；新消息的原值完整保留。自定义模型别名不含 deepseek 时不会启用空字段兼容。该兼容行为通过实际提供方验证，但不代表所有网关均接受空字段。

## 验证

受影响包应在启用 CGO 的 Linux 环境运行：

```sh
go test -race ./internal/pkg/llm ./internal/manager/biz/aiops/graph/callbacks ./internal/manager/biz/aiops/chatruntime ./internal/manager/data/aiops/store ./internal/manager/biz/aiops/agent
```

测试覆盖普通和 Stream 适配路径、多轮工具续接、旧历史、持久化长文本及 NULL、JSON 隐藏字段、请求体释放及错误传播。Stream 测试针对项目当前的流式适配层，不声称验证提供方原生 SSE 分片。

协议模糊测试：

```sh
go test ./internal/pkg/llm -run '^$' -fuzz '^FuzzReasoningTransport_' -fuzztime=10s
```

真实提供方测试默认跳过，显式设置 ONGRID_REASONING_SMOKE=1 及 SMOKE_API_KEY、SMOKE_BASE_URL、SMOKE_MODEL 才启用；仅使用合成工具。凭据只通过环境变量传入，不写入仓库。迁移 up/down 应在一次性 MySQL 数据库演练；SQLite 持久化测试不替代 MySQL DDL 验证。
