import type { TempoTraceSummary } from '@/api/traces';

type SearchSpan = { durationNanos?: string | number };
type SearchSpanSet = { spans?: SearchSpan[] };

export function traceSummaryStartMs(summary: TempoTraceSummary): number {
  const value = summary.startTimeUnixNano
    ? Number(summary.startTimeUnixNano) / 1_000_000
    : Date.parse(summary.startTime || '');
  return Number.isFinite(value) ? value : 0;
}

export function traceSummaryDurationMs(summary: TempoTraceSummary): number {
  for (const value of [summary.durationMs, summary.traceDurationMs]) {
    if (typeof value === 'number' && Number.isFinite(value) && value > 0) return value;
  }

  const spanSets: unknown[] = [summary.spanSet];
  if (Array.isArray(summary.spanSets)) spanSets.push(...summary.spanSets);

  let durationNanos = 0;
  for (const candidate of spanSets) {
    if (!candidate || typeof candidate !== 'object') continue;
    const spans = (candidate as SearchSpanSet).spans;
    if (!Array.isArray(spans)) continue;
    for (const span of spans) {
      const value = Number(span.durationNanos);
      if (Number.isFinite(value) && value > durationNanos) durationNanos = value;
    }
  }
  return durationNanos / 1_000_000;
}

export function formatTraceSummaryDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '-';
  if (ms < 0.001) return '<0.001ms';
  if (ms < 1) return `${Number(ms.toFixed(3))}ms`;
  if (ms < 1000) return `${ms.toFixed(1)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

export type TraceQuickFilter = '' | 'span:status = error' | 'trace:duration > 1s';

export type TraceScope = 'business' | 'internal' | 'all';

const INTERNAL_ROOTS = '(GET|POST|PUT|PATCH|DELETE) /(internal|api/v1/traces)/.*|GET /api/v1/prometheus/auth|GET /healthz|GET /readyz|HTTP (GET|POST) prometheus|metrics\\..*|alert\\.Evaluate';

export function traceSearchQuery(
  traceQL: string,
  service: string,
  operation: string,
  peerService = '',
  scope: TraceScope = 'business',
  quickFilter: TraceQuickFilter = '',
): string {
  const explicit = traceQL.trim();
  const clauses = [
    quickFilter,
    service && `resource.service.name = ${JSON.stringify(service)}`,
    operation && `span:name = ${JSON.stringify(operation)}`,
    peerService && `span.peer.service = ${JSON.stringify(peerService)}`,
    scope === 'business' && `trace:rootName !~ ${JSON.stringify(INTERNAL_ROOTS)}`,
    scope === 'internal' && `trace:rootName =~ ${JSON.stringify(INTERNAL_ROOTS)}`,
  ].filter(Boolean);
  const query = explicit || (clauses.length === 0 ? '{}' : `{ ${clauses.join(' && ')} }`);
  return /\bwith\s*\(/i.test(query) ? query : `${query} with (most_recent=true)`;
}
