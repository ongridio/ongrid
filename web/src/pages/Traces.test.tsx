import { selectOption } from '@/test/select-option';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { getTrace, listTraceTagValues, searchTraces } from '@/api/traces';
import TracesPage from './Traces';

vi.mock('@/api/traces', () => ({
  getTrace: vi.fn(),
  listTraceTagValues: vi.fn(async () => ({ values: ['zp-cms', 'other', 'GET /orders', 'mysql'] })),
  searchTraces: vi.fn(async () => ({ traces: [] })),
}));
vi.mock('@/components/traces/TraceWaterfall', () => ({ TraceWaterfall: () => null }));

it('快捷筛选叠加现有条件，切换服务后仍生效，重置清空所有条件', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  render(<MemoryRouter><TracesPage /></MemoryRouter>);
  await selectOption(screen.getByRole('combobox', { name: 'service.name' }), 'zp-cms');
  await selectOption(screen.getByRole('combobox', { name: 'operation' }), 'GET /orders');
  await selectOption(screen.getByRole('combobox', { name: 'peer.service' }), 'mysql');
  const query = () => vi.mocked(searchTraces).mock.calls.at(-1)![0].q!;
  for (const [label, condition] of [['超过 1s', 'trace:duration > 1s'], ['出错的 trace', 'span:status = error']]) {
    fireEvent.click(screen.getByRole('button', { name: label }));
    await waitFor(() => {
      expect(query()).toContain(condition);
      expect(query()).toContain('resource.service.name = "zp-cms"');
      expect(query()).toContain('span:name = "GET /orders"');
      expect(query()).toContain('span.peer.service = "mysql"');
      expect(query()).toContain('trace:rootName !~');
    });
    fireEvent.click(screen.getByRole('button', { name: label }));
    await waitFor(() => expect(query()).not.toContain(condition));
    expect(query()).toContain('resource.service.name = "zp-cms"');
    expect(query()).toContain('span:name = "GET /orders"');
    expect(query()).toContain('span.peer.service = "mysql"');
    expect(query()).toContain('trace:rootName !~');
    fireEvent.click(screen.getByRole('button', { name: label }));
    await waitFor(() => expect(query()).toContain(condition));
  }
  expect(query()).not.toContain('trace:duration > 1s');
  await selectOption(screen.getByRole('combobox', { name: 'service.name' }), 'other');
  await waitFor(() => expect(query()).toContain('resource.service.name = "other"'));
  expect(query()).toContain('span:status = error');
  fireEvent.click(screen.getByRole('button', { name: '超过 1s' }));
  await waitFor(() => expect(query()).toContain('trace:duration > 1s'));
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'TraceQL' }));
  fireEvent.change(screen.getByPlaceholderText('{ resource.service.name="my-api" && duration > 200ms }'), { target: { value: '{ span:name = "custom" }' } });
  fireEvent.click(screen.getByRole('button', { name: '查询' }));
  await waitFor(() => expect(query()).toBe('{ span:name = "custom" } with (most_recent=true)'));
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.click(screen.getByRole('button', { name: '1 天' }));
  fireEvent.change(screen.getByRole('textbox', { name: 'trace_id' }), { target: { value: 'abc123' } });
  fireEvent.click(screen.getByRole('button', { name: '重置' }));
  await waitFor(() => expect(query()).toBe('{} with (most_recent=true)'));
  for (const name of ['service.name', 'operation', 'peer.service']) {
    expect(screen.getByRole('combobox', { name })).toHaveTextContent(name === 'peer.service' ? '全部依赖' : '全部');
  }
  expect(screen.getByRole('combobox', { name: '请求类型' })).toHaveTextContent('全部请求');
  expect(screen.getByRole('button', { name: '时间范围' })).toHaveTextContent('1 小时');
  expect(screen.getByRole('textbox', { name: 'trace_id' })).toHaveValue('');
  expect(screen.getByPlaceholderText('{ resource.service.name="my-api" && duration > 200ms }')).toHaveValue('');
  const request = vi.mocked(searchTraces).mock.calls.at(-1)![0];
  expect(Date.parse(request.end) - Date.parse(request.start)).toBe(3600_000);
});

