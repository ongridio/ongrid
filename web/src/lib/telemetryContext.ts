import type { LogFieldFilter, LogRecord } from '@/api/logs';

export function absoluteWindow(params: URLSearchParams) {
  const start = params.get('start') || '';
  const end = params.get('end') || '';
  const a = Date.parse(start);
  const b = Date.parse(end);
  return Number.isFinite(a) && Number.isFinite(b) && b > a && b - a <= 7 * 86400000
    ? { start: new Date(a).toISOString(), end: new Date(b).toISOString() }
    : null;
}
export function correlationFilters(params: URLSearchParams): LogFieldFilter[] {
  return ['trace_id', 'span_id', 'service_name', 'environment', 'service_namespace', 'service_version', 'instance_id']
    .filter((key) => params.has(key) && (!['service_version', 'instance_id'].includes(key) || !!params.get(key)))
    .map((field) => ({ field, operator: 'eq', values: [params.get(field)!] }));
}
export function logTraceLink(record: LogRecord, params: URLSearchParams) {
  if (!record.trace_id || !/^(?:[a-fA-F0-9]{16}|[a-fA-F0-9]{32})$/.test(record.trace_id)) return null;
  const next = new URLSearchParams(params);
  if (!absoluteWindow(next)) {
    const ts = Date.parse(record.timestamp);
    if (!Number.isFinite(ts)) return null;
    next.set('start', new Date(ts - 300000).toISOString());
    next.set('end', new Date(ts + 300000).toISOString());
  }
  next.delete('q');
  next.delete('trace_id');
  next.delete('span_id');
  return `/traces/${record.trace_id}?${next}`;
}

export function localDateTime(value: string) {
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19)
    : '';
}

// Tempo search omits leading zeroes, while OTLP logs use 32 hex characters.
export function canonicalTraceID(value: string) {
  return /^[a-fA-F0-9]{1,32}$/.test(value) && /[1-9a-fA-F]/.test(value)
    ? value.toLowerCase().padStart(32, '0')
    : '';
}
