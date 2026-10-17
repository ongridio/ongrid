// 仅识别工具结果的顶层状态，不递归扫描查询返回的业务数据。
type ToolStatus = 'pending' | 'success' | 'error' | 'timeout' | 'unknown';

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown> : undefined;
}

export function resolveToolStatus(call: { status?: string; error?: string; result?: unknown }): ToolStatus {
  if (call.status === 'timeout') return 'timeout';
  if (call.status === 'error' || call.error) return 'error';
  if (call.status === 'pending') return 'pending';

  const result = record(call.result);
  if (result) {
    if (result.status === 'timeout') return 'timeout';
    if (result.ok === false || result.isError === true ||
        result.status === 'error' || result.status === 'failed' ||
        (typeof result.error === 'string' && result.error.trim() !== '') ||
        record(result.error) ||
        (typeof result.exit_code === 'number' && result.exit_code !== 0) ||
        (typeof result.exit_status === 'number' && result.exit_status !== 0)) return 'error';
    if (result.ok === true || result.status === 'success' || result.status === 'succeeded' ||
        result.exit_code === 0 || result.exit_status === 0) return 'success';
  }
  return call.status === 'success' ? 'success' : 'unknown';
}
