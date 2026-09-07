import { fireEvent, render, screen, waitFor } from '@testing-library/react';
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
          `/apm/service?${period}&service_name=orders&environment=&service_namespace=trade&tab=operations`,
        ]}
      >
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'consume' });
    fireEvent.click(screen.getByRole('button', { name: '高级筛选' }));
    fireEvent.change(screen.getByLabelText('入口类型'), {
      target: { value: 'consumer' },
    });
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
    const operation = await screen.findByRole('link', { name: 'POST /orders' });
    expect(await screen.findByRole('alert')).toHaveTextContent('dependency timeout');
    expect(screen.getByText('P95 延迟')).toBeInTheDocument();
    expect(screen.getAllByText('200')).toHaveLength(2);
    fireEvent.change(screen.getByLabelText('延迟分位数'), { target: { value: 'p99_ms' } });
    expect(screen.getByText('220')).toBeInTheDocument();
    expect(urls.every((url) => url.searchParams.get('service_namespace') === 'trade')).toBe(true);
    expect(
      urls.find((url) => url.pathname.endsWith('/operations'))?.searchParams.get('page_size'),
    ).toBe('5');
    const linked = new URL(operation.getAttribute('href')!, 'http://localhost');
    expect(linked.searchParams.get('q')).toContain('name = "POST /orders"');
    expect(linked.searchParams.get('start')).toBe('2026-09-07T00:00:00Z');
    expect(linked.searchParams.get('environment')).toBe('production');
    expect(screen.queryByRole('link', { name: '运行时指标' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: '实例' })).toBeInTheDocument();
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
            environments: ['production', 'staging'],
            service_namespaces: ['trade'],
          },
        });
      }),
    );
    render(
      <MemoryRouter initialEntries={[`/apm?${period}`]}>
        <ApmPage />
      </MemoryRouter>,
    );
    await screen.findByRole('link', { name: 'orders' });
    expect(screen.getByText('服务 · 72')).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'staging' })).toBeInTheDocument();
    expect(screen.queryByText('估算样本请求数')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('环境'), {
      target: { value: JSON.stringify('') },
    });
    await waitFor(() => expect(requested?.searchParams.get('environment')).toBe(''));
    fireEvent.change(screen.getByLabelText('时间范围'), {
      target: { value: '15m' },
    });
    await waitFor(() => expect(requested?.searchParams.get('range')).toBe('15m'));
    const oldEnd = requested!.searchParams.get('end')!;
    expect(Date.parse(oldEnd) - Date.parse(requested!.searchParams.get('start')!)).toBe(900000);
    await waitFor(() => expect(screen.getByRole('button', { name: '刷新' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: '刷新' }));
    await waitFor(() => expect(requested!.searchParams.get('end')).not.toBe(oldEnd));
    expect(requested?.searchParams.get('environment')).toBe('');
  });
});
