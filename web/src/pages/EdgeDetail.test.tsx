import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { http, HttpResponse } from 'msw';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import EdgeDetailPage, {
  calculateTooltipYDomain,
  findNearestTooltipEntry,
} from './EdgeDetail';
import { server } from '@/test/msw-server';

const CPU_SERIES = Array.from({ length: 496 }, (_, cpu) => ({
  metric: { device_id: '586', cpu: String(cpu) },
  values: [
    [1_785_217_600, '20'],
    [1_785_217_660, '21'],
  ] as [number, string][],
}));

function useMetricsSeries(series = CPU_SERIES) {
  server.use(
    http.get('/api/v1/metrics/query_range', ({ request }) => {
      const expr = new URL(request.url).searchParams.get('expr') ?? '';
      return HttpResponse.json({
        resolution: '1m',
        from: '2026-07-28T00:00:00Z',
        to: '2026-07-28T06:00:00Z',
        matrix: expr.includes('node_cpu_seconds_total') ? series : [],
      });
    }),
  );
}

function renderDeviceMetrics() {
  render(
    <MemoryRouter initialEntries={['/devices/586']}>
      <Routes>
        <Route path="/devices/:edgeId" element={<EdgeDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

vi.stubGlobal(
  'ResizeObserver',
  class ResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  },
);

describe('EdgeDetailPage high-cardinality metrics', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/devices/586', () =>
        HttpResponse.json({
          id: 586,
          name: 'Issue #238 - 496-core chart repro',
          hostname: 'issue-238-496core',
          cpu_count: 496,
          online: true,
        }),
      ),
      http.get('/api/v1/devices/586/edges', () => HttpResponse.json({ items: [] })),
    );
    useMetricsSeries();
  });

  it('在图表外完整展示 496 条 CPU 图例并保持自然数字顺序', async () => {
    renderDeviceMetrics();

    const heading = await screen.findByRole('heading', { name: 'CPU 利用率（按核）' });
    const panel = heading.closest('section');
    expect(panel).not.toBeNull();

    const numericLegendButtons = () =>
      within(panel as HTMLElement)
        .queryAllByRole('button')
        .filter((button) => /^\d+$/.test(button.textContent?.trim() ?? ''));

    await waitFor(() => expect(numericLegendButtons()).toHaveLength(496));
    expect(numericLegendButtons().map((button) => button.textContent)).toEqual(
      Array.from({ length: 496 }, (_, cpu) => String(cpu)),
    );
    expect(
      within(panel as HTMLElement).queryByRole('button', { name: '下一组序列' }),
    ).not.toBeInTheDocument();

    const legend = within(panel as HTMLElement).getByRole('group', { name: '序列图例' });
    const chart = legend.previousElementSibling;
    expect(chart).not.toBeNull();
    expect(chart).toHaveClass('h-60');
  });

  it('按只看、切换、恢复循环切换序列状态', async () => {
    useMetricsSeries(CPU_SERIES.slice(0, 3));
    renderDeviceMetrics();

    const heading = await screen.findByRole('heading', { name: 'CPU 利用率（按核）' });
    const panel = heading.closest('section');
    expect(panel).not.toBeNull();

    await waitFor(() =>
      expect(
        within(panel as HTMLElement).getByRole('button', { name: '0 · 正常' }),
      ).toBeInTheDocument(),
    );

    fireEvent.click(within(panel as HTMLElement).getByRole('button', { name: '0 · 正常' }));
    await waitFor(() =>
      expect(
        within(panel as HTMLElement).getByRole('button', { name: '0 · 已选中' }),
      ).toHaveAttribute('aria-pressed', 'true'),
    );

    fireEvent.click(within(panel as HTMLElement).getByRole('button', { name: '1 · 正常' }));
    await waitFor(() =>
      expect(
        within(panel as HTMLElement).getByRole('button', { name: '1 · 已选中' }),
      ).toHaveAttribute('aria-pressed', 'true'),
    );
    expect(
      within(panel as HTMLElement).getByRole('button', { name: '0 · 正常' }),
    ).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(within(panel as HTMLElement).getByRole('button', { name: '1 · 已选中' }));
    await waitFor(() =>
      expect(
        within(panel as HTMLElement).getByRole('button', { name: '1 · 正常' }),
      ).toHaveAttribute('aria-pressed', 'false'),
    );
  });
});

