import userEvent from '@testing-library/user-event';
import { selectOption } from '@/test/select-option';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { delay, http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import ApmPage from './Apm';
import { Onboarding } from '@/components/apm/Onboarding';

vi.mock('@/components/apm/Dependencies', () => ({ Dependencies: () => null }));
vi.mock('recharts', () => ({
  ResponsiveContainer: () => null,
  LineChart: () => null,
  Line: () => null,
  CartesianGrid: () => null,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));
const period = 'start=2026-09-07T00:00:00Z&end=2026-09-07T01:00:00Z';
const row = {
  identity: {
    service_name: 'orders',
    service_namespace: 'trade',
    environment: 'production',
  },
  rps: 2,
  error_rate: 0,
  p50_ms: 100,
  p95_ms: 200,
  p99_ms: 220,
  requests: 7200,
  data_status: 'observed',
};
describe('Application performance', () => {
  it('only loads runtime curves on the instances tab', async () => {
    let discovery = 0, curves = 0;
    server.use(
      http.get('/api/v1/apm/instances', () => { discovery++; return HttpResponse.json({ data: { items: [], instances: [] } }); }),
      http.get('/api/v1/apm/runtime', () => { curves++; return HttpResponse.json({ data: { items: [], instances: [] } }); }),
    );
    render(<MemoryRouter initialEntries={[`/apm?${period}&service_name=orders&environment=production&service_namespace=trade&tab=traces`]}><ApmPage /></MemoryRouter>);
    await screen.findByText('当前范围未观测到链路');
    expect(discovery).toBe(1);
    expect(curves).toBe(0);
    fireEvent.click(screen.getByRole('tab', { name: '实例' }));
    await waitFor(() => expect(curves).toBe(1));
  });
  it.each([
    ['版本', 'service_version', 'v2', 'v1'],
    ['实例', 'instance_id', 'pod-2', 'pod-1'],
    ['环境', 'environment', 'staging', 'production'],
    ['业务命名空间', 'service_namespace', 'sales', 'trade'],
  ])('keeps %s options stable while switching and refreshing', async (label, key, first, second) => {
    const detail = key === 'service_version' || key === 'instance_id';
    let latest = new URLSearchParams();
    server.use(http.get(`/api/v1/apm/${detail ? 'runtime' : 'services'}`, async ({ request }) => {
      latest = new URL(request.url).searchParams;
      await delay(100);
      return HttpResponse.json({ data: detail ? {
        instances: [{ instance_id: 'pod-1', version: 'v1' }, { instance_id: 'pod-2', version: 'v2' }], items: [],
      } : { items: [row], total: 1, environments: ['production', 'staging'], service_namespaces: ['trade', 'sales'] } });
    }));
    render(<MemoryRouter initialEntries={[`/apm?${period}${detail ? '&service_name=orders&environment=production&service_namespace=trade&tab=instances' : ''}`]}><ApmPage /></MemoryRouter>);
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
    const trigger = screen.getByRole('combobox', { name: label });
    for (const value of [first, second, first]) {
      await selectOption(trigger, value);
      await waitFor(() => expect(latest.get(key)).toBe(value));
      // Options must remain available before the replacement request completes.
      await userEvent.click(trigger);
      expect(await screen.findByRole('option', { name: first })).toBeInTheDocument();
      expect(screen.getByRole('option', { name: second })).toBeInTheDocument();
      await userEvent.keyboard('{Escape}');
      await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
      expect(trigger).toHaveTextContent(value);
      expect(latest.get(key)).toBe(value);
    }
  });

  it('filters services by device and cluster before pagination and preserves both in detail links', async () => {
    const queries: URLSearchParams[] = [];
    server.use(http.get('/api/v1/apm/services', ({ request }) => {
      const query = new URL(request.url).searchParams;
      queries.push(query);
      return HttpResponse.json({ data: { items: [row], total: 1, page: 1, page_size: 25 } });
    }));
    render(<MemoryRouter initialEntries={[`/apm?${period}&page=3`]}><ApmPage /></MemoryRouter>);
    await screen.findByRole('link', { name: 'orders' });
    expect(screen.getByRole('heading', { name: '服务' })).toBeInTheDocument();
    await selectOption(screen.getByRole('combobox', { name: '设备' }), 'ubuntu (#42)');
    await waitFor(() => expect(queries.at(-1)?.get('device_id')).toBe('42'));
    await selectOption(screen.getByRole('combobox', { name: '集群' }), 'production (#7)');
    await waitFor(() => expect(queries.at(-1)?.get('cluster_node_id')).toBe('7'));
    expect(queries.at(-1)?.get('device_id')).toBe('42');
    expect(queries.at(-1)?.has('page')).toBe(false);
    const link = new URL(screen.getByRole('link', { name: 'orders' }).getAttribute('href')!, 'http://localhost');
    expect(link.searchParams.get('device_id')).toBe('42');
    expect(link.searchParams.get('cluster_node_id')).toBe('7');
    expect(new URLSearchParams(link.searchParams.get('list_query')!).get('cluster_node_id')).toBe('7');
    await selectOption(screen.getByRole('combobox', { name: '设备' }), '全部设备');
    await waitFor(() => expect(queries.at(-1)?.has('device_id')).toBe(false));
    expect(queries.at(-1)?.get('cluster_node_id')).toBe('7');
  });
  beforeEach(() => { localStorage.setItem('ongrid-locale', 'zh-CN'); server.use(http.get('/api/v1/devices', () => HttpResponse.json({ items: [{ id: 42, name: 'ubuntu' }] })), http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: [{ id: 7, name: 'production' }] })), http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: null })), http.get('/api/v1/traces/search', () => HttpResponse.json({ traces: [] })), http.get('/api/v1/apm/runtime', () => HttpResponse.json({ data: { items: [], instances: [] } })), http.get('/api/v1/apm/instances', () => HttpResponse.json({ data: { items: [], instances: [] } }))); });
  it('keeps same-name services separate and links their complete identity', async () => {
    server.use(
      http.get('/api/v1/apm/repository-binding', ({ request }) => HttpResponse.json({ data: new URL(request.url).searchParams.get('environment') === 'production' ? { identity: row.identity, repo_id: '3', repo_url: 'ssh://git@example/apm-demo.git', source_directory: '', tag_pattern: '{version}' } : null })),
      http.get('/api/v1/apm/services', () =>
        HttpResponse.json({
          data: {
            items: [
              { ...row, languages: ['go', 'java', 'nodejs'] },
              {
                ...row,
                identity: { ...row.identity, environment: 'staging' },
                rps: 0.0004,
                error_rate: null,
                data_status: 'insufficient_samples',
              },
            ],
            total: 2,
            page: 1,
            page_size: 25,
          },
        }),
      ),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    const links = await screen.findAllByRole('link', { name: 'orders' });
    expect(links).toHaveLength(2);
    expect(await screen.findByRole('button', { name: 'apm-demo' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '绑定仓库' })).toBeInTheDocument();
    expect(screen.getByText('Go')).toBeInTheDocument();
    expect(screen.getByText('Java')).toBeInTheDocument();
    expect(screen.getByText('Node.js')).toBeInTheDocument();
    expect(document.querySelector('img[src="/icons/languages/go.svg"]')).toBeInTheDocument();
    expect(screen.getByText('未知')).toBeInTheDocument();
    const urls = links.map((link) => new URL(link.getAttribute('href')!, 'http://localhost'));
    expect(urls.map((url) => url.searchParams.get('environment'))).toEqual([
      'production',
      'staging',
    ]);
    expect(urls[0].searchParams.get('service_namespace')).toBe('trade');
    expect(urls[0].searchParams.get('start')).toBe('2026-09-07T00:00:00Z');
    expect(screen.getByText('样本不足')).toBeInTheDocument();
    expect(screen.getByText('0.0004')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '接入管理' }));
    expect(await screen.findByText('应用接入')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'orders' })).not.toBeInTheDocument();
  });
  it('labels sampled HTTP metrics in the list and overview and preserves the source in operation links', async () => {
    const sample = { ...row, metric_source: 'tempo_spanmetrics' };
    const metadata = { metric_source: 'tempo_spanmetrics', sampling: 'unknown', protocol: 'http', metric_format: 'otel' };
    server.use(
      http.get('/api/v1/apm/services', () => HttpResponse.json({ data: { items: [sample], total: 1, page: 1, page_size: 25 } })),
      http.get('/api/v1/apm/overview', ({ request }) => HttpResponse.json({ data: new URL(request.url).searchParams.get('protocol') === 'rpc'
        ? { summary: { ...row, rps: null, data_status: 'no_data' }, points: [], metadata: { ...metadata, metric_source: 'application_metrics' } }
        : { summary: sample, points: [], metadata } })),
      http.get('/api/v1/apm/operations', () => HttpResponse.json({ data: { items: [{ ...sample, operation: 'GET /orders/:id' }], total: 1, page: 1, page_size: 25, metadata } })),
    );
    render(<MemoryRouter initialEntries={[`/apm?${period}`]}><ApmPage /></MemoryRouter>);
    expect(await screen.findByText('Trace 样本 · 已观测')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'orders' }));
    expect(await screen.findByText(/RPS 为样本速率/)).toBeInTheDocument();
    const operation = (await screen.findAllByRole('link', { name: 'GET /orders/:id' }))[0];
    expect(new URL(operation.getAttribute('href')!, 'http://localhost').searchParams.get('metric_source')).toBe('tempo_spanmetrics');
  });
  it('displays backend failure as an error instead of a healthy empty list', async () => {
    server.use(
      http.get('/api/v1/apm/services', () =>
        HttpResponse.json({ message: 'telemetry unavailable' }, { status: 502 }),
      ),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('alert')).toHaveTextContent('telemetry unavailable');
    expect(screen.queryByText('当前范围未观测到服务请求指标')).not.toBeInTheDocument();
  });
  it('requests operations with full identity and isolates consumers', async () => {
    let requestURL: URL | undefined;
    server.use(
      http.get('/api/v1/apm/diagnostics', () =>
        HttpResponse.json({
          data: { checks: [], instances: [], trace_ids: [], sampled_traces: 0 },
        }),
      ),
      http.get('/api/v1/apm/operations', ({ request }) => {
        requestURL = new URL(request.url);
        return HttpResponse.json({
          data: {
            items: [{ ...row, operation: 'consume' }],
            total: 1,
            page: 1,
            page_size: 25,
          },
        });
      }),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=&service_namespace=trade&tab=operations&metric_source=tempo_spanmetrics&span_kind=consumer`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'consume' });
    fireEvent.click(screen.getByRole('button', { name: '接入管理' }));
    expect(screen.queryByLabelText('指标来源')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('指标格式')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('入口类型')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: '接口' }));
    await waitFor(() => expect(requestURL?.searchParams.get('span_kind')).toBe('consumer'));
    expect(requestURL?.searchParams.has('environment')).toBe(true);
    expect(requestURL?.searchParams.get('environment')).toBe('');
  });
  it('combines overview panels, preserves context and keeps metrics when dependencies fail', async () => {
    const urls: URL[] = [];
    server.use(
      http.get('/api/v1/apm/overview', ({ request }) => {
        urls.push(new URL(request.url));
        return HttpResponse.json({ data: { summary: row, points: [] } });
      }),
      http.get('/api/v1/apm/operations', ({ request }) => {
        urls.push(new URL(request.url));
        return HttpResponse.json({
          data: {
            items: [{ ...row, operation: 'POST /orders' }],
            total: 1,
            page: 1,
            page_size: 5,
          },
        });
      }),
      http.get('/api/v1/apm/dependencies', () =>
        HttpResponse.json({ message: 'dependency timeout' }, { status: 502 }),
      ),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=production&service_namespace=trade`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    const httpPanel = within(screen.getByRole('region', { name: 'HTTP' }));
    const operation = await httpPanel.findByRole('link', { name: 'POST /orders' });
    expect(await screen.findByRole('alert')).toHaveTextContent('dependency timeout');
    expect(httpPanel.getByText('P95 延迟')).toBeInTheDocument();
    expect(httpPanel.getAllByText('200')).toHaveLength(2);
    await selectOption(httpPanel.getByLabelText('延迟分位数'), 'P99 延迟');
    expect(httpPanel.getByText('220')).toBeInTheDocument();
    expect(urls.every((url) => url.searchParams.get('service_namespace') === 'trade')).toBe(true);
    expect(
      urls.find((url) => url.pathname.endsWith('/operations'))?.searchParams.get('page_size'),
    ).toBe('5');
    const linked = new URL(operation.getAttribute('href')!, 'http://localhost');
    expect(linked.pathname).toBe('/apm/service');
    expect(linked.searchParams.get('operation')).toBe('POST /orders');
    expect(linked.searchParams.get('start')).toBe('2026-09-07T00:00:00Z');
    expect(linked.searchParams.get('environment')).toBe('production');
    expect(screen.queryByRole('link', { name: '运行时指标' })).not.toBeInTheDocument();
    expect(screen.getByRole('tab', { name: '实例' })).toBeInTheDocument();
  });
  it('uses server facets and moves relative windows forward on manual refresh', async () => {
    let requested: URL | undefined;
    server.use(
      http.get('/api/v1/apm/services', ({ request }) => {
        requested = new URL(request.url);
        return HttpResponse.json({
          data: {
            items: [row],
            total: 72,
            page: 1,
            page_size: 25,
            environments: ['', 'production', 'staging'],
            service_namespaces: ['', 'trade'],
          },
        });
      }),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}&environment=&service_namespace=`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'orders' });
    expect(screen.getByText('服务 · 72')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('combobox', { name: '环境' }));
    expect(await screen.findByRole('option', { name: 'staging' })).toBeInTheDocument();
    expect(screen.queryByText('估算样本请求数')).not.toBeInTheDocument();
    expect(screen.queryByText('应用指标 · 独立于 Trace 采样')).not.toBeInTheDocument();
    expect(screen.queryByRole('option', { name: '未设置' })).not.toBeInTheDocument();
    expect(requested?.searchParams.has('environment')).toBe(false);
    expect(requested?.searchParams.has('service_namespace')).toBe(false);
    await selectOption(screen.getByRole('combobox', { name: '环境' }), 'staging');
    await waitFor(() => expect(requested?.searchParams.get('environment')).toBe('staging'));
    fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
    fireEvent.click(screen.getByRole('button', { name: '最近 15 分钟' }));
    await waitFor(() => expect(requested?.searchParams.get('range')).toBe('15m'));
    const oldEnd = requested!.searchParams.get('end')!;
    expect(Date.parse(oldEnd) - Date.parse(requested!.searchParams.get('start')!)).toBe(900000);
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: '刷新' }));
    await waitFor(() => expect(requested!.searchParams.get('end')).not.toBe(oldEnd));
    expect(requested?.searchParams.get('environment')).toBe('staging');
    await selectOption(screen.getByRole('combobox', { name: '环境' }), '全部环境');
    await waitFor(() => expect(requested?.searchParams.has('environment')).toBe(false));
  });
  it.each(['1h', 'custom'])('keeps %s windows correct during polling and tab return', async (range) => {
    const urls: URL[] = [];
    const clock = vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-09-08T10:00:00Z'));
    const interval = vi.spyOn(window, 'setInterval');
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
    server.use(http.get('/api/v1/apm/services', ({ request }) => {
      urls.push(new URL(request.url));
      return HttpResponse.json({ data: { items: [row], total: 72, page: 2, page_size: 25 } });
    }));
    const view = render(<MemoryRouter initialEntries={[
      `/apm?${period}&range=${range}&page=2&environment=production&service_namespace=trade`,
    ]}><ApmPage /></MemoryRouter>);
    try {
      await screen.findByRole('link', { name: 'orders' });
      await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
      const poll = interval.mock.calls.find(([, delay]) => delay === 30_000)?.[0] as (() => void) | undefined;
      if (range === '1h') {
        expect(urls.at(-1)?.searchParams.get('end')).toBe('2026-09-08T10:00:00.000Z');
        expect(poll).toBeTypeOf('function');
        clock.mockReturnValue(Date.parse('2026-09-08T10:00:30Z'));
        act(() => poll!());
        await waitFor(() => expect(urls.at(-1)?.searchParams.get('end')).toBe('2026-09-08T10:00:30.000Z'));
        await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
        visibility.mockReturnValue('hidden');
        const count = urls.length;
        act(() => poll!());
        expect(urls).toHaveLength(count);
        clock.mockReturnValue(Date.parse('2026-09-08T10:01:00Z'));
        visibility.mockReturnValue('visible');
        fireEvent(document, new Event('visibilitychange'));
        await waitFor(() => expect(urls.at(-1)?.searchParams.get('end')).toBe('2026-09-08T10:01:00.000Z'));
        expect(urls.at(-1)?.searchParams.get('start')).toBe('2026-09-08T09:01:00.000Z');
      } else {
        expect(poll).toBeUndefined();
        fireEvent(document, new Event('visibilitychange'));
        expect(urls.at(-1)?.searchParams.get('end')).toBe('2026-09-07T01:00:00Z');
      }
      expect(urls.at(-1)?.searchParams.get('page')).toBe('2');
      expect(urls.at(-1)?.searchParams.get('environment')).toBe('production');
      expect(urls.at(-1)?.searchParams.get('service_namespace')).toBe('trade');
    } finally {
      view.unmount();
      clock.mockRestore();
      interval.mockRestore();
      visibility.mockRestore();
    }
  });
  it('shows one aggregate service row and both protocol sections without a switch', async () => {
    let listURL: URL | undefined;
    const overviewURLs: URL[] = [];
    const protocols = [
      {
        protocol: 'http',
        rps: 2,
        error_rate: 10,
        p95_ms: 800,
        data_status: 'observed',
      },
      {
        protocol: 'rpc',
        rps: 18,
        error_rate: 0,
        p95_ms: 12,
        data_status: 'observed',
      },
    ];
    server.use(
      http.get('/api/v1/apm/services', ({ request }) => {
        listURL = new URL(request.url);
        return HttpResponse.json({
          data: {
            items: [
              { ...row, protocols },
              {
                ...row,
                identity: { ...row.identity, service_name: 'rpc-only' },
                protocols: [protocols[1]],
              },
            ],
            total: 2,
            page: 1,
            page_size: 25,
          },
        });
      }),
      http.get('/api/v1/apm/overview', ({ request }) => {
        overviewURLs.push(new URL(request.url));
        return HttpResponse.json({ data: { summary: row, points: [] } });
      }),
      http.get('/api/v1/apm/operations', () =>
        HttpResponse.json({
          data: { items: [], total: 0, page: 1, page_size: 5 },
        }),
      ),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({ data: { items: [] } })),
    );
    // Old bookmarks must no longer hide RPC-only services.
    render(
      <MemoryRouter initialEntries={[`/apm?${period}&protocol=http`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'orders' });
    expect(listURL?.searchParams.get('protocol')).toBe('all');
    expect(screen.getAllByRole('link', { name: 'orders' })).toHaveLength(1);
    expect(screen.getByRole('link', { name: 'rpc-only' })).toBeInTheDocument();
    const mixedLatency = within(screen.getByRole('link', { name: 'orders' }).closest('tr')!).getAllByRole('cell')[5];
    expect(mixedLatency).toHaveTextContent('HTTP800RPC12');
    const rpcLatency = within(screen.getByRole('link', { name: 'rpc-only' }).closest('tr')!).getAllByRole('cell')[5];
    expect(rpcLatency).toHaveTextContent('RPC12');
    expect(rpcLatency).not.toHaveTextContent('HTTP');
    expect(screen.queryByRole('columnheader', { name: '协议' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'RPC' })).not.toBeInTheDocument();
    expect(screen.getAllByRole('row')).toHaveLength(3);
    expect(screen.getByText('800')).toBeInTheDocument();
    expect(screen.queryByRole('group', { name: '请求协议' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '更多' })).not.toBeInTheDocument();
    expect(screen.queryByRole('combobox', { name: '排序' })).not.toBeInTheDocument();
    const env = screen.getByRole('combobox', { name: '环境' });
    const namespace = screen.getByLabelText('业务命名空间');
    const search = screen.getByRole('textbox', { name: '搜索服务名称…' });
    expect(env.compareDocumentPosition(namespace) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(
      namespace.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: '按P95 (ms)排序' }));
    await waitFor(() => expect(listURL?.searchParams.get('sort')).toBe('p95_ms'));
    await screen.findByRole('link', { name: 'orders' });
    expect(screen.getByRole('columnheader', { name: 'P95 (ms)' })).toHaveAttribute(
      'aria-sort',
      'descending',
    );
    fireEvent.click(screen.getByRole('link', { name: 'orders' }));
    await waitFor(() =>
      expect(overviewURLs.map((url) => url.searchParams.get('protocol')).sort()).toEqual([
        'http',
        'rpc',
      ]),
    );
    expect(overviewURLs.every((url) => url.searchParams.get('service_name') === 'orders')).toBe(
      true,
    );
    expect(screen.getByRole('region', { name: 'HTTP' })).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'RPC' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'RPC' })).not.toBeInTheDocument();
  });

  it.each(['http', 'rpc'])('hides uncollected %s panels but keeps collected zero-traffic metrics', async (missing) => {
    server.use(
      http.get('/api/v1/apm/overview', ({ request }) => HttpResponse.json({ data: {
        summary: { ...row, rps: 0, data_status: new URL(request.url).searchParams.get('protocol') === missing ? 'no_data' : 'no_requests' }, points: [],
      } })),
      http.get('/api/v1/apm/operations', () => HttpResponse.json({ data: { items: [], total: 0, page: 1, page_size: 5 } })),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({ data: { items: [] } })),
    );
    render(<MemoryRouter initialEntries={[`/apm/service?${period}&service_name=orders`]}><ApmPage /></MemoryRouter>);
    const present = missing === 'http' ? 'RPC' : 'HTTP';
    await within(await screen.findByRole('region', { name: present })).findByText('请求速率');
    await waitFor(() => expect(screen.queryByRole('region', { name: missing.toUpperCase() })).not.toBeInTheDocument());
    expect(within(screen.getByRole('region', { name: present })).getByText('请求速率')).toBeInTheDocument();
  });

  it('debounces search and restores list filters, page and scroll after visiting a service', async () => {
    const urls: URL[] = [];
    server.use(
      http.get('/api/v1/apm/services', ({ request }) => {
        urls.push(new URL(request.url));
        return HttpResponse.json({
          data: { items: [row], total: 80, page: 3, page_size: 25 },
        });
      }),
      http.get('/api/v1/apm/overview', () =>
        HttpResponse.json({ data: { summary: row, points: [] } }),
      ),
      http.get('/api/v1/apm/operations', () =>
        HttpResponse.json({
          data: { items: [], total: 0, page: 1, page_size: 5 },
        }),
      ),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({ data: { items: [] } })),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}&search=ord&sort=p95_ms&page=3`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'orders' });
    const search = screen.getByRole('textbox', { name: '搜索服务名称…' });
    fireEvent.change(search, { target: { value: 'orde' } });
    fireEvent.change(search, { target: { value: 'orders' } });
    expect(urls).toHaveLength(1);
    await waitFor(() => expect(urls).toHaveLength(2));
    expect(urls[1].searchParams.get('search')).toBe('orders');
    expect(urls[1].searchParams.has('page')).toBe(false);
    await screen.findByRole('link', { name: 'orders' });
    const main = screen.getByRole('main');
    main.scrollTop = 230;
    fireEvent.click(screen.getByRole('link', { name: 'orders' }));
    await screen.findByText('服务指标');
    const back = screen.getByRole('link', { name: '← 服务列表' });
    const restored = new URL(back.getAttribute('href')!, 'http://localhost');
    expect(restored.searchParams.get('sort')).toBe('p95_ms');
    expect(restored.searchParams.get('search')).toBe('orders');
    expect(restored.searchParams.has('environment')).toBe(false);
    expect(urls.every((url) => !url.searchParams.has('list_query'))).toBe(true);
    main.scrollTop = 0;
    fireEvent.click(back);
    await screen.findByRole('link', { name: 'orders' });
    await waitFor(() => expect(main.scrollTop).toBe(230));
    expect(screen.getByRole('textbox', { name: '搜索服务名称…' })).toHaveValue('orders');
  });
  it('retains rows during a refresh failure and immediately clears them when the time scope changes', async () => {
    let resolve!: () => void;
    let pending = false;
    const gate = new Promise<void>((done) => {
      resolve = done;
    });
    server.use(
      http.get('/api/v1/apm/services', async () => {
        if (pending) {
          await gate;
          return HttpResponse.json({ message: 'temporarily unavailable' }, { status: 502 });
        }
        return HttpResponse.json({
          data: { items: [row], total: 1, page: 1, page_size: 25 },
        });
      }),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'orders' });
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
    pending = true;
    fireEvent.click(screen.getByRole('button', { name: '刷新' }));
    expect(screen.getByRole('link', { name: 'orders' })).toBeInTheDocument();
    await act(async () => resolve());
    expect(await screen.findByRole('alert')).toHaveTextContent('保留上次结果');
    expect(screen.getByRole('link', { name: 'orders' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
    fireEvent.click(screen.getByRole('button', { name: '最近 15 分钟' }));
    expect(screen.queryByRole('link', { name: 'orders' })).not.toBeInTheDocument();
  });
  it('opens scoped operation metrics and restores the operation query and pagination', async () => {
    let overviewURL: URL | undefined;
    server.use(
      http.get('/api/v1/apm/operations', () =>
        HttpResponse.json({
          data: {
            items: [{ ...row, operation: 'POST /orders' }],
            total: 80,
            page: 3,
            page_size: 25,
          },
        }),
      ),
      http.get('/api/v1/apm/overview', ({ request }) => {
        overviewURL = new URL(request.url);
        return HttpResponse.json({ data: { summary: row, points: [] } });
      }),
    );
    const query = `${period}&service_name=orders&environment=production&service_namespace=trade&tab=operations&search=POST&sort=p95_ms&page=3`;
    render(
      <MemoryRouter initialEntries={[`/apm/service?${query}`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    fireEvent.click(
      await within(screen.getByRole('region', { name: 'HTTP' })).findByRole('link', {
        name: 'POST /orders',
      }),
    );
    await waitFor(() => expect(overviewURL?.searchParams.get('operation')).toBe('POST /orders'));
    expect(overviewURL?.searchParams.get('environment')).toBe('production');
    expect(overviewURL?.searchParams.get('start')).toBe('2026-09-07T00:00:00Z');
    expect(screen.getByRole('heading', { name: 'POST /orders' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '查看链路' })).toHaveAttribute(
      'href',
      expect.stringContaining('/traces?'),
    );
    fireEvent.click(screen.getByRole('link', { name: '← 全部接口' }));
    const httpPanel = within(screen.getByRole('region', { name: 'HTTP' }));
    await httpPanel.findByRole('link', { name: 'POST /orders' });
    expect(screen.getByRole('textbox', { name: '搜索接口…' })).toHaveValue('POST');
    expect(httpPanel.getByRole('columnheader', { name: 'P95 (ms)' })).toHaveAttribute(
      'aria-sort',
      'descending',
    );
  });
  it('switches same-name services by their full identity while keeping the time and showing all protocols', async () => {
    server.use(
      http.get('/api/v1/apm/overview', () =>
        HttpResponse.json({ data: { summary: row, points: [] } }),
      ),
      http.get('/api/v1/apm/operations', () =>
        HttpResponse.json({
          data: { items: [], total: 0, page: 1, page_size: 5 },
        }),
      ),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({ data: { items: [] } })),
      http.get('/api/v1/apm/services', () =>
        HttpResponse.json({
          data: {
            items: [
              row,
              {
                ...row,
                identity: {
                  ...row.identity,
                  environment: 'staging',
                  service_namespace: '',
                },
                protocols: [
                  {
                    protocol: 'http',
                    rps: 2,
                    error_rate: 0,
                    p95_ms: 200,
                    data_status: 'observed',
                  },
                ],
              },
            ],
            total: 2,
            page: 1,
            page_size: 25,
          },
        }),
      ),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=production&service_namespace=trade&protocol=rpc`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('button', { name: '切换服务' }));
    const dialog = screen.getByRole('dialog', { name: '选择服务' });
    const link = await within(dialog).findByRole('link', {
      name: 'orders staging / 未设置',
    });
    const next = new URL(link.getAttribute('href')!, 'http://localhost');
    expect(next.searchParams.get('environment')).toBe('staging');
    expect(next.searchParams.get('service_namespace')).toBe('');
    expect(next.searchParams.get('protocol')).toBe('all');
    expect(next.searchParams.get('end')).toBe('2026-09-07T01:00:00Z');
    fireEvent.click(link);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(screen.getByText('staging / 未设置')).toBeInTheDocument();
  });
  it('paginates and sorts protocol operations independently without losing scroll or identity', async () => {
    const latest = new Map<string, URL>();
    server.use(
      http.get('/api/v1/apm/operations', ({ request }) => {
        const url = new URL(request.url);
        const protocol = url.searchParams.get('protocol')!;
        latest.set(protocol, url);
        return HttpResponse.json({
          data: {
            items: [{ ...row, operation: protocol === 'rpc' ? 'trade.Orders/Get' : 'GET /orders' }],
            total: 80,
            page: Number(url.searchParams.get('page')),
            page_size: 25,
          },
        });
      }),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=production&service_namespace=trade&tab=operations`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    const httpPanel = () => within(screen.getByRole('region', { name: 'HTTP' }));
    const rpcPanel = () => within(screen.getByRole('region', { name: 'RPC' }));
    await rpcPanel().findByRole('link', { name: 'trade.Orders/Get' });
    const main = screen.getByRole('main');
    main.scrollTop = 240;
    fireEvent.click(rpcPanel().getByRole('button', { name: '下一页' }));
    await waitFor(() => expect(latest.get('rpc')?.searchParams.get('page')).toBe('2'));
    await rpcPanel().findByRole('link', { name: 'trade.Orders/Get' });
    expect(latest.get('http')?.searchParams.get('page')).toBe('1');
    await waitFor(() => expect(main.scrollTop).toBe(240));
    fireEvent.click(httpPanel().getByRole('button', { name: '按错误率排序' }));
    await waitFor(() => expect(latest.get('http')?.searchParams.get('sort')).toBe('error_rate'));
    await rpcPanel().findByRole('link', { name: 'trade.Orders/Get' });
    expect(latest.get('rpc')?.searchParams.get('sort')).toBe('rps');
    expect(latest.get('rpc')?.searchParams.get('page')).toBe('2');
    const target = new URL(
      rpcPanel().getByRole('link', { name: 'trade.Orders/Get' }).getAttribute('href')!,
      'http://localhost',
    );
    expect(target.searchParams.get('protocol')).toBe('rpc');
    expect(target.searchParams.get('operation')).toBe('trade.Orders/Get');
    expect(target.searchParams.get('environment')).toBe('production');
    expect(target.searchParams.get('start')).toBe('2026-09-07T00:00:00Z');
  });
  it('keeps successful protocol metrics visible when the other protocol query fails', async () => {
    server.use(
      http.get('/api/v1/apm/overview', ({ request }) =>
        new URL(request.url).searchParams.get('protocol') === 'rpc'
          ? HttpResponse.json({ message: 'RPC metrics unavailable' }, { status: 502 })
          : HttpResponse.json({ data: { summary: { ...row, p95_ms: 800 }, points: [] } }),
      ),
      http.get('/api/v1/apm/operations', () =>
        HttpResponse.json({ data: { items: [], total: 0, page: 1, page_size: 5 } }),
      ),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({ data: { items: [] } })),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=production&service_namespace=trade`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('alert')).toHaveTextContent('RPC: RPC metrics unavailable');
    expect(
      within(screen.getByRole('region', { name: 'HTTP' })).getByText('800'),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole('region', { name: 'RPC' })).queryByText('800'),
    ).not.toBeInTheDocument();
    const traceURL = new URL(
      screen.getByRole('link', { name: '查看链路' }).getAttribute('href')!,
      'http://localhost',
    );
    expect(traceURL.searchParams.get('q')).not.toContain('span.http');
    expect(traceURL.searchParams.get('q')).not.toContain('span.rpc');
  });
  it('keeps instance queries independent and checks both protocols in onboarding', async () => {
    const protocols = new Set<string>();
    const shared = {
      instance_id: 'shared-instance',
      device_id: '',
      cluster_id: '',
      pod: '',
      version: '',
    };
    server.use(
      http.get('/api/v1/apm/diagnostics', ({ request }) => {
        const protocol = new URL(request.url).searchParams.get('protocol')!;
        protocols.add(protocol);
        return HttpResponse.json({
          data: {
            checks: [{ key: 'metrics', status: 'observed', detail: '' }],
            instances:
              protocol === 'rpc' ? [shared, { ...shared, instance_id: 'rpc-instance' }] : [shared],
            trace_ids: [],
            sampled_traces: 0,
          },
        });
      }),
      http.get('/api/v1/apm/runtime', () => HttpResponse.json({ data: { items: [], instances: [shared, shared, {...shared, instance_id: 'rpc-instance'}] } })),
    );
    render(
      <MemoryRouter
        initialEntries={[
          `/apm/service?${period}&service_name=orders&environment=production&service_namespace=trade&tab=instances`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByText(/rpc-instance/);
    expect(screen.getByRole('heading', { name: '观测到的实例' })).toBeInTheDocument();
    expect(screen.getAllByText(/shared-instance/)).toHaveLength(1);
    expect([...protocols]).toEqual([]);
    fireEvent.click(screen.getByRole('button', { name: '接入管理' }));
    expect(await screen.findByRole('heading', { name: '接入诊断' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '应用接入' })).not.toBeInTheDocument();
    await waitFor(() => expect([...protocols].sort()).toEqual(['http', 'rpc']));
    fireEvent.click(screen.getByRole('tab', { name: '接入指南' }));
    expect(await screen.findByRole('heading', { name: '应用接入' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '接入诊断' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', { name: '接入诊断' }));
    expect(await screen.findByRole('heading', { name: '接入诊断' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '应用接入' })).not.toBeInTheDocument();
  });
});


describe('Official language onboarding', () => {
  beforeEach(() => server.use(http.get('/api/v1/devices', () => HttpResponse.json({ items: [] })), http.get('/api/v1/topology/nodes', () => HttpResponse.json({ items: [] })), http.get('/api/v1/apm/repository-binding', () => HttpResponse.json({ data: null })), http.get('/api/v1/traces/search', () => HttpResponse.json({ traces: [] }))));
  it('provides nine languages and states metrics boundaries', async () => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    render(<Onboarding />);
    for (const [label, command] of [
      ['Java', 'java -javaagent:'], ['Node.js', 'node --require'], ['Python', 'opentelemetry-instrument'],
      ['Go', 'official OTel Go SDK'], ['C# / .NET', 'dotnet App.dll'], ['PHP', 'php app.php'],
      ['C++', './app'], ['Rust', './target/release/ongrid-apm-rust-example'], ['Ruby', 'bundle exec ruby app.rb'],
    ]) {
      await selectOption(screen.getByRole('combobox', { name: '语言' }), label);
      expect(document.querySelector('pre')).toHaveTextContent(command);
      expect(document.querySelector('pre')).toHaveTextContent('service.namespace=trade,deployment.environment.name=production');
    }
    expect(document.querySelector('pre')).toHaveTextContent('OTEL_METRICS_EXPORTER=none');
    expect(screen.getByText(/官方指标 SDK 尚未稳定/)).toBeInTheDocument();
    await selectOption(screen.getByRole('combobox', { name: '语言' }), 'PHP');
    expect(screen.getByText(/长驻 worker/)).toBeInTheDocument();
  });
  it('keeps resource and request queries scoped to the chosen version and instance', async () => {
    const requests: URL[] = [];
    const instances = ['1.0.0', '1.1.0-demo'].map((version, index) => ({instance_id: `pod-${index + 1}`, version, device_id: '', cluster_id: '', pod: ''}));
    server.use(
      http.get('/api/v1/apm/runtime', ({request}) => {
        const url = new URL(request.url); requests.push(url);
        return HttpResponse.json({data: {instances, items: instances
          .filter((item) => !url.searchParams.get('service_version') || item.version === url.searchParams.get('service_version'))
          .flatMap((item) => [
            {...item, name: 'process_cpu_cores', unit: 'cores', value: 0.02, points: []},
            {...item, name: 'go_memstats_heap_alloc_bytes', unit: 'bytes', value: 1048576, points: []},
            {...item, name: 'go_goroutines', unit: 'count', value: 17, points: []},
          ])}});
      }),
      http.get('/api/v1/apm/diagnostics', () => HttpResponse.json({data: {checks: [], instances: [], trace_ids: []}})),
      http.get('/api/v1/apm/overview', ({request}) => {requests.push(new URL(request.url)); return HttpResponse.json({data: {summary: row, points: []}});}),
      http.get('/api/v1/apm/operations', ({request}) => {requests.push(new URL(request.url)); return HttpResponse.json({data: {items: [], total: 0, page: 1, page_size: 5}});}),
      http.get('/api/v1/apm/dependencies', () => HttpResponse.json({data: {items: []}})),
    );
    render(<MemoryRouter initialEntries={[`/apm?${period}&service_name=orders&environment=production&service_namespace=trade&tab=instances`]}><ApmPage /></MemoryRouter>);
    await screen.findByRole('button', {name: 'pod-2'});
    expect(screen.getByRole('region', {name: 'Go 堆内存'})).toBeInTheDocument();
    expect(screen.getByRole('region', {name: 'Goroutines'})).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', {name: 'pod-2'}));
    expect(screen.getByRole('tab', {name: '实例'})).toHaveAttribute('aria-selected', 'true');
    await selectOption(screen.getByRole('combobox', {name: '版本'}), '1.1.0-demo');
    await waitFor(() => expect(requests.some((url) => url.pathname.endsWith('/runtime') && url.searchParams.get('service_version') === '1.1.0-demo')).toBe(true));
    await selectOption(screen.getByRole('combobox', {name: '实例'}), 'pod-2');
    await waitFor(() => expect(requests.some((url) => url.searchParams.get('instance_id') === 'pod-2')).toBe(true));
    expect(screen.queryByRole('button', {name: 'pod-1'})).not.toBeInTheDocument();
    expect(screen.getByRole('button', {name: 'pod-2'})).toBeInTheDocument();
    fireEvent.click(screen.getByRole('tab', {name: '概览'}));
    await waitFor(() => expect(requests.some((url) => url.pathname.endsWith('/overview') && url.searchParams.get('service_version') === '1.1.0-demo' && url.searchParams.get('instance_id') === 'pod-2')).toBe(true));
    expect(screen.queryByRole('heading', {name: '实例资源与运行时'})).not.toBeInTheDocument();
    const traces = new URL(screen.getByRole('link', {name: '查看链路'}).getAttribute('href')!, 'https://ongrid.test');
    expect(traces.searchParams.get('q')).toContain('resource.service.version = "1.1.0-demo"');
    expect(traces.searchParams.get('q')).toContain('resource.service.instance.id = "pod-2"');
  });

});
