import { useState } from 'react';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { Modal } from '@/components/Modal';
import { Checkbox, Switch, Select, Popover, PopoverTrigger, PopoverContent, DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, Tabs, TabsList, TabsTrigger, TabsContent, Hint } from './index';
import { useDialogs } from './useDialogs';

beforeEach(() => { localStorage.setItem('ongrid-locale', 'zh-CN'); });

it('嵌套选择器先处理 Escape，外层弹窗保持打开，关闭后焦点回到入口', async () => {
  const user = userEvent.setup();
  function Example() {
    const [open, setOpen] = useState(false);
    const [value, setValue] = useState('a');
    return <><button onClick={() => setOpen(true)}>编辑</button><Modal open={open} onClose={() => setOpen(false)} title="配置">
      <Select label="类型" value={value} onValueChange={setValue} options={[{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]} />
    </Modal></>;
  }
  render(<Example />);
  const entry = screen.getByRole('button', { name: '编辑' });
  await act(async () => { await user.click(entry); });
  const dialog = await screen.findByRole('dialog', { name: '配置' });
  const select = within(dialog).getByRole('combobox', { name: '类型' });
  await act(async () => { await user.click(select); });
  await screen.findByRole('listbox');
  await act(async () => { await user.keyboard('{Escape}'); });
  expect(dialog).toBeVisible();
  await waitFor(() => expect(select).toHaveFocus());
  await act(async () => { await user.keyboard('{Escape}'); });
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  await waitFor(() => expect(entry).toHaveFocus());
});

it('动态 option、空值和数字值保留表单语义及外部 label 关联', async () => {
  const user = userEvent.setup();
  function Example() {
    const [value, setValue] = useState('0');
    return <form aria-label="表单"><label htmlFor="retention">保留时间</label><Select id="retention" name="retention" value={value} onValueChange={setValue}>
      <><option value="">全部</option>{[0, 7].map((n) => <option key={n} value={n}>{n} 天</option>)}</>
    </Select></form>;
  }
  render(<Example />);
  await act(async () => { await user.click(screen.getByRole('combobox', { name: '保留时间' })); });
  await act(async () => { await user.click(await screen.findByRole('option', { name: '7 天' })); });
  expect(new FormData(screen.getByRole('form') as HTMLFormElement).get('retention')).toBe('7');
});

it('Checkbox 和 Switch 支持标签点击、键盘、半选、禁用及表单值', async () => {
  const user = userEvent.setup();
  const disabledChange = vi.fn();
  function Example() {
    const [checked, setChecked] = useState(false);
    const [enabled, setEnabled] = useState(false);
    return <form aria-label="开关表单">
      <label>选择设备<Checkbox checked={checked} indeterminate={!checked} onCheckedChange={setChecked} name="device" value="42" /></label>
      <label>启用采集<Switch checked={enabled} onCheckedChange={setEnabled} name="enabled" /></label>
      <Checkbox aria-label="禁用" disabled onCheckedChange={disabledChange} />
    </form>;
  }
  render(<Example />);
  expect(screen.getByRole('checkbox', { name: '选择设备' })).toHaveAttribute('aria-checked', 'mixed');
  await act(async () => { await user.click(screen.getByText('选择设备')); });
  expect(screen.getByRole('checkbox', { name: '选择设备' })).toBeChecked();
  const toggle = screen.getByRole('switch', { name: '启用采集' });
  await act(async () => { toggle.focus(); await user.keyboard(' '); });
  expect(toggle).toBeChecked();
  expect(new FormData(screen.getByRole('form') as HTMLFormElement).get('device')).toBe('42');
  await act(async () => { await user.click(screen.getByRole('checkbox', { name: '禁用' })); });
  expect(disabledChange).not.toHaveBeenCalled();
});

it('菜单键盘跳过禁用项，执行后关闭并返回触发器', async () => {
  const user = userEvent.setup();
  const action = vi.fn();
  render(<DropdownMenu><DropdownMenuTrigger>操作</DropdownMenuTrigger><DropdownMenuContent>
    <DropdownMenuItem disabled>禁止删除</DropdownMenuItem><DropdownMenuItem onClick={action}>重命名</DropdownMenuItem>
  </DropdownMenuContent></DropdownMenu>);
  const trigger = screen.getByRole('button', { name: '操作' });
  await act(async () => { trigger.focus(); await user.keyboard('{ArrowDown}'); });
  await screen.findByRole('menu');
  await act(async () => { await user.keyboard('{End}{Enter}'); });
  expect(action).toHaveBeenCalledOnce();
  await waitFor(() => expect(trigger).toHaveFocus());
  expect(screen.queryByRole('menu')).not.toBeInTheDocument();
});

it('Popover 允许填写内容，Escape 返回触发器；Tooltip 支持键盘聚焦', async () => {
  const user = userEvent.setup();
  render(<><Popover><PopoverTrigger>筛选</PopoverTrigger><PopoverContent aria-label="筛选条件"><input aria-label="关键字" /></PopoverContent></Popover><Hint content="刷新当前数据"><button>刷新</button></Hint></>);
  const trigger = screen.getByRole('button', { name: '筛选' });
  await act(async () => { await user.click(trigger); });
  await act(async () => { await user.type(await screen.findByRole('textbox', { name: '关键字' }), 'orders'); });
  expect(screen.getByRole('textbox')).toHaveValue('orders');
  await act(async () => { await user.keyboard('{Escape}'); });
  await waitFor(() => expect(trigger).toHaveFocus());
  await act(async () => { await user.tab(); });
  expect(await screen.findByRole('tooltip')).toHaveTextContent('刷新当前数据');
});

it('Tabs 使用方向键切换并关联当前 panel', async () => {
  const user = userEvent.setup();
  render(<Tabs defaultValue="logs"><TabsList aria-label="数据视图"><TabsTrigger value="logs">日志</TabsTrigger><TabsTrigger value="traces">链路</TabsTrigger></TabsList><TabsContent value="logs">日志列表</TabsContent><TabsContent value="traces">链路列表</TabsContent></Tabs>);
  await act(async () => { screen.getByRole('tab', { name: '日志' }).focus(); await user.keyboard('{ArrowRight}{Enter}'); });
  await waitFor(() => expect(screen.getByRole('tab', { name: '链路' })).toHaveAttribute('aria-selected', 'true'));
  expect(screen.getByRole('tabpanel', { name: '链路' })).toHaveTextContent('链路列表');
});

it('确认弹窗只有明确确认才执行，取消及卸载都会结束等待', async () => {
  const user = userEvent.setup();
  const result = vi.fn();
  function Example() {
    const { confirmAction, dialog } = useDialogs();
    return <>{dialog}<button onClick={async () => result(await confirmAction('删除设备？'))}>删除设备</button></>;
  }
  const { unmount } = render(<Example />);
  await act(async () => { await user.click(screen.getByRole('button', { name: '删除设备' })); });
  await act(async () => { await user.click(await screen.findByRole('button', { name: '取消' })); });
  expect(result).toHaveBeenLastCalledWith(false);
  await act(async () => { await user.click(screen.getByRole('button', { name: '删除设备' })); });
  await act(async () => { await user.click(await screen.findByRole('button', { name: '确认' })); });
  expect(result).toHaveBeenLastCalledWith(true);
  await act(async () => { await user.click(screen.getByRole('button', { name: '删除设备' })); });
  unmount();
  await waitFor(() => expect(result).toHaveBeenLastCalledWith(false));
});

it('菜单鼠标打开', async () => {
  const user = userEvent.setup();
  render(<div onClick={(e) => e.stopPropagation()}><DropdownMenu><DropdownMenuTrigger>鼠标菜单</DropdownMenuTrigger><DropdownMenuContent><DropdownMenuItem>项目</DropdownMenuItem></DropdownMenuContent></DropdownMenu></div>);
  await act(async () => { await user.click(screen.getByRole('button', { name: '鼠标菜单' })); });
  expect(await screen.findByRole('menuitem', { name: '项目' })).toBeInTheDocument();
});
