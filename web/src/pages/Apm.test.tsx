import userEvent from '@testing-library/user-event';
import { selectOption } from '@/test/select-option';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { server } from '@/test/msw-server';
import ApmPage from './Apm';

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
  beforeEach(() => localStorage.setItem('ongrid-locale', 'zh-CN'));
  it('keeps same-name services separate and links their complete identity', async () => {
    server.use(
      http.get('/api/v1/apm/services', () =>
        HttpResponse.json({
          data: {
            items: [
              row,
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
          `/apm/service?${period}&service_name=orders&environment=&service_namespace=trade&tab=operations&metric_source=tempo_spanmetrics`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'consume' });
    fireEvent.click(screen.getByRole('button', { name: '接入管理' }));
    await selectOption(screen.getByLabelText('入口类型'), '消息消费');
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
  it('uses server facets and moves relative windows forward only on refresh', async () => {
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
    await selectOption(screen.getByLabelText('时间范围'), '最近 15 分钟');
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
    fireEvent.click(screen.getByRole('button', { name: '按最高 P95 (ms)排序' }));
    await waitFor(() => expect(listURL?.searchParams.get('sort')).toBe('p95_ms'));
    await screen.findByRole('link', { name: 'orders' });
    expect(screen.getByRole('columnheader', { name: '最高 P95 (ms)' })).toHaveAttribute(
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
    await selectOption(screen.getByLabelText('时间范围'), '最近 15 分钟');
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
  it('shows instances without sampled traces and deduplicates both protocols', async () => {
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
      http.get('/api/v1/apm/runtime', () => HttpResponse.json({ data: { items: [] } })),
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
    expect([...protocols].sort()).toEqual(['http', 'rpc']);
    fireEvent.click(screen.getByRole('button', { name: '接入管理' }));
    expect(await screen.findByRole('heading', { name: 'HTTP · 接入诊断' })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'RPC · 接入诊断' })).toBeInTheDocument();
  });
});
