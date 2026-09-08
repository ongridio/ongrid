import { selectOption } from '@/test/select-option';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import userEvent from '@testing-library/user-event';
import { listTraceTagValues, searchTraces } from '@/api/traces';
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
  await selectOption(screen.getByRole('combobox', { name: '时间范围' }), '1 天');
  fireEvent.change(screen.getByRole('textbox', { name: 'trace_id' }), { target: { value: 'abc123' } });
  fireEvent.click(screen.getByRole('button', { name: '重置' }));
  await waitFor(() => expect(query()).toBe('{} with (most_recent=true)'));
  for (const name of ['service.name', 'operation', 'peer.service']) {
    expect(screen.getByRole('combobox', { name })).toHaveTextContent(name === 'peer.service' ? '全部依赖' : '全部');
  }
  expect(screen.getByRole('combobox', { name: '请求类型' })).toHaveTextContent('全部请求');
  expect(screen.getByRole('combobox', { name: '时间范围' })).toHaveTextContent('1 小时');
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
