import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import LogsPage from './Logs';
import { server } from '@/test/msw-server';

class MockPointerEvent extends MouseEvent {
  pointerId: number;

  constructor(type: string, init: PointerEventInit = {}) {
    super(type, init);
    this.pointerId = init.pointerId ?? 0;
  }
}

Object.defineProperty(window, 'PointerEvent', { configurable: true, value: MockPointerEvent });

vi.mock('recharts', () => ({
  ResponsiveContainer: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  BarChart: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
  Bar: () => null,
  CartesianGrid: () => null,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: () => null,
}));

const records = [
  {
    id: 'log-1',
    timestamp: '2026-08-19T01:00:00.000Z',
    message: 'payment request completed',
    severity_text: 'INFO',
    backend: 'elasticsearch',
    attributes: {
      ongrid_source: 'k8s',
      severity_text: 'INFO',
      severity_number: '9',
      service_name: 'unknown_service',
      service_name_extracted: 'payments',
      attributes_device_id: '42',
      attributes_ongrid_source: 'k8s',
      'k8s.pod.name': 'payments-7d4',
      'service.name': 'payments',
      comp: 'reconciler',
      'k8s.container.restart_count': 2,
      'k8s.pod.uid': 'internal-pod-uid',
      'ongrid.backend': 'elasticsearch',
      'ongrid.backend_generation': 3,
      k8s_container_name: 'payments',
      k8s_container_restart_count: 2,
      k8s_namespace_name: 'production',
      k8s_pod_uid: 'internal-pod-uid',
      log_file_path: '/var/log/containers/payments.log',
      ongrid_backend: 'elasticsearch',
      ongrid_backend_generation: 3,
    },
    resource_attributes: { device_id: '42', cluster_id: '7', cluster_name: 'kind-local', level: 'INFO', 'k8s.namespace.name': 'production' },
  },
  {
    id: 'log-2',
    timestamp: '2026-08-19T01:01:00.000Z',
    message: 'upstream timeout while calling inventory',
    severity_text: 'ERROR',
    backend: 'elasticsearch',
    attributes: { ongrid_source: 'k8s', 'k8s.pod.name': 'gateway-22b', 'service.name': 'gateway' },
    resource_attributes: { device_id: '42', cluster_id: '7', cluster_name: 'kind-local', level: 'ERROR', 'k8s.namespace.name': 'production' },
  },
];

type CapturedSearchRequest = {
  start: string;
  end: string;
  cursor?: string;
  scope?: { cluster_ids?: string[]; levels?: string[] };
};

const searchRequests: CapturedSearchRequest[] = [];

const logFields = [
  { name: 'device_id', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'cluster_id', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'namespace', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'workload', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'pod', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'container', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'service_name', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'service_name_extracted', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'source_id', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'level', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'trace_id', type: 'keyword', searchable: true, aggregatable: false },
  { name: 'k8s.container.restart_count', type: 'long', searchable: true, aggregatable: true },
  { name: 'k8s.pod.uid', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'ongrid.backend', type: 'keyword', searchable: true, aggregatable: true },
  { name: 'ongrid.backend_generation', type: 'long', searchable: true, aggregatable: true },
  { name: 'message', type: 'text', searchable: true, aggregatable: false },
];

async function waitForInitialLogs() {
  await screen.findByText('payment request completed');
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
}

