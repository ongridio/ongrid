import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import ApmPage from './Apm';

beforeEach(() => server.use(
  http.get('/api/v1/system-settings', () => HttpResponse.json({ items: [], total: 0 })),
  http.get('/api/v1/edges', () => HttpResponse.json({ items: [], total: 0 })),
));

vi.mock('@/pages/DailyTools', () => ({ NativeFlamegraph: ({ error, loading }: { error: string; loading: boolean }) => <div>{error || (loading ? 'Loading profile' : 'Profile loaded')}</div> }));
vi.mock('@/components/apm/Dependencies', () => ({ Dependencies: () => null }));
const scope = new URLSearchParams({ service_name: 'orders', environment: 'production', service_namespace: 'trade', range: 'custom', start: '2026-09-10T00:00:00Z', end: '2026-09-10T01:00:00Z', device_id: '42' });
beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  server.use(
    http.get('/api/v1/devices', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: [
      { id: 132, type: 'cluster', name: 'Kubernetes', props: { source: 'kubernetes', k8s_cluster_id: 50 } },
      { id: 50, type: 'cluster', name: 'Other cluster', props: { source: 'kubernetes', k8s_cluster_id: 75 } },
      { id: 133, type: 'cluster', name: 'Second cluster', props: { source: 'kubernetes', k8s_cluster_id: 51 } },
    ] })),
    http.get('/api/v1/topology/relations', () => HttpResponse.json({ items: [] })),
    http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: null })),
    http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { items: [], instances: [{ instance_id: 'orders-1', version: 'v1', device_id: '42' }] } })),
  );
});
it('orders the nine service views and preserves scope while switching between traces and errors', async () => {
  const queries: URLSearchParams[] = [];
  server.use(http.get('/api/v1/traces/search', ({ request }) => {
    queries.push(new URL(request.url).searchParams);
    return HttpResponse.json({ traces: [{ traceID: '0123456789abcdef0123456789abcdef', rootTraceName: 'GET /orders', durationMs: 20 }] });
  }));
  const errorQueries: URLSearchParams[] = [];
  server.use(http.get('/api/v1/apm/error-groups', ({ request }) => {
    errorQueries.push(new URL(request.url).searchParams);
    return HttpResponse.json({ data: { items: [{ fingerprint: 'one', operation: 'GET /orders', count: 1, first_seen: 1788998400, last_seen: 1788998400, versions: ['v1'], instances: ['orders-1'], trace_id: '0123456789abcdef0123456789abcdef', stack_trace: '' }], total: 1, page: 1, page_size: 25, sampled_traces: 1, failed_traces: 0 } });
  }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=traces&protocol=http&operation=GET%20%2Forders`]}><ApmPage /></MemoryRouter>);
  const tabs = within(screen.getByRole('tablist', { name: '服务视图' })).getAllByRole('tab');
  expect(tabs.map((tab) => tab.textContent)).toEqual(['概览', '接口', '实例', '依赖', '链路', '错误', '版本对比', '日志', '性能剖析']);
  for (const tab of tabs) {
    const url = new URL(tab.getAttribute('href')!, 'http://localhost');
    for (const [key, value] of scope) expect(url.searchParams.get(key)).toBe(value);
  }
  await screen.findByText('GET /orders');
  expect(queries.at(-1)?.get('q')).not.toContain('status = error');
  expect(screen.queryByRole('button', { name: 'AI 分析' })).not.toBeInTheDocument();
  expect(errorQueries).toHaveLength(0);
  fireEvent.click(screen.getByRole('tab', { name: '错误' }));
  await screen.findAllByRole('button', { name: 'AI 分析' });
  const query = errorQueries.at(-1)!;
  expect(errorQueries).toHaveLength(2);
  for (const [key, value] of scope) if (key !== 'range') expect(query.get(key)).toBe(value);
  expect(query.get('start')).toBe(scope.get('start'));
  expect(query.has('tab')).toBe(false);
  expect(query.has('range')).toBe(false);
  expect(screen.getByRole('tab', { name: '错误' })).toHaveAttribute('aria-selected', 'true');
  fireEvent.click(screen.getByRole('tab', { name: '链路' }));
  fireEvent.click(screen.getByRole('tab', { name: '错误' }));
  await screen.findAllByRole('button', { name: 'AI 分析' });
  expect(errorQueries).toHaveLength(2);
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
it('keeps version and instance filters in log queries and explorer links', async () => {
  let input: Record<string, unknown> | undefined;
  server.use(http.post('/api/v1/logs/search', async ({ request }) => { input = await request.json() as Record<string, unknown>; return HttpResponse.json({ data: { records: [], has_more: false } }); }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs&instance_id=orders-1&service_version=v1&cluster_node_id=7`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('当前范围未查询到日志');
  expect(input?.filters).toEqual(expect.arrayContaining([
    { field: 'service_version', operator: 'eq', values: ['v1'] },
    { field: 'instance_id', operator: 'eq', values: ['orders-1'] },
  ]));
  expect(input?.scope).toEqual({ device_ids: [42], cluster_ids: ['7'] });
  const link = new URL(screen.getByRole('link', { name: /打开日志检索/ }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('service_version')).toBe('v1');
  expect(link.searchParams.get('instance_id')).toBe('orders-1');
});
it.each(['', '&cluster_node_id=132', '&cluster_id=50'])('maps telemetry clusters to unified clusters for container logs: %s', async (clusterFilter) => {
  const requests: Record<string, unknown>[] = [];
  const instanceQueries: URLSearchParams[] = [];
  server.use(
    http.get('/api/v1/apm/instances', ({ request }) => {
      const query = new URL(request.url).searchParams;
      instanceQueries.push(query);
      return HttpResponse.json({ data: { instances: query.get('cluster_id') === '132' ? [] : [
        { instance_id: 'trade.orders-abc.orders', version: 'v1', device_id: '42', cluster_id: '50', namespace: 'payments', pod: 'orders-abc' },
        { instance_id: 'trade.orders-def.orders', version: 'v2', device_id: '42', cluster_id: '50', namespace: 'payments', pod: 'orders-def' },
      ] } });
    }),
    http.post('/api/v1/logs/search', async ({ request }) => {
      const input = await request.json() as Record<string, unknown>;
      requests.push(input);
      return HttpResponse.json({ data: { records: requests.length === 2 ? [{ id: 'pod-log', timestamp: scope.get('start'), message: 'container output' }] : [], has_more: false } });
    }),
  );
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs&instance_id=trade.orders-abc.orders&service_version=v1${clusterFilter}`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('container output');
  expect(requests).toHaveLength(2);
  expect(requests[1]).toMatchObject({ scope: { pods: ['orders-abc'], device_ids: [42], namespaces: ['payments'], cluster_ids: ['132'] } });
  expect(requests[1]).not.toHaveProperty('filters');
  expect(instanceQueries.length).toBeGreaterThan(0);
  expect(instanceQueries.every(query => query.get('cluster_id') !== '132')).toBe(true);
  if (clusterFilter.includes('cluster_node_id')) {
    expect(instanceQueries.every(query => query.get('cluster_node_id') === '132')).toBe(true);
    expect(requests[0]).toMatchObject({ scope: { cluster_ids: ['132'] } });
  }
  const link = new URL(screen.getByRole('link', { name: /打开日志检索/ }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('pod')).toBe('orders-abc');
  expect(link.searchParams.get('device_id')).toBe('42');
  expect(link.searchParams.get('namespace')).toBe('payments');
  expect(link.searchParams.get('cluster_id')).toBe('132');
  expect(link.searchParams.has('cluster_node_id')).toBe(false);
  expect(link.searchParams.has('service_name')).toBe(false);
});
it.each([false, true])('normalizes new and legacy instances before matching logs, mismatch=%s', async (mismatch) => {
  const requests: Record<string, unknown>[] = [];
  server.use(
    http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { instances: [
      { instance_id: 'one', device_id: '42', cluster_id: '50', namespace: 'payments', pod: 'orders-abc' },
      { instance_id: 'one', device_id: '42', cluster_id: mismatch ? '50' : '132', k8s_cluster_id: '50', namespace: 'payments', pod: 'orders-abc' },
    ] } })),
    http.post('/api/v1/logs/search', async ({ request }) => {
      requests.push(await request.json() as Record<string, unknown>);
      return HttpResponse.json({ data: { records: [], has_more: false } });
    }),
  );
  const params = new URLSearchParams(scope);
  params.delete('cluster_id');
  render(<MemoryRouter initialEntries={[`/apm/service?${params}&tab=logs`]}><ApmPage /></MemoryRouter>);
  if (mismatch) {
    await screen.findByText(/无法解析容器日志的集群/);
    expect(requests).toHaveLength(1);
  } else {
    await screen.findByText(/按已观测到的 Pod/);
    expect(requests).toHaveLength(2);
    expect(requests[1]).toMatchObject({ scope: { cluster_ids: ['132'], pods: ['orders-abc'], device_ids: [42], namespaces: ['payments'] } });
  }
});
it.each(['missing', 'ambiguous'])('does not query unscoped container logs when the cluster mapping is %s', async (mapping) => {
  const requests: Record<string, unknown>[] = [];
  server.use(
    http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: mapping === 'missing' ? [] : [
      { id: 132, props: { source: 'kubernetes', k8s_cluster_id: 50 } },
      { id: 133, props: { source: 'kubernetes', k8s_cluster_id: 50 } },
    ] })),
    http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { instances: [
      { instance_id: 'one', device_id: '42', cluster_id: '50', namespace: 'payments', pod: 'orders-abc' },
    ] } })),
    http.post('/api/v1/logs/search', async ({ request }) => {
      requests.push(await request.json() as Record<string, unknown>);
      return HttpResponse.json({ data: { records: [], has_more: false } });
    }),
  );
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs`]}><ApmPage /></MemoryRouter>);
  expect(await screen.findByRole('alert')).toHaveTextContent('无法解析容器日志的集群');
  expect(requests).toHaveLength(1);
  expect(screen.queryByText(/按已观测到的 Pod/)).not.toBeInTheDocument();
});
it('preserves all observed devices in the container log explorer link', async () => {
  const requests: Record<string, unknown>[] = [];
  server.use(
    http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { instances: [
      { instance_id: 'one', device_id: '42', cluster_id: '50', namespace: 'payments', pod: 'orders-abc' },
      { instance_id: 'two', device_id: '43', cluster_id: '50', namespace: 'payments', pod: 'orders-def' },
    ] } })),
    http.post('/api/v1/logs/search', async ({ request }) => {
      requests.push(await request.json() as Record<string, unknown>);
      return HttpResponse.json({ data: { records: [], has_more: false } });
    }),
  );
  const allDevices = new URLSearchParams(scope);
  allDevices.delete('device_id');
  render(<MemoryRouter initialEntries={[`/apm/service?${allDevices}&tab=logs`]}><ApmPage /></MemoryRouter>);
  await screen.findByText(/按已观测到的 Pod/);
  expect(requests[1]).toMatchObject({ scope: { device_ids: [42, 43], cluster_ids: ['132'], namespaces: ['payments'] } });
  const link = new URL(screen.getByRole('link', { name: /打开日志检索/ }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('device_id')).toBe('42,43');
  expect(link.searchParams.get('cluster_id')).toBe('132');
  expect(link.searchParams.get('namespace')).toBe('payments');
});
it.each([['payments', 'staging', '50'], ['payments', undefined, '50'], ['payments', 'payments', '51']])('does not broaden container log matching across namespace/cluster boundaries: %s / %s / %s', async (first, second, cluster) => {
  const requests: Record<string, unknown>[] = [];
  server.use(
    http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { instances: [
      { instance_id: 'one', device_id: '42', cluster_id: '50', namespace: first, pod: 'same-name' },
      { instance_id: 'two', device_id: '42', cluster_id: cluster, namespace: second, pod: 'other-name' },
    ] } })),
    http.post('/api/v1/logs/search', async ({ request }) => {
      requests.push(await request.json() as Record<string, unknown>);
      return HttpResponse.json({ data: { records: [], has_more: false } });
    }),
  );
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=logs`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('当前范围未查询到日志');
  expect(requests).toHaveLength(1);
  expect(screen.queryByText(/按已观测到的 Pod/)).not.toBeInTheDocument();
});
it('queries historical profiles for the selected device, service and instance without starting a capture', async () => {
  let query: URLSearchParams | undefined;
  server.use(http.get('/api/v1/profiles/flamegraph', ({ request }) => { query = new URL(request.url).searchParams; return HttpResponse.json({ flamebearer: { names: [], levels: [], numTicks: 0, maxSelf: 0 }, metadata: {} }); }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=profiles`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('Profile loaded');
  for (const key of ['environment', 'service_namespace', 'device_id', 'start', 'end']) expect(query?.get(key)).toBe(scope.get(key));
  expect(query?.get('service')).toBe('orders');
  expect(query?.get('instance_id')).toBe('orders-1');
  expect(query?.get('service_version')).toBe('v1');
  const link = new URL(screen.getByRole('link', { name: /按需 pprof/ }).getAttribute('href')!, 'http://localhost');
  expect(link.searchParams.get('service_name')).toBe('orders');
  expect(link.searchParams.get('tool')).toBe('profile');
  expect(link.searchParams.get('service_version')).toBe('v1');
});

