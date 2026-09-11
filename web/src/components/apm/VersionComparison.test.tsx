import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import { VersionComparison } from './VersionComparison';

const params = new URLSearchParams({ service_name: 'orders', service_namespace: 'trade', environment: 'prod', protocol: 'all', instance_id: 'old-pod', service_version: 'v1', start: '2026-09-09T00:00:00Z', end: '2026-09-09T01:00:00Z', device_id: '42', baseline_version: 'v1', comparison_version: 'v2' });
const response = (version: string, sampled = false) => ({ metadata: { metric_source: sampled ? 'tempo_spanmetrics' : 'application_metrics', metric_format: 'otel', sampling: sampled ? 'unknown' : 'not_applicable' }, summary: { requests: 100, rps: 1, error_rate: version === 'v1' ? 2 : 5, p95_ms: version === 'v1' ? 100 : 160, p99_ms: null }, points: [] });
describe('Version comparison', () => {
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('compares protocols in the same scope without querying curves or treating missing data as zero', async () => {
    const queries: URLSearchParams[] = [];
    server.use(http.get('/api/v1/apm/summary', ({ request }) => {
      const q = new URL(request.url).searchParams; queries.push(q);
      return HttpResponse.json({ data: response(q.get('service_version')!) });
    }));
    render(<MemoryRouter><VersionComparison params={params} versions={['v1', 'v2']} refresh={0} onChange={vi.fn()} /></MemoryRouter>);
    expect(await screen.findAllByText('+3 个百分点')).toHaveLength(2);
    expect(screen.getAllByText('+60 ms')).toHaveLength(2);
    expect(queries).toHaveLength(4);
    for (const q of queries) {
      for (const key of ['service_name', 'environment', 'service_namespace', 'start', 'end', 'device_id']) expect(q.get(key)).toBe(params.get(key));
      expect(q.has('instance_id')).toBe(false);
      expect(q.has('baseline_version')).toBe(false);
    }
    expect(queries.map((q) => `${q.get('protocol')}/${q.get('service_version')}`).sort()).toEqual(['http/v1', 'http/v2', 'rpc/v1', 'rpc/v2']);
    const p99 = screen.getAllByRole('row').filter((row) => within(row).queryByText('P99'));
    expect(p99.every((row) => within(row).getAllByText('—').length === 3)).toBe(true);
    for (const link of screen.getAllByRole('link')) {
      const target = new URL(link.getAttribute('href')!, 'http://localhost');
      expect(['v1', 'v2']).toContain(target.searchParams.get('service_version'));
      expect(target.searchParams.has('instance_id')).toBe(false);
      expect(target.searchParams.get('start')).toBe(params.get('start'));
    }
  });
  it('does not calculate deltas across full metrics and trace samples', async () => {
    server.use(http.get('/api/v1/apm/summary', ({ request }) => {
      const version = new URL(request.url).searchParams.get('service_version')!;
      return HttpResponse.json({ data: response(version, version === 'v2') });
    }));
    render(<MemoryRouter><VersionComparison params={params} versions={['v1', 'v2']} refresh={0} onChange={vi.fn()} /></MemoryRouter>);
    expect(await screen.findAllByText(/两个版本的数据来源或采样口径不同/)).toHaveLength(2);
    expect(screen.queryByText('+3 个百分点')).not.toBeInTheDocument();
    expect(screen.queryByText('+60 ms')).not.toBeInTheDocument();
  });
});
