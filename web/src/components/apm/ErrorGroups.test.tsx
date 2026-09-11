import { fireEvent, render, screen } from '@testing-library/react';
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
});
