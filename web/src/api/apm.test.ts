import { describe, expect, it } from 'vitest';
import { queryApm, serviceParams, serviceTraceQL, traceLink } from './apm';
import { http, HttpResponse } from 'msw';
import { server } from '@/test/msw-server';
import { absoluteWindow, correlationFilters, logTraceLink, canonicalTraceID } from '@/lib/telemetryContext';

describe('APM correlation', () => {
  it('uses resolved device membership or Kubernetes telemetry identity and fails closed while unresolved', () => {
    const p = new URLSearchParams({ service_name: 'orders', cluster_node_id: '101' });
    expect(serviceTraceQL(p)).toContain('resource.device_id =~ "a^"');
    p.set('cluster_device_ids', '42,43');
    expect(serviceTraceQL(p)).toContain('resource.device_id =~ "^(42|43)$"');
    p.set('device_id', '42');
    expect(serviceTraceQL(p)).toContain('resource.device_id = "42"');
    p.set('telemetry_cluster_id', '7');
    expect(serviceTraceQL(p)).toContain('resource.cluster_id = "7"');
    expect(serviceTraceQL(p)).not.toContain('resource.device_id =~');
    expect(serviceTraceQL(p)).not.toContain('cluster_id = "101"');
  });
  it('keeps trace-sample service discovery valid after visiting an all-protocol service', async () => {
    let received: URL | undefined;
    server.use(http.get('/api/v1/apm/services', ({ request }) => {
      received = new URL(request.url);
      return HttpResponse.json({ data: { items: [] } });
    }));
    await queryApm('services', new URLSearchParams({
      metric_source: 'tempo_spanmetrics', protocol: 'all', span_kind: 'consumer',
    }));
    expect(received?.searchParams.get('protocol')).toBe('http');
    expect(received?.searchParams.get('span_kind')).toBe('consumer');
    expect(received?.searchParams.get('metric_source')).toBe('tempo_spanmetrics');
  });
  const params = new URLSearchParams({
    start: '2026-09-07T00:00:00Z',
    end: '2026-09-07T01:00:00Z',
    page: '3',
    service_name: 'orders"}',
    environment: '',
    service_namespace: 'trade',
    span_kind: 'server',
    device_id: '42',
    cluster_id: '7',
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
    expect(next.get('protocol')).toBe('all');
    expect(next.get('device_id')).toBe('42');
    expect(next.get('cluster_id')).toBe('7');
    expect(serviceTraceQL(next)).toContain('resource.device_id = "42"');
    expect(serviceTraceQL(next)).toContain('resource.cluster_id = "7"');
    expect(serviceTraceQL(next)).not.toContain('span.http');
    expect(serviceTraceQL(next)).not.toContain('span.rpc');
    expect(serviceTraceQL(params)).toContain(String.raw`resource.service.name = "orders\"}"`);
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
  it('links native HTTP/RPC operations through attributes and keeps sampled span names separate', () => {
    const p = new URLSearchParams(params);
    p.set('operation', 'POST /orders/{id}');
    expect(serviceTraceQL(p)).toContain('span.http.route = "/orders/{id}"');
    expect(serviceTraceQL(p)).not.toContain(' && name =');
    p.set('protocol', 'rpc');
    p.set('operation', 'trade.Orders/Get');
    expect(serviceTraceQL(p)).toContain('span.rpc.method = "trade.Orders/Get"');
    expect(serviceTraceQL(p)).toContain('span.rpc.service = "trade.Orders"');
    expect(serviceTraceQL(p)).not.toContain('span.http');
    p.set('metric_source', 'tempo_spanmetrics');
    expect(serviceTraceQL(p)).toContain('name = "trade.Orders/Get"');
    expect(serviceTraceQL(p)).not.toContain('span.rpc');
  });

});
