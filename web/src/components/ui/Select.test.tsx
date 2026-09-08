import { useState } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { Select } from './Select';
import { RoleSelect, type RoleFilterValue } from './RoleSelect';

beforeEach(() => {
  localStorage.setItem('ongrid-locale', 'zh-CN');
});

function Example({ searchable = false }: { searchable?: boolean }) {
  const [value, setValue] = useState('');
  return <>
    <Select label="服务" value={value} onValueChange={setValue} searchable={searchable} options={[
      { value: '', label: '全部' }, { value: 'orders', label: '订单服务' }, { value: 'billing', label: '账单服务', disabled: true },
    ]} />
    <output data-testid="value">{value}</output>
  </>;
}

it('单选支持键盘、空字符串选项、禁用项，选择后关闭并恢复焦点', async () => {
  const user = userEvent.setup();
  render(<Example />);
  const trigger = screen.getByRole('combobox', { name: '服务' });
  await act(async () => { await user.tab(); });
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  expect(await screen.findByRole('option', { name: '账单服务' })).toHaveAttribute('aria-disabled', 'true');
  await waitFor(() => expect(screen.getByRole('option', { name: '全部' })).toHaveFocus());
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  await waitFor(() => expect(screen.getByRole('option', { name: '订单服务' })).toHaveFocus());
  await act(async () => { await user.keyboard('{Enter}'); });
  await waitFor(() => expect(screen.getByTestId('value')).toHaveTextContent('orders'));
  await waitFor(() => expect(trigger).toHaveFocus());
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  const all = await screen.findByRole('option', { name: '全部' });
  await act(async () => { await user.click(all); });
  expect(screen.getByTestId('value')).toBeEmptyDOMElement();
  expect(trigger).toHaveTextContent('全部');
});

it('搜索按显示名称过滤，有空态，Escape 不提交选择且回到触发器', async () => {
  const user = userEvent.setup();
  render(<Example searchable />);
  const trigger = screen.getByRole('combobox', { name: '服务' });
  await act(async () => { await user.click(trigger); });
  const input = await screen.findByRole('combobox', { name: '搜索 服务' });
  await act(async () => { await user.type(input, '不存在'); });
  expect(await screen.findByText('没有匹配项')).toBeVisible();
  await act(async () => { await user.keyboard('{Escape}'); });
  await waitFor(() => expect(trigger).toHaveFocus());
  expect(screen.getByTestId('value')).toBeEmptyDOMElement();
  await act(async () => { await user.click(trigger); });
  const search = await screen.findByRole('combobox', { name: '搜索 服务' });
  await act(async () => { await user.clear(search); });
  await act(async () => { await user.type(search, '订单'); });
  expect(screen.queryByRole('option', { name: '账单服务' })).not.toBeInTheDocument();
  await act(async () => { await user.keyboard('{ArrowDown}'); });
  await act(async () => { await user.keyboard('{Enter}'); });
  await waitFor(() => expect(screen.getByTestId('value')).toHaveTextContent('orders'));
});

it('禁用下拉不可打开，公共角色选择保留未分类选项和隐藏标签时的名称', async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  const { unmount } = render(<Select label="禁用" value="" onValueChange={onChange} options={[{ value: '', label: '全部' }]} disabled />);
  await act(async () => { await user.click(screen.getByRole('combobox', { name: '禁用' })); });
  expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  expect(onChange).not.toHaveBeenCalled();
  unmount();
  function Roles() {
    const [value, setValue] = useState<RoleFilterValue>('');
    return <RoleSelect value={value} onChange={setValue} showLabel={false} />;
  }
  render(<Roles />);
  await act(async () => { await user.click(screen.getByRole('combobox', { name: '角色' })); });
  const unknown = await screen.findByRole('option', { name: '未分类' });
  await act(async () => { await user.click(unknown); });
  expect(screen.getByRole('combobox', { name: '角色' })).toHaveTextContent('未分类');
});
