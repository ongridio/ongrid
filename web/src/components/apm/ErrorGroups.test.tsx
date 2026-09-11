import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';
import { server } from '@/test/msw-server';
import { ErrorGroups } from './ErrorGroups';
const params = new URLSearchParams({ service_name: 'orders', service_namespace: 'trade', environment: 'prod', protocol: 'all', service_version: 'v2', instance_id: 'pod-2', start: '2026-09-09T00:00:00Z', end: '2026-09-09T01:00:00Z' });
const group = { fingerprint: 'first', operation: 'GET /orders', error_type: 'DatabaseError', status_code: '500', stack_trace: 'orders.go:42', count: 17, first_seen: 1788912000, last_seen: 1788915600, versions: ['v2'], instances: ['pod-2'], trace_id: '0123456789abcdef0123456789abcdef', span_id: '1' };
describe('Error aggregation', () => {
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('preserves scope and displays bounded counts, evidence and partial failures', async () => {
    server.use(http.get('/api/v1/apm/error-groups', ({ request }) => {
      const q = new URL(request.url).searchParams;
      for (const key of ['service_name', 'service_namespace', 'environment', 'service_version', 'instance_id', 'start', 'end']) expect(q.get(key)).toBe(params.get(key));
      if (q.get('protocol') === 'rpc') return HttpResponse.json({ message: 'RPC unavailable' }, { status: 503 });
      return HttpResponse.json({ data: { items: [group], total: 1, page: 1, page_size: 25, sampled_traces: 49, failed_traces: 1, truncated: true } });
    }));
    render(<MemoryRouter><ErrorGroups params={params} refresh={0} /></MemoryRouter>);
    expect(await screen.findByText('17 个错误 Span')).toBeInTheDocument();
    expect(await screen.findByRole('alert')).toHaveTextContent('RPC unavailable');
    expect(screen.getByText(/已达到 50 条链路上限/)).toHaveTextContent('1 条链路详情不可用');
    fireEvent.click(screen.getByText('版本、实例与堆栈'));
    expect(screen.getByText('orders.go:42')).toBeVisible();
    expect(screen.getByText('实例: pod-2')).toBeVisible();
    expect(screen.getByRole('link', { name: /代表链路/ })).toHaveAttribute('href', expect.stringContaining(`/traces/${group.trace_id}?`));
    expect(screen.getByRole('button', { name: 'AI 分析' })).toBeEnabled();
  });

  it('reuses snapshot pages independently and refreshes from fresh traces', async () => {
    const calls: { protocol: string; page: number; snapshot: string | null }[] = [];
    let generation = 1;
    server.use(http.get('/api/v1/apm/error-groups', ({ request }) => {
      const q = new URL(request.url).searchParams;
      const protocol = q.get('protocol')!, page = Number(q.get('page'));
      calls.push({ protocol, page, snapshot: q.get('snapshot_id') });
      return HttpResponse.json({ data: {
        items: Array.from({ length: page === 1 ? 25 : 1 }, (_, i) => ({ ...group, fingerprint: `${protocol}-${page}-${i}`, operation: `${protocol}-${page}-${i}`, count: generation })),
        total: 26, page, page_size: 25, sampled_traces: 50, failed_traces: 0, truncated: true, snapshot_id: `${protocol}-snapshot-${generation}`,
      } });
    }));
    const { rerender } = render(<MemoryRouter><ErrorGroups params={params} refresh={0} /></MemoryRouter>);
    const section = screen.getByRole('region', { name: 'HTTP 错误聚合' });
    await screen.findByText('http-1-0');
    await screen.findByText('rpc-1-0');
    expect(calls).toHaveLength(2);
    fireEvent.click(within(section).getByRole('button', { name: '下一页' }));
    await screen.findByText('http-2-0');
    expect(calls).toHaveLength(3);
    expect(calls[2]).toEqual({ protocol: 'http', page: 2, snapshot: 'http-snapshot-1' });
    expect(screen.getByText('rpc-1-0')).toBeVisible();
    fireEvent.click(within(section).getByRole('button', { name: '上一页' }));
    await screen.findByText('http-1-0');
    expect(calls).toHaveLength(3);
    generation++;
    rerender(<MemoryRouter><ErrorGroups params={params} refresh={1} /></MemoryRouter>);
    await waitFor(() => expect(calls).toHaveLength(5));
    expect(calls.slice(-2)).toEqual(expect.arrayContaining([
      { protocol: 'http', page: 1, snapshot: null }, { protocol: 'rpc', page: 1, snapshot: null },
    ]));
    await screen.findByText('http-1-0');
    expect(within(section).getAllByText('2 个错误 Span')).toHaveLength(25);
  });

  it('retries an expired page with a fresh first page without reloading the other protocol', async () => {
    const calls: string[] = [];
    server.use(http.get('/api/v1/apm/error-groups', ({ request }) => {
      const q = new URL(request.url).searchParams;
      calls.push(`${q.get('protocol')}:${q.get('page')}:${q.get('snapshot_id') || 'fresh'}`);
      if (q.has('snapshot_id')) return HttpResponse.json({ message: 'Snapshot expired' }, { status: 400 });
      return HttpResponse.json({ data: { items: [{ ...group, operation: q.get('protocol') }], total: 26, page: 1, page_size: 25, sampled_traces: 50, failed_traces: 0, truncated: true, snapshot_id: 'first-snapshot' } });
    }));
    render(<MemoryRouter><ErrorGroups params={params} refresh={0} /></MemoryRouter>);
    const section = screen.getByRole('region', { name: 'HTTP 错误聚合' });
    await screen.findByText('http');
    await screen.findByText('rpc');
    fireEvent.click(within(section).getByRole('button', { name: '下一页' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Snapshot expired');
    fireEvent.click(within(section).getByRole('button', { name: '重试' }));
    await screen.findByText('http');
    expect(calls.filter((call) => call.startsWith('http:'))).toEqual(['http:1:fresh', 'http:2:first-snapshot', 'http:1:fresh']);
    expect(calls.filter((call) => call.startsWith('rpc:'))).toEqual(['rpc:1:fresh']);
  });
});
