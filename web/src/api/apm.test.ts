import { describe, expect, it } from 'vitest';
import { serviceParams, serviceTraceQL, traceLink } from './apm';
import { absoluteWindow, correlationFilters, logTraceLink, canonicalTraceID } from '@/lib/telemetryContext';

describe('APM correlation', () => {
  const params = new URLSearchParams({
    start: '2026-09-07T00:00:00Z',
    end: '2026-09-07T01:00:00Z',
    page: '3',
    service_name: 'orders"}',
    environment: '',
    service_namespace: 'trade',
    span_kind: 'server',
  });
  it('preserves full identity and exact time while resetting list pagination', () => {
    const next = serviceParams(params, {
      service_name: 'orders',
      service_namespace: 'other',
      environment: 'staging',
    });
    expect(next.has('page')).toBe(false);
    expect(next.get('start')).toBe(params.get('start'));
    expect(next.get('environment')).toBe('staging');
    expect(serviceTraceQL(params)).toContain('resource.service.name = "orders\\\"}"');
    expect(serviceTraceQL(params)).toContain('resource.deployment.environment.name = nil');
    expect(serviceTraceQL(params)).toContain('kind = server');
    expect(new URL(traceLink(params), 'http://localhost').searchParams.get('environment')).toBe('');
  });
  it('keeps empty scope distinct from all and uses exact trace IDs', () => {
    const p = new URLSearchParams(params);
    p.set('trace_id', '1234567890abcdef1234567890abcdef');
    expect(correlationFilters(p)).toContainEqual({ field: 'environment', operator: 'eq', values: [''] });
    const link = logTraceLink(
      {
        id: 'a',
        timestamp: '2026-09-07T00:30:00Z',
        backend: 'loki',
        message: 'test',
        trace_id: p.get('trace_id')!,
      },
      p,
    );
    expect(link).toContain('/traces/1234567890abcdef1234567890abcdef?');
    expect(new URL(link!, 'http://localhost').searchParams.get('end')).toBe(params.get('end'));
    expect(
      logTraceLink({ id: 'a', timestamp: '', backend: 'loki', message: 'x', trace_id: '../bad' }, p),
    ).toBeNull();
    expect(absoluteWindow(new URLSearchParams('start=bad&end=bad'))).toBeNull();
    expect(canonicalTraceID('3')).toBe('00000000000000000000000000000003');
    expect(canonicalTraceID('0')).toBe('');
  });
});
