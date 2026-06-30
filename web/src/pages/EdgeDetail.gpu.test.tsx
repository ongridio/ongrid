// EdgeDetail GPU 指标面板 — MSW 双卡 mock，断言 4 面板 + 图例 GPU 0/1。
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import EdgeDetailPage from './EdgeDetail';
import { server } from '@/test/msw-server';
import { buildGpuExprs } from '@/lib/edgeGpuMetrics';

vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));

vi.mock('@/lib/drilldown', () => ({
  openMetricDrilldown: vi.fn().mockResolvedValue(undefined),
}));

const UUID_A = 'GPU-uuid-aaa';
const UUID_B = 'GPU-uuid-bbb';

function dualGpuMatrix(expr: string) {
  const isUtil = expr.includes('utilization_gpu_ratio');
  const isMem = expr.includes('memory_used_bytes');
  const isTemp = expr.includes('temperature_gpu');
  const baseA = isUtil ? 10 : isMem ? 20 : isTemp ? 55 : 120;
  const baseB = isUtil ? 30 : isMem ? 40 : isTemp ? 65 : 150;
  return [
    {
      metric: { uuid: UUID_A, device_id: '25' },
      values: [
        [1_700_000_000, String(baseA)],
        [1_700_000_060, String(baseA + 5)],
      ],
    },
    {
      metric: { uuid: UUID_B, device_id: '25' },
      values: [
        [1_700_000_000, String(baseB)],
        [1_700_000_060, String(baseB + 5)],
      ],
    },
  ];
}

function hostMatrix() {
  return [
    {
      metric: { cpu: '0', device_id: '25' },
      values: [[1_700_000_000, '0.05']],
    },
  ];
}

const gpuEdge = {
  id: 4,
  name: '4060ti',
  status: 'online' as const,
  roles: ['server'] as const,
  access_key_id: 'ak-test',
  last_seen_at: '2026-06-30T12:00:00Z',
  device_id: 25,
  host_info: {
    gpu_available: true,
    gpu_model: 'NVIDIA GeForce RTX 4060 Ti',
  },
};

const noGpuEdge = {
  ...gpuEdge,
  host_info: { gpu_available: false, gpu_model: '' },
};

const promCalls: string[] = [];

function useMetricsHandlers(edge: typeof gpuEdge) {
  server.use(
    http.get('/api/v1/edges/4', () => HttpResponse.json(edge)),
    http.get('/api/v1/metrics/query_range', ({ request }) => {
      const url = new URL(request.url);
      const expr = url.searchParams.get('expr') ?? '';
      promCalls.push(expr);

      const gpuExprs = buildGpuExprs(edge.device_id ?? 0);
      const gpuExprSet = new Set(Object.values(gpuExprs));
      if (gpuExprSet.has(expr)) {
        return HttpResponse.json({
          resolution: '1m',
          from: url.searchParams.get('start'),
          to: url.searchParams.get('end'),
          matrix: dualGpuMatrix(expr),
        });
      }

      return HttpResponse.json({
        resolution: '1m',
        from: url.searchParams.get('start'),
        to: url.searchParams.get('end'),
        matrix: hostMatrix(),
      });
    }),
  );
}

function renderEdgeDetail() {
  return render(
    <MemoryRouter initialEntries={['/edges/4']}>
      <Routes>
        <Route path="/edges/:edgeId" element={<EdgeDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('EdgeDetail GPU metrics tab', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    promCalls.length = 0;
    vi.stubGlobal(
      'ResizeObserver',
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
  });

  it('shows 4 GPU panels with GPU 0 / GPU 1 legends when gpu_available', async () => {
    useMetricsHandlers(gpuEdge);
    renderEdgeDetail();

    expect(await screen.findByText('GPU 利用率')).toBeInTheDocument();
    expect(screen.getByText('GPU 显存占用')).toBeInTheDocument();
    expect(screen.getByText('GPU 温度')).toBeInTheDocument();
    expect(screen.getByText('GPU 功耗')).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getAllByText('GPU 0').length).toBeGreaterThanOrEqual(4);
      expect(screen.getAllByText('GPU 1').length).toBeGreaterThanOrEqual(4);
    });

    const gpuExprs = buildGpuExprs(25);
    await waitFor(() => {
      expect(promCalls.filter((e) => e === gpuExprs.gpuUtil).length).toBeGreaterThanOrEqual(1);
      expect(promCalls.filter((e) => e === gpuExprs.gpuMem).length).toBeGreaterThanOrEqual(1);
      expect(promCalls.filter((e) => e === gpuExprs.gpuTemp).length).toBeGreaterThanOrEqual(1);
      expect(promCalls.filter((e) => e === gpuExprs.gpuPower).length).toBeGreaterThanOrEqual(1);
    });
  });

  it('hides GPU panels when gpu_available is false', async () => {
    useMetricsHandlers(noGpuEdge);
    renderEdgeDetail();

    await screen.findByText('CPU 利用率（按核）');
    expect(screen.queryByText('GPU 利用率')).not.toBeInTheDocument();

    const gpuExprs = buildGpuExprs(25);
    await waitFor(() => {
      expect(promCalls.some((e) => Object.values(gpuExprs).includes(e))).toBe(false);
    });
  });

  it('shows no-data hint when gpu_available but prom returns empty matrix', async () => {
    server.use(
      http.get('/api/v1/edges/4', () => HttpResponse.json(gpuEdge)),
      http.get('/api/v1/metrics/query_range', ({ request }) => {
        const url = new URL(request.url);
        const expr = url.searchParams.get('expr') ?? '';
        promCalls.push(expr);
        const gpuExprs = buildGpuExprs(25);
        if (Object.values(gpuExprs).includes(expr)) {
          return HttpResponse.json({
            resolution: '1m',
            from: url.searchParams.get('start'),
            to: url.searchParams.get('end'),
            matrix: [],
          });
        }
        return HttpResponse.json({
          resolution: '1m',
          from: url.searchParams.get('start'),
          to: url.searchParams.get('end'),
          matrix: hostMatrix(),
        });
      }),
    );
    renderEdgeDetail();

    expect(
      await screen.findByText(/主机已上报 GPU，但暂无 GPU 时序数据/),
    ).toBeInTheDocument();
  });
});
