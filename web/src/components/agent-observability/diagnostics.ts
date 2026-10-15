const labels: Record<string, [string, string]> = {
  traces: ['链路已收到', 'Traces received'],
  sampling: ['上游采样比例未知', 'Upstream sampling is unknown'],
  incomplete_identity: ['应用身份不完整', 'Incomplete application identity'],
  missing_parent: ['父步骤缺失', 'Missing parent step'],
  parent_cycle: ['父子关系存在循环', 'Cyclic parent relationship'],
  dropped_telemetry: [
    'SDK 丢弃了部分字段或事件',
    'SDK dropped attributes or events',
  ],
  duplicate_span_id: ['重复的 Span ID', 'Duplicate span IDs'],
  invalid_span_identity: ['步骤标识无效', 'Invalid span identity'],
  content_not_recorded: ['未获得输入输出', 'Input and output unavailable'],
  usage_incomplete: ['Token 用量不完整', 'Incomplete token usage'],
  usage_conflict: ['用量数据冲突', 'Conflicting usage values'],
  usage_overlap: ['嵌套步骤用量重叠', 'Overlapping nested usage'],
  session_not_recorded: ['未获得会话标识', 'Session identifier unavailable'],
  session_conflict: ['会话标识冲突', 'Conflicting session identifiers'],
  trace_read_failed: ['部分链路读取失败', 'Some traces could not be read'],
  trace_too_large: ['链路超出查询容量', 'Trace exceeds query capacity'],
  candidate_limit: ['已达到候选链路上限', 'Candidate trace limit reached'],
  result_limit: ['结果达到容量上限', 'Result capacity reached'],
  upstream_partial: ['存储查询未完成', 'Storage query incomplete'],
  invalid_start_time: ['开始时间无效', 'Invalid start time'],
  invalid_end_time: ['结束时间未获得或无效', 'End time missing or invalid'],
  invalid_parent_id: ['父步骤标识无效', 'Invalid parent span ID'],
};
export function issueLabel(
  key: string,
  tr: (zh: string, en: string) => string,
) {
  return labels[key] ? tr(...labels[key]) : key;
}