describe('EdgeDetailPage network device layout', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/devices/140', () =>
        HttpResponse.json({
          id: 140,
          name: 'ongrid-netdev-b',
          hostname: 'ongrid-netdev-b',
          os: 'network',
          arch: 'network',
          ip_address: '10.20.0.3',
          roles: ['network'],
          online: false,
        }),
      ),
      http.get('/api/v1/devices/140/edges', () => HttpResponse.json({ items: [] })),
      http.get('/api/v1/devices/140/network', () =>
        HttpResponse.json({
          device_id: 140,
          device_kind: 'network',
          management_address: '10.20.0.3',
          sys_name: 'ongrid-netdev-b',
          vendor: 'Ongrid Lab',
          model: 'Virtual Switch',
          reachability_status: 'reachable',
          discovery_source: 'snmp',
          scanner_host_name: 'VM-4-17-ubuntu',
          last_observed_at: '2026-08-04T08:00:00Z',
          interfaces: [
            { if_index: 1, name: 'eth0', mac: '02:42:ac:14:00:03', interface_kind: 'ethernet', oper_status: 'up' },
          ],
          links: [],
        }),
      ),
    );
  });

  it('使用网络设备专属标签和接口，不显示主机页签', async () => {
    render(
      <MemoryRouter initialEntries={['/devices/140']}>
        <Routes>
          <Route path="/devices/:edgeId" element={<EdgeDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('Ongrid Lab')).toBeInTheDocument();
    expect(screen.getByText('可达')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '概览' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '接口' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '指标' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '主机信息' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '插件' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '接口' }));
    expect(await screen.findByText('eth0')).toBeInTheDocument();
    expect(screen.getByText('02:42:ac:14:00:03')).toBeInTheDocument();
  });

  it('从查询参数直接打开网络拓扑页签', async () => {
    render(
      <MemoryRouter initialEntries={['/devices/140?tab=topology']}>
        <Routes>
          <Route path="/devices/:edgeId" element={<EdgeDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await screen.findByText('ongrid-netdev-b');
    expect(screen.getByRole('button', { name: '拓扑' })).toHaveClass('border-zinc-100');
  });
});

describe('findNearestTooltipEntry', () => {
  it('自动纵轴使用实际数据范围而不是强制从零开始', () => {
    const rows = [
      { ts: 1, tsLabel: '00:01', netRx_eth0: 80, netRx_eth1: 90 },
      { ts: 2, tsLabel: '00:02', netRx_eth0: 85, netRx_eth1: 95 },
    ];
    const series = [
      { key: 'netRx_eth0', label: 'eth0', color: '#000' },
      { key: 'netRx_eth1', label: 'eth1', color: '#fff' },
    ];

    expect(calculateTooltipYDomain(rows, series)).toEqual([80, 95]);
  });

  it('只返回鼠标纵向位置最近的序列', () => {
    const payload = [
      { dataKey: 'cpu_20', value: 20 },
      { dataKey: 'cpu_80', value: 80 },
    ];

    expect(findNearestTooltipEntry(payload, 22, { y: 0, height: 100 }, [0, 100])).toEqual(
      payload[1],
    );
    expect(findNearestTooltipEntry(payload, 79, { y: 0, height: 100 }, [0, 100])).toEqual(
      payload[0],
    );
  });
});

describe('EdgeDetailPage SkyWalking receiver', () => {
  it.each([
    ['zh-CN', {}, '127.0.0.1:11800', '编辑配置', '随 Trace 采集自动开启'],
    ['en-US', { grpc_endpoint: '0.0.0.0:4317' }, '0.0.0.0:11800', 'Edit config', 'enabled automatically with Trace collection'],
    ['zh-CN', { skywalking_grpc_endpoint: '127.0.0.1:21800' }, '127.0.0.1:21800', '编辑配置', '随 Trace 采集自动开启'],
  ] as const)('shows the receiver address for %s %j without another toggle', async (locale, spec, endpoint, edit, hint) => {
    localStorage.setItem('ongrid-locale', locale);
    server.use(
      http.get('/api/v1/edges/42', () => HttpResponse.json({ id: 42, name: 'skywalking-edge', status: 'online', roles: [] })),
      http.get('/api/v1/edges/42/plugins', () => HttpResponse.json({ items: [{ plugin_name: 'traces', enabled: true, spec }] })),
    );
    render(
      <MemoryRouter initialEntries={['/edges/42?tab=plugins']}>
        <Routes><Route path="/edges/:edgeId" element={<EdgeDetailPage />} /></Routes>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: edit }));
    expect(await screen.findByText(endpoint)).toBeInTheDocument();
    expect(screen.getByText(new RegExp(hint))).toBeInTheDocument();
    expect(screen.getAllByRole('checkbox')).toHaveLength(2); // Existing OTLP controls only.
  });
});
