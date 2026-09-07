import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import { searchTraces } from '@/api/traces';
import TracesPage from './Traces';

vi.mock('@/api/traces', () => ({
  getTrace: vi.fn(async () => ({ batches: [] })),
  listTraceTagValues: vi.fn(async () => ({ values: [] })),
  searchTraces: vi.fn(async () => ({ traces: [{ traceID: '3', rootServiceName: 'orders', rootTraceName: 'GET /orders', durationMs: 20, startTimeUnixNano: '1788739260000000000' }] })),
}));
vi.mock('@/components/traces/TraceWaterfall', () => ({ TraceWaterfall: () => <div>waterfall</div> }));
function Location() { const location = useLocation(); return <output data-testid="location">{location.pathname}{location.search}</output>; }
it('APM 链路详情保留查询时间与服务范围，日志使用完整 Trace ID，返回列表保留上下文', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  const p = new URLSearchParams({ start: '2026-09-07T00:00:00Z', end: '2026-09-07T01:00:00Z', service_name: 'orders', service_namespace: 'trade', environment: 'production', q: '{ resource.service.name = "orders" }' });
  render(<MemoryRouter initialEntries={[`/traces/3?${p}`]}><Location /><Routes><Route path="/traces/:traceId" element={<TracesPage />} /><Route path="/traces" element={<TracesPage />} /></Routes></MemoryRouter>);
  const link = await screen.findByRole('link', { name: '查看同请求日志' });
  const logURL = new URL(link.getAttribute('href')!, 'http://localhost');
  expect(logURL.searchParams.get('trace_id')).toBe('00000000000000000000000000000003');
  expect(logURL.searchParams.get('environment')).toBe('production');
  await waitFor(() => expect(searchTraces).toHaveBeenCalledWith(expect.objectContaining({ start: '2026-09-07T00:00:00.000Z', end: '2026-09-07T01:00:00.000Z' })));
  fireEvent.click(screen.getByRole('button', { name: /返回链路列表/ }));
  expect(screen.getByTestId('location')).toHaveTextContent('/traces?');
  expect(screen.getByTestId('location')).toHaveTextContent('environment=production');
});