describe('LogsPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    searchRequests.length = 0;
    server.use(
      http.get('/api/v1/edges', () => HttpResponse.json({
        items: [{
          id: 3,
          name: 'edge-42',
          device_name: 'checkout-host',
          status: 'online',
          roles: ['server'],
          access_key_id: 'test-key',
          last_seen_at: null,
          device_id: 42,
        }],
        total: 1,
      })),
      http.get('/api/v1/topology/nodes', () => HttpResponse.json({
        items: [
          { id: 7, type: 'cluster', name: 'kind-local', props: null, created_at: '', updated_at: '' },
          { id: 12, type: 'cluster', name: 'production', props: null, created_at: '', updated_at: '' },
        ],
        total: 2,
      })),
      http.get('/api/v1/logs/fields', () => HttpResponse.json({ code: 0, message: '', data: logFields })),
      http.post('/api/v1/logs/field-values', () => HttpResponse.json({ code: 0, message: '', data: [] })),
      http.post('/api/v1/logs/search', async ({ request }) => {
        searchRequests.push(await request.json() as CapturedSearchRequest);
        return HttpResponse.json({
          code: 0,
          message: '',
          data: { records, has_more: false, took_ms: 27, backends: ['elasticsearch'] },
        });
      }),
      http.post('/api/v1/logs/histogram', () => HttpResponse.json({
        code: 0,
        message: '',
        data: [
          { start: '2026-08-19T01:00:00.000Z', count: 2 },
          { start: '2026-08-19T01:01:00.000Z', count: 3 },
        ],
      })),
    );
  });

  it('renders the dense search workspace and exact result summary', async () => {
    render(<MemoryRouter><LogsPage /></MemoryRouter>);

    await waitForInitialLogs();
    expect(screen.getByText('payment request completed')).toBeInTheDocument();
    expect(screen.getByText('payment request completed').closest('[role="listitem"]')).toHaveTextContent('设备:checkout-host (#42)');
    expect(screen.getByText('payment request completed').closest('[role="listitem"]')).not.toHaveTextContent('unknown_service');
    expect(screen.getAllByText('elasticsearch')).toHaveLength(1);
    expect(screen.getAllByText('kind-local (#7)')).not.toHaveLength(0);
    expect(screen.getByRole('checkbox', { name: /级别.*level/i })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /集群.*cluster_id/i })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /comp/i })).not.toBeChecked();
    expect(screen.queryByRole('checkbox', { name: /Workload.*workload/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /后端.*backend/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /服务.*service_name/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /severity_text/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /severity_number/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /attributes_device_id/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /attributes_ongrid_source/i })).not.toBeInTheDocument();
    expect(screen.queryByText('k8s.container.restart_count')).not.toBeInTheDocument();
    expect(screen.queryByText('k8s.pod.uid')).not.toBeInTheDocument();
    expect(screen.queryByText('ongrid.backend')).not.toBeInTheDocument();
    expect(screen.queryByText('ongrid.backend_generation')).not.toBeInTheDocument();
    expect(screen.queryByText('k8s_container_name')).not.toBeInTheDocument();
    expect(screen.queryByText('k8s_container_restart_count')).not.toBeInTheDocument();
    expect(screen.queryByText('k8s_namespace_name')).not.toBeInTheDocument();
    expect(screen.queryByText('k8s_pod_uid')).not.toBeInTheDocument();
    expect(screen.queryByText('log_file_path')).not.toBeInTheDocument();
    expect(screen.queryByText('ongrid_backend')).not.toBeInTheDocument();
    expect(screen.queryByText('ongrid_backend_generation')).not.toBeInTheDocument();
    expect(screen.getByText('日志总数', { exact: false })).toHaveTextContent('5');
    expect(screen.getByText('已加载', { exact: false })).toHaveTextContent('2');
    expect(screen.getByText('耗时', { exact: false })).toHaveTextContent('27 ms');
    expect(screen.getByRole('group', { name: /日志时间直方图/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /原始日志/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /表格/ })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /采集与后端配置/ })).toHaveAttribute('href', '/settings/integrations?focus=logs');
    expect(screen.queryByText('日志检索')).not.toBeInTheDocument();
    expect(screen.queryByText('采集配置')).not.toBeInTheDocument();
  });

  it('resolves a cluster name from topology when logs contain only cluster_id', async () => {
    const recordsWithoutClusterName = records.map((record) => ({
      ...record,
      resource_attributes: { ...record.resource_attributes, cluster_name: undefined },
    }));
    server.use(http.post('/api/v1/logs/search', () => HttpResponse.json({
      code: 0,
      message: '',
      data: { records: recordsWithoutClusterName, has_more: false, took_ms: 27, backends: ['elasticsearch'] },
    })));

    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    const rawRow = screen.getByText('payment request completed').closest('[role="listitem"]');
    await waitFor(() => expect(rawRow).toHaveTextContent('集群:kind-local (#7)'));

    await user.click(screen.getByRole('button', { name: /表格/ }));
    expect(screen.getByText('payment request completed').closest('tr')).toHaveTextContent('kind-local (#7)');
  });

  it('uses fields API metadata when the current query has no records', async () => {
    server.use(http.get('/api/v1/logs/fields', () => HttpResponse.json({
      code: 0,
      message: '',
      data: [
        { name: 'cluster_id', type: 'keyword', searchable: true, aggregatable: true },
        { name: 'service_name_extracted', type: 'keyword', searchable: true, aggregatable: true },
        { name: 'trace_id', type: 'keyword', searchable: true, aggregatable: false },
        { name: 'message', type: 'text', searchable: true, aggregatable: false },
      ],
    })), http.post('/api/v1/logs/search', () => HttpResponse.json({
      code: 0,
      message: '',
      data: { records: [], has_more: false, took_ms: 3, backends: ['elasticsearch'] },
    })));

    render(<MemoryRouter><LogsPage /></MemoryRouter>);

    const clusterField = await screen.findByRole('checkbox', { name: /集群.*cluster_id/i });
    await waitFor(() => expect(clusterField).toBeChecked());
    expect(screen.getByRole('checkbox', { name: /Trace ID.*trace_id/i })).not.toBeChecked();
    expect(screen.queryByRole('checkbox', { name: /设备.*device_id/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /service_name_extracted/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /message/i })).not.toBeInTheDocument();
  });

  it('does not flash API-only fields before the initial search settles', async () => {
    let fieldsCompleted = false;
    let resolveSearch!: () => void;
    const searchGate = new Promise<void>((resolve) => { resolveSearch = resolve; });
    server.use(
      http.get('/api/v1/logs/fields', () => {
        fieldsCompleted = true;
        return HttpResponse.json({ code: 0, message: '', data: logFields });
      }),
      http.post('/api/v1/logs/search', async () => {
        await searchGate;
        return HttpResponse.json({
          code: 0,
          message: '',
          data: { records, has_more: false, took_ms: 27, backends: ['elasticsearch'] },
        });
      }),
    );

    render(<MemoryRouter><LogsPage /></MemoryRouter>);

    await waitFor(() => expect(fieldsCompleted).toBe(true));
    expect(screen.queryByRole('checkbox', { name: /Workload.*workload/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /Trace ID.*trace_id/i })).not.toBeInTheDocument();

    resolveSearch();
    await waitForInitialLogs();
    expect(screen.queryByRole('checkbox', { name: /Workload.*workload/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /Trace ID.*trace_id/i })).not.toBeInTheDocument();
  });

  it('does not restart the initial log queries when the device catalog finishes loading without a device filter', async () => {
    let resolveEdges!: () => void;
    const edgesGate = new Promise<void>((resolve) => { resolveEdges = resolve; });
    let histogramRequests = 0;
    server.use(
      http.get('/api/v1/edges', async () => {
        await edgesGate;
        return HttpResponse.json({
          items: [{
            id: 3,
            name: 'edge-42',
            device_name: 'checkout-host',
            status: 'online',
            roles: ['server'],
            access_key_id: 'test-key',
            last_seen_at: null,
            device_id: 42,
          }],
          total: 1,
        });
      }),
      http.post('/api/v1/logs/histogram', () => {
        histogramRequests++;
        return HttpResponse.json({ code: 0, message: '', data: [] });
      }),
    );

    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();
    expect(searchRequests).toHaveLength(1);
    expect(histogramRequests).toBe(1);

    resolveEdges();
    const rawRow = screen.getByText('payment request completed').closest('[role="listitem"]');
    await waitFor(() => expect(rawRow).toHaveTextContent('设备:checkout-host (#42)'));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });

    expect(searchRequests).toHaveLength(1);
    expect(histogramRequests).toBe(1);
  });

  it('loads advanced facet values on demand with bounded concurrency', async () => {
    const user = userEvent.setup();
    let started = 0;
    let completed = 0;
    let active = 0;
    let maxActive = 0;
    server.use(http.post('/api/v1/logs/field-values', async () => {
      started++;
      active++;
      maxActive = Math.max(maxActive, active);
      await new Promise((resolve) => setTimeout(resolve, 20));
      active--;
      completed++;
      return HttpResponse.json({ code: 0, message: '', data: [] });
    }));

    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
    expect(started).toBe(0);

    await user.click(screen.getByRole('button', { name: /更多筛选/ }));

    await waitFor(() => expect(completed).toBe(2));
    expect(started).toBe(2);
    expect(maxActive).toBeLessThanOrEqual(2);
  });

  it('aborts stale facet requests when advanced filters are closed', async () => {
    const user = userEvent.setup();
    let started = 0;
    let aborted = 0;
    server.use(http.post('/api/v1/logs/field-values', async ({ request }) => {
      started++;
      await new Promise<void>((resolve) => {
        const onAbort = () => {
          aborted++;
          resolve();
        };
        if (request.signal.aborted) onAbort();
        else request.signal.addEventListener('abort', onAbort, { once: true });
      });
      return HttpResponse.json({ code: 0, message: '', data: [] });
    }));

    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();
    const advancedToggle = screen.getByRole('button', { name: /更多筛选/ });
    await user.click(advancedToggle);
    await waitFor(() => expect(started).toBe(2));

    await user.click(advancedToggle);

    await waitFor(() => expect(aborted).toBe(2));
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(started).toBe(2);
  });

  it('lists registered clusters and refreshes immediately with the selected cluster scope', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    const clusterSelect = screen.getByRole('combobox', { name: '集群' });
    await waitFor(() => expect(within(clusterSelect).getByRole('option', { name: 'kind-local (#7)' })).toBeInTheDocument());
    expect(within(clusterSelect).getByRole('option', { name: 'production (#12)' })).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'Namespace' })).not.toBeInTheDocument();

    const requestCount = searchRequests.length;
    await user.selectOptions(clusterSelect, '12');

    await waitFor(() => expect(searchRequests.length).toBeGreaterThan(requestCount));
    expect(searchRequests.at(-1)?.scope?.cluster_ids).toEqual(['12']);
  });

  it('aborts a superseded first-page search before applying a new cluster scope', async () => {
    const user = userEvent.setup();
    let abortedCount = 0;
    server.use(http.post('/api/v1/logs/search', async ({ request }) => {
      const input = await request.clone().json() as CapturedSearchRequest;
      searchRequests.push(input);
      if (input.scope?.cluster_ids?.[0] !== '12') {
        await new Promise<void>((resolve) => {
          const onAbort = () => {
            abortedCount++;
            resolve();
          };
          if (request.signal.aborted) onAbort();
          else request.signal.addEventListener('abort', onAbort, { once: true });
        });
      }
      return HttpResponse.json({
        code: 0,
        message: '',
        data: { records, has_more: false, took_ms: 27, backends: ['elasticsearch'] },
      });
    }));

    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    const clusterSelect = screen.getByRole('combobox', { name: '集群' });
    await waitFor(() => expect(within(clusterSelect).getByRole('option', { name: 'production (#12)' })).toBeInTheDocument());
    await waitFor(() => expect(searchRequests.length).toBeGreaterThan(0));
    const abortedBeforeSelection = abortedCount;

    await user.selectOptions(clusterSelect, '12');

    await waitFor(() => expect(abortedCount).toBeGreaterThan(abortedBeforeSelection));
    await screen.findByText('payment request completed');
    expect(searchRequests.at(-1)?.scope?.cluster_ids).toEqual(['12']);
  });

  it('sends level rather than severity in advanced filters', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    await user.click(screen.getByRole('button', { name: /更多筛选/ }));
    expect(screen.queryByRole('combobox', { name: 'Service' })).not.toBeInTheDocument();
    await user.type(screen.getByRole('textbox', { name: '级别' }), 'ERROR');
    await user.click(screen.getByRole('button', { name: '搜索' }));

    await waitFor(() => expect(searchRequests.at(-1)?.scope?.levels).toEqual(['ERROR']));
  });

  it('keeps raw and table log text directly selectable without context interaction', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    const rawMessage = screen.getByText('upstream timeout while calling inventory');
    const rawRow = rawMessage.closest('[role="listitem"]');
    expect(rawRow).toHaveClass('select-text', 'cursor-text');
    expect(rawMessage.closest('button')).toBeNull();
    const selection = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(rawMessage);
    selection?.removeAllRanges();
    selection?.addRange(range);
    expect(selection?.toString()).toBe('upstream timeout while calling inventory');

    await user.click(screen.getByRole('button', { name: /表格/ }));
    expect(screen.getByRole('columnheader', { name: '日志正文' })).toBeInTheDocument();
    const row = screen.getByText('upstream timeout while calling inventory').closest('tr');
    expect(row).not.toBeNull();
    expect(row).toHaveTextContent('checkout-host (#42)');
    expect(row).toHaveClass('select-text', 'cursor-text');
    expect(row).not.toHaveAttribute('role', 'button');
    expect(row).not.toHaveAttribute('tabindex');
    expect(screen.queryByText('上下文日志')).not.toBeInTheDocument();
  });

  it('keeps unwrapped raw and table logs horizontally scrollable', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    const wrap = screen.getByRole('button', { name: /换行/ });
    const dense = screen.getByRole('button', { name: /紧凑/ });
    expect(wrap).toHaveAttribute('aria-pressed', 'true');
    expect(dense).toHaveAttribute('aria-pressed', 'false');
    await user.click(wrap);
    await user.click(dense);
    expect(wrap).toHaveAttribute('aria-pressed', 'false');
    expect(dense).toHaveAttribute('aria-pressed', 'true');

    const rawMessage = screen.getByText('upstream timeout while calling inventory');
    const rawRow = rawMessage.closest('[role="listitem"]');
    expect(rawMessage.parentElement).toHaveClass('whitespace-nowrap');
    expect(rawMessage.parentElement).not.toHaveClass('truncate');
    expect(rawRow).toHaveClass('w-max', 'min-w-full');
    expect(rawRow?.parentElement).toHaveClass('overflow-x-auto');

    await user.click(screen.getByRole('button', { name: /表格/ }));
    const tableMessage = screen.getByText('upstream timeout while calling inventory').closest('td');
    const table = tableMessage?.closest('table');
    expect(tableMessage).toHaveClass('whitespace-nowrap');
    expect(tableMessage).not.toHaveClass('truncate');
    expect(table).toHaveClass('w-max');
    expect(table?.parentElement).toHaveClass('overflow-x-auto');
  });

  it('reuses the first-page time window when loading the next cursor page', async () => {
    const user = userEvent.setup();
    const pageRequests: CapturedSearchRequest[] = [];
    const olderRecord = {
      ...records[0],
      id: 'log-3',
      timestamp: '2026-08-19T00:59:00.000Z',
      message: 'older page marker',
    };
    server.use(http.post('/api/v1/logs/search', async ({ request }) => {
      const input = await request.json() as CapturedSearchRequest;
      pageRequests.push(input);
      const nextPage = input.cursor === 'page-2';
      return HttpResponse.json({
        code: 0,
        message: '',
        data: nextPage
          ? { records: [olderRecord], has_more: false, took_ms: 11, backends: ['elasticsearch'] }
          : { records, next_cursor: 'page-2', has_more: true, took_ms: 27, backends: ['elasticsearch'] },
      });
    }));

    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 20)); });
    const firstPage = pageRequests.filter((request) => !request.cursor).at(-1)!;

    await user.click(screen.getByRole('button', { name: '加载更多' }));
    await screen.findByText('older page marker');

    const secondPage = pageRequests.find((request) => request.cursor === 'page-2')!;
    expect(secondPage.start).toBe(firstPage.start);
    expect(secondPage.end).toBe(firstPage.end);
  });

  it('zooms to a clicked bucket, supports drag selection, and restores the previous window', async () => {
    const user = userEvent.setup();
    render(<MemoryRouter><LogsPage /></MemoryRouter>);
    await waitForInitialLogs();

    const histogram = screen.getByRole('group', { name: /日志时间直方图/ });
    vi.spyOn(histogram, 'getBoundingClientRect').mockReturnValue({
      x: 0, y: 0, top: 0, left: 0, right: 1000, bottom: 92, width: 1000, height: 92, toJSON: () => ({}),
    } as DOMRect);

    const initialRequestCount = searchRequests.length;
    fireEvent.pointerDown(histogram, { pointerId: 1, button: 0, clientX: 400 });
    fireEvent.pointerUp(histogram, { pointerId: 1, button: 0, clientX: 400 });
    await waitFor(() => expect(searchRequests.length).toBeGreaterThan(initialRequestCount));

    const clicked = searchRequests.at(-1)!;
    expect(new Date(clicked.end).getTime() - new Date(clicked.start).getTime()).toBe(60_000);
    expect(screen.getByText(/已选时间/)).toBeInTheDocument();
    expect(screen.getByLabelText('开始时间')).toHaveAttribute('step', '1');

    const clickedRequestCount = searchRequests.length;
    await user.click(screen.getByRole('button', { name: /返回上一级范围/ }));
    await waitFor(() => expect(searchRequests.length).toBeGreaterThan(clickedRequestCount));

    const restoredRequestCount = searchRequests.length;
    fireEvent.pointerDown(histogram, { pointerId: 2, button: 0, clientX: 220 });
    fireEvent.pointerMove(histogram, { pointerId: 2, button: 0, clientX: 620 });
    fireEvent.pointerUp(histogram, { pointerId: 2, button: 0, clientX: 620 });
    await waitFor(() => expect(searchRequests.length).toBeGreaterThan(restoredRequestCount));

    const dragged = searchRequests.at(-1)!;
    const draggedDuration = new Date(dragged.end).getTime() - new Date(dragged.start).getTime();
    expect(draggedDuration).toBeGreaterThan(1_000);
    expect(draggedDuration).toBeLessThan(60 * 60_000);
  });
});