it('搜索服务并用键盘选择只提交一次查询，选择全部清除服务过滤', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  vi.mocked(searchTraces).mockClear();
  vi.mocked(listTraceTagValues).mockImplementation(async () => ({ values: ['orders', 'billing'] }));
  const user = userEvent.setup();
  render(<MemoryRouter initialEntries={['/traces']}><TracesPage /></MemoryRouter>);
  await waitFor(() => expect(searchTraces).toHaveBeenCalledTimes(1));
  act(() => screen.getByRole('combobox', { name: 'service.name' }).focus());
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  const search = await screen.findByRole('combobox', { name: '搜索 service.name' });
  await act(async () => { await user.type(search, 'orders'); });
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  await act(async () => { await user.keyboard('{Enter}'); });
  await waitFor(() => expect(searchTraces).toHaveBeenCalledTimes(2));
  expect(vi.mocked(searchTraces).mock.lastCall?.[0].q).toContain('resource.service.name = "orders"');
  const trigger = screen.getByRole('combobox', { name: 'service.name' });
  await waitFor(() => expect(trigger).toHaveFocus());
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  const all = await screen.findByRole('option', { name: '全部' });
  await act(async () => { await user.click(all); });
  await waitFor(() => expect(searchTraces).toHaveBeenCalledTimes(3));
  expect(vi.mocked(searchTraces).mock.lastCall?.[0].q).not.toContain('orders');
});

function Location() { const location = useLocation(); return <output data-testid="location">{location.pathname}{location.search}</output>; }
it('APM 链路详情保留查询时间与服务范围，日志使用完整 Trace ID，返回列表保留上下文', async () => {
  vi.mocked(getTrace).mockResolvedValue({ batches: [] });
  vi.mocked(searchTraces).mockResolvedValue({ from: '2026-09-07T00:00:00Z', to: '2026-09-07T01:00:00Z', traces: [{ traceID: '3', rootServiceName: 'orders', rootTraceName: 'GET /orders', durationMs: 20, startTimeUnixNano: '1788739260000000000' }] });
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

it('自定义时间仅在应用后查询，并保留到 Trace 搜索的起止时间', async () => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
  vi.mocked(searchTraces).mockClear();
  render(<MemoryRouter initialEntries={['/traces']}><TracesPage /></MemoryRouter>);
  await waitFor(() => expect(searchTraces).toHaveBeenCalled());
  const count = vi.mocked(searchTraces).mock.calls.length;
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-09-08T01:00:00' } });
  fireEvent.change(screen.getByLabelText('结束时间'), { target: { value: '2026-09-08T02:00:00' } });
  expect(searchTraces).toHaveBeenCalledTimes(count);
  fireEvent.click(screen.getByRole('button', { name: '应用' }));
  await waitFor(() => expect(vi.mocked(searchTraces).mock.calls.at(-1)![0]).toMatchObject({
    start: new Date('2026-09-08T01:00:00').toISOString(), end: new Date('2026-09-08T02:00:00').toISOString(),
  }));
  expect(screen.queryByLabelText('开始时间')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.click(screen.getByRole('button', { name: '1 小时' }));
  await waitFor(() => expect(Date.parse(vi.mocked(searchTraces).mock.calls.at(-1)![0].end)).toBeGreaterThan(Date.now() - 10000));
  const beforeRepeat = vi.mocked(searchTraces).mock.calls.length;
  fireEvent.click(screen.getByRole('button', { name: '时间范围' }));
  fireEvent.click(screen.getByRole('button', { name: '1 小时' }));
  await waitFor(() => expect(searchTraces).toHaveBeenCalledTimes(beforeRepeat + 1));
});
