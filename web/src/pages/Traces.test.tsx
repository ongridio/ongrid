import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { expect, it, vi } from 'vitest';
import { searchTraces } from '@/api/traces';
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
  await waitFor(() => expect(screen.getByRole('combobox', { name: 'service.name' }).children.length).toBeGreaterThan(1));
  fireEvent.change(screen.getByRole('combobox', { name: 'service.name' }), { target: { value: 'zp-cms' } });
  fireEvent.change(screen.getByRole('combobox', { name: 'operation' }), { target: { value: 'GET /orders' } });
  fireEvent.change(screen.getByRole('combobox', { name: 'peer.service' }), { target: { value: 'mysql' } });
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
  fireEvent.change(screen.getByRole('combobox', { name: 'service.name' }), { target: { value: 'other' } });
  await waitFor(() => expect(query()).toContain('resource.service.name = "other"'));
  expect(query()).toContain('span:status = error');
  fireEvent.click(screen.getByRole('button', { name: '超过 1s' }));
  await waitFor(() => expect(query()).toContain('trace:duration > 1s'));
  await waitFor(() => expect(screen.getByRole('button', { name: '查询' })).toBeEnabled());
  fireEvent.click(screen.getByRole('button', { name: 'TraceQL' }));
  fireEvent.change(screen.getByPlaceholderText('{ resource.service.name="my-api" && duration > 200ms }'), { target: { value: '{ span:name = "custom" }' } });
  fireEvent.click(screen.getByRole('button', { name: '查询' }));
  await waitFor(() => expect(query()).toBe('{ span:name = "custom" } with (most_recent=true)'));
  fireEvent.change(screen.getByRole('combobox', { name: '时间范围' }), { target: { value: '24h' } });
  fireEvent.change(screen.getByRole('textbox', { name: 'trace_id' }), { target: { value: 'abc123' } });
  fireEvent.click(screen.getByRole('button', { name: '重置' }));
  await waitFor(() => expect(query()).toBe('{} with (most_recent=true)'));
  for (const name of ['service.name', 'operation', 'peer.service']) {
    expect(screen.getByRole('combobox', { name })).toHaveValue('');
  }
  expect(screen.getByRole('combobox', { name: '请求类型' })).toHaveValue('all');
  expect(screen.getByRole('combobox', { name: '时间范围' })).toHaveValue('1h');
  expect(screen.getByRole('textbox', { name: 'trace_id' })).toHaveValue('');
  expect(screen.getByPlaceholderText('{ resource.service.name="my-api" && duration > 200ms }')).toHaveValue('');
  const request = vi.mocked(searchTraces).mock.calls.at(-1)![0];
  expect(Date.parse(request.end) - Date.parse(request.start)).toBe(3600_000);
});