it('scopes diagnostics and retries a partial protocol failure', async () => {
  const queries: URLSearchParams[] = [];
  let failRPC = true;
  server.use(http.get('/api/v1/apm/diagnostics', ({ request }) => {
    const query = new URL(request.url).searchParams;
    queries.push(query);
    if (query.get('protocol') === 'rpc' && failRPC) return HttpResponse.json({ message: 'RPC backend unavailable' }, { status: 503 });
    return HttpResponse.json({ data: { checks: [
      { key: 'coverage', status: 'unknown', detail: 'expected_instances_unknown' },
      { key: 'instance_identity', status: 'incomplete', detail: 'service_instance_id' },
      { key: 'metric_freshness', status: 'observed', detail: 'prometheus_sample_at_window_end' },
    ], instances: [], trace_ids: [], sampled_traces: 0, last_metric_timestamp: 1789001940 } });
  }));
  render(<MemoryRouter initialEntries={[`/apm/service?${scope}&tab=onboarding&service_version=v1&instance_id=orders-1`]}><ApmPage /></MemoryRouter>);
  await screen.findByText('未配置预期实例数，不能计算覆盖率。');
  expect(screen.getByText('信息不完整')).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('RPC backend unavailable');
  for (const query of queries) {
    for (const [key, value] of scope) expect(query.get(key)).toBe(value);
    expect(query.get('service_version')).toBe('v1');
    expect(query.get('instance_id')).toBe('orders-1');
  }
  failRPC = false;
  fireEvent.click(screen.getByRole('button', { name: '重新检查' }));
  await waitFor(() => expect(screen.getAllByText('未配置预期实例数，不能计算覆盖率。')).toHaveLength(2));
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});
it('gives discovery a selected top-level tab and hides metric time controls', async () => {
  server.use(http.get('/api/v1/k8s/edge-attachments', () => HttpResponse.json({ data: { items: [], total: 0 } })));
  render(<MemoryRouter initialEntries={['/apm?tab=discovery']}><ApmPage /></MemoryRouter>);
  const tabs = within(screen.getByRole('tablist', { name: '服务视图' })).getAllByRole('tab');
  expect(tabs.map(tab => tab.textContent)).toEqual(['服务列表', '服务地图', '服务发现', '接入指南']);
  expect(screen.getByRole('tab', { name: '服务发现' })).toHaveAttribute('aria-selected', 'true');
  expect(screen.queryByRole('button', { name: '时间范围' })).not.toBeInTheDocument();
  expect(await screen.findByText('尚未接入设备')).toBeInTheDocument();
});

it('lets legacy device discovery links navigate to the setup guide', async () => {
  render(<MemoryRouter initialEntries={['/apm?tab=onboarding&capture_edge_id=67']}><ApmPage /></MemoryRouter>);
  expect(screen.getByRole('tab', { name: '服务发现' })).toHaveAttribute('aria-selected', 'true');
  fireEvent.click(screen.getByRole('tab', { name: '接入指南' }));
  expect(screen.getByRole('tab', { name: '接入指南' })).toHaveAttribute('aria-selected', 'true');
  expect(screen.queryByRole('switch', { name: '全局自动发现' })).not.toBeInTheDocument();
});
