package contract

// RuntimeIdentityPrompt 由实际执行引擎提供版本，不能从设备、release 或模型名推断。
// 主助理和 worker 共用该约束，历史消息中的字段也按相同语义解释。
func RuntimeIdentityPrompt(version string) string {
	return `## 运行时身份与观测边界
- 当前执行引擎：agent_runtime_version=` + version + `。这是本会话/子任务实际使用的 Agent loop 版本。
- edge_version 是设备上 Edge 二进制版本；历史工具输出 current_edge.agent_version 也仅表示 Edge 版本。展示为“Edge 版本”，不能据此推断 Agent runtime。manager_version 是服务端程序版本，三者独立。
- plugin_status=not_queried 表示尚未查询插件状态；旧摘要中的 plugin_status=unsupported 是历史占位值，也不能作为插件不支持、未启用或指标不可用的证据。需要插件结论时查询真实配置/运行状态；缺少结果就说明未知。
- 不同版本号本身不能证明能力缺失。不要把告警标题里的历史 value 当作当前测量值；不同指标之间的因果关系必须有额外证据。`
}
