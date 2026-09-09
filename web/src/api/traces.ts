import { request } from './client';

// Tempo /api/search response, manager-proxied. `traces` is left as the
// raw Tempo trace summary array — Tempo evolves the per-trace shape
// across versions, so we type the load-bearing fields and keep the
// rest as `unknown` so older/newer Tempo versions still render.
export type TempoTraceSummary = {
  traceID: string;
  rootServiceName?: string;
  rootTraceName?: string;
  // duration in milliseconds — Tempo returns it as `durationMs` on the
  // search response. Older versions used `traceDurationMs`; we accept
  // either at the page level.
  durationMs?: number;
  traceDurationMs?: number;
  // start time as RFC3339 (newer Tempo) or unix-nanoseconds-as-string
  // (older). The page tolerates both.
  startTimeUnixNano?: string;
  startTime?: string;
  spanCount?: number;
  // A few Tempo versions surface a small spans preview in the summary.
  // We don't render it but keep it untyped so JSON.stringify diagnostics
  // work without a re-decode.
  [k: string]: unknown;
};

export type TraceSearchResponse = {
  traces: TempoTraceSummary[] | null;
  metrics?: unknown;
  from: string;
  to: string;
};

// OTLP-shaped trace body, manager-proxied verbatim from Tempo's
// /api/traces/<id>. Tempo wraps spans in
// {"batches":[{"resource":{...},"scopeSpans":[{"spans":[...]}]}]} —
// older versions used `instrumentationLibrarySpans` instead of
// `scopeSpans`. The page walks both shapes.
export type OtlpAnyValue = {
  stringValue?: string;
  intValue?: string | number;
  doubleValue?: number;
  boolValue?: boolean;
  bytesValue?: string;
  arrayValue?: { values?: OtlpAnyValue[] };
  kvlistValue?: { values?: OtlpAttribute[] };
};

export type OtlpAttribute = {
  key: string;
  value?: OtlpAnyValue;
};

export type OtlpSpanEvent = {
  timeUnixNano?: string;
  name: string;
  attributes?: OtlpAttribute[];
};

export type OtlpSpan = {
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
  name: string;
  kind?: number | string;
  startTimeUnixNano?: string;
  endTimeUnixNano?: string;
  status?: { code?: number | string; message?: string };
  attributes?: OtlpAttribute[];
  events?: OtlpSpanEvent[];
};

export type OtlpScopeSpans = {
  scope?: { name?: string; version?: string };
  spans?: OtlpSpan[];
};

export type OtlpResourceSpans = {
  resource?: { attributes?: OtlpAttribute[] };
  scopeSpans?: OtlpScopeSpans[];
  // Older Tempo / OTLP 0.x payloads.
  instrumentationLibrarySpans?: OtlpScopeSpans[];
};

export type TraceGetResponse = {
  // Tempo's response shape varies; both `batches` and `resourceSpans`
  // appear in the wild. The page accepts either.
  batches?: OtlpResourceSpans[];
  resourceSpans?: OtlpResourceSpans[];
  [k: string]: unknown;
};

export function searchTraces(params: {
  q?: string;
  service?: string;
  operation?: string;
  start: string; // RFC3339 or unix-seconds string
  end: string;
  limit?: number;
  minDuration?: string;
  maxDuration?: string;
}, signal?: AbortSignal) {
  const qs = new URLSearchParams();
  if (params.q) qs.set('q', params.q);
  if (params.service) qs.set('service', params.service);
  if (params.operation) qs.set('operation', params.operation);
  qs.set('start', params.start);
  qs.set('end', params.end);
  if (params.limit) qs.set('limit', String(params.limit));
  if (params.minDuration) qs.set('minDuration', params.minDuration);
  if (params.maxDuration) qs.set('maxDuration', params.maxDuration);
  return request<TraceSearchResponse>('GET', `/traces/search?${qs.toString()}`, undefined, { signal });
}

export function getTrace(traceId: string) {
  return request<TraceGetResponse>('GET', `/traces/${encodeURIComponent(traceId)}`);
}

export function listTraceTagValues(tag: string) {
  return request<{ values: string[] | null }>(
    'GET',
    `/traces/tags/${encodeURIComponent(tag)}/values`,
  );
}
