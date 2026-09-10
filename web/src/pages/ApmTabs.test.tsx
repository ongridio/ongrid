import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import ApmPage from './Apm';

vi.mock('@/pages/DailyTools', () => ({ NativeFlamegraph: ({ error, loading }: { error: string; loading: boolean }) => <div>{error || (loading ? 'Loading profile' : 'Profile loaded')}</div> }));
vi.mock('@/components/apm/Dependencies', () => ({ Dependencies: () => null }));
const scope = new URLSearchParams({ service_name: 'orders', environment: 'production', service_namespace: 'trade', range: 'custom', start: '2026-09-10T00:00:00Z', end: '2026-09-10T01:00:00Z', device_id: '42' });
beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  server.use(
    http.get('/api/v1/devices', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: null })),
    http.get('/api/v1/apm/runtime', () => HttpResponse.json({ data: { items: [], instances: [{ instance_id: 'orders-1', version: 'v1', device_id: '42' }] } })),
  );
});
it('orders the eight service views and preserves scope while switching between traces and errors', async () => {
  const queries: URLSearchParams[] = [];
  server.use(http.get('/api/v1/traces/search', ({ request }) => {
    queries.push(new URL(request.url).searchParams);
    return HttpResponse.json({ traces: [{ traceID: '0123456789abcdef0123456789abcdef', rootTraceName: 'GET /orders', durationMs: 20 }] });
  }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=traces`]}><ApmPage /></MemoryRouter>);
  const tabs = within(screen.getByRole('tablist', { name: '服务视图' })).getAllByRole('tab');
  expect(tabs.map((tab) => tab.textContent)).toEqual(['概览', '接口', '实例', '依赖', '链路', '错误', '日志', '性能剖析']);
  for (const tab of tabs) {
    const url = new URL(tab.getAttribute('href')!, 'http://localhost');
    for (const [key, value] of scope) expect(url.searchParams.get(key)).toBe(value);
  }
  await screen.findByText('GET /orders');
  expect(queries.at(-1)?.get('q')).not.toContain('status = error');
  expect(screen.queryByRole('button', { name: 'AI 分析' })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('tab', { name: '错误' }));
  await screen.findByRole('button', { name: 'AI 分析' });
  const query = queries.at(-1)!;
  for (const value of ['status = error', 'resource.service.name = "orders"', 'resource.service.namespace = "trade"', 'resource.deployment.environment.name = "production"', 'resource.device_id = "42"']) expect(query.get('q')).toContain(value);
  expect(query.get('start')).toBe(scope.get('start'));
  expect(screen.getByRole('tab', { name: '错误' })).toHaveAttribute('aria-selected', 'true');
});
it('queries scoped service logs and releases the unused pagination cursor', async () => {
  let input: Record<string, unknown> | undefined;
  let cursorClosed = false;
  server.use(
    http.post('/api/v1/logs/search', async ({ request }) => {
      input = await request.json() as Record<string, unknown>;
      return HttpResponse.json({ data: { records: [{ id: '1', timestamp: scope.get('start'), message: 'order accepted', severity_text: 'INFO' }], next_cursor: 'next-page', has_more: true } });
    }),
    http.post('/api/v1/logs/cursor/close', async ({ request }) => { cursorClosed = (await request.json() as { cursor: string }).cursor === 'next-page'; return HttpResponse.json({ data: null }); }),
  );
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs&cluster_node_id=7`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('order accepted');
  expect(input).toMatchObject({ start: new Date(scope.get('start')!).toISOString(), end: new Date(scope.get('end')!).toISOString(), scope: { device_ids: [42], cluster_ids: ['7'] }, limit: 50 });
  expect(input?.filters).toEqual(expect.arrayContaining([{ field: 'service_namespace', operator: 'eq', values: ['trade'] }, { field: 'environment', operator: 'eq', values: ['production'] }]));
  await waitFor(() => expect(cursorClosed).toBe(true));
});
it('does not silently broaden logs when an instance filter cannot be applied', async () => {
  let searches = 0;
  server.use(http.post('/api/v1/logs/search', () => { searches++; return HttpResponse.json({ data: { records: [], has_more: false } }); }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs&instance_id=orders-1&service_version=v1&cluster_node_id=7`]}><ApmPage /></MemoryRouter>);
  expect(screen.getByText('日志暂不支持版本和实例筛选')).toBeInTheDocument();
  expect(searches).toBe(0);
  fireEvent.click(screen.getByRole('button', { name: '清除版本和实例筛选' }));
  await screen.findByText('当前范围未查询到日志');
  expect(searches).toBe(1);
  const traces = new URL(screen.getByRole('tab', { name: '链路' }).getAttribute('href')!, 'http://localhost');
  expect(traces.searchParams.get('cluster_node_id')).toBe('7');
  expect(traces.searchParams.has('cluster_id')).toBe(false);
});
it('queries historical profiles for the selected device, service and instance without starting a capture', async () => {
  let query: URLSearchParams | undefined;
  server.use(http.get('/api/v1/profiles/flamegraph', ({ request }) => { query = new URL(request.url).searchParams; return HttpResponse.json({ flamebearer: { names: [], levels: [], numTicks: 0, maxSelf: 0 }, metadata: {} }); }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=profiles`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('Profile loaded');
  for (const key of ['environment', 'service_namespace', 'device_id', 'start', 'end']) expect(query?.get(key)).toBe(scope.get(key));
  expect(query?.get('service')).toBe('orders');
  expect(query?.get('instance_id')).toBe('orders-1');
  const link = new URL(screen.getByRole('link', { name: /按需 pprof/ }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('service_name')).toBe('orders');
  expect(link.searchParams.get('tool')).toBe('profile');
});
