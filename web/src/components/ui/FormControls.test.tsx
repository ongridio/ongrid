import { createRef } from 'react';
import { render, screen, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { Button, Input, Label, Textarea, Radio, Slider } from './index';

it('保留输入框焦点、标签、原生约束和表单提交值', async () => {
  const ref = createRef<HTMLInputElement>();
  const submit = vi.fn((event: React.FormEvent) => event.preventDefault());
  render(<form aria-label="编辑" onSubmit={submit}>
    <Label htmlFor="name">名称</Label><Input id="name" ref={ref} name="name" required defaultValue="node" />
    <Label htmlFor="secret">密码</Label><Input id="secret" name="secret" type="password" defaultValue="test" autoComplete="current-password" />
    <Label htmlFor="note">备注</Label><Textarea id="note" name="note" defaultValue="details" rows={3} />
    <Input aria-label="只读编号" readOnly defaultValue="42" /><Input aria-label="禁用字段" disabled />
    <Input aria-label="错误字段" aria-invalid="true" aria-describedby="field-error" /><span id="field-error">格式不正确</span>
    <Button>取消</Button><Button type="submit">保存</Button>
  </form>);
  ref.current?.focus();
  expect(screen.getByRole('textbox', { name: '名称' })).toHaveFocus();
  expect(screen.getByLabelText('名称')).toBeRequired();
  expect(screen.getByLabelText('密码')).toHaveAttribute('type', 'password');
  expect(screen.getByLabelText('备注')).toHaveAttribute('rows', '3');
  expect(screen.getByRole('textbox', { name: '禁用字段' })).toBeDisabled();
  expect(screen.getByRole('textbox', { name: '错误字段' })).toHaveAccessibleDescription('格式不正确');
  await userEvent.click(screen.getByRole('button', { name: '取消' }));
  expect(submit).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole('button', { name: '保存' }));
  expect(submit).toHaveBeenCalledOnce();
  expect(Object.fromEntries(new FormData(screen.getByRole('form') as HTMLFormElement))).toEqual({ name: 'node', secret: 'test', note: 'details' });
});

it('单选保留原生分组、键盘切换和滑块数值事件', async () => {
  const change = vi.fn();
  render(<><Label><Radio name="scope" value="one" defaultChecked />当前设备</Label><Label><Radio name="scope" value="all" />全部设备</Label><Slider aria-label="采样时长" min={1} max={60} defaultValue={10} onChange={change} /></>);
  screen.getByRole('radio', { name: '当前设备' }).focus();
  await userEvent.keyboard('{ArrowRight}');
  expect(screen.getByRole('radio', { name: '全部设备' })).toBeChecked();
  expect(screen.getByRole('radio', { name: '当前设备' })).not.toBeChecked();
  fireEvent.change(screen.getByRole('slider'), { target: { value: '20' } });
  expect(change).toHaveBeenCalledOnce();
  expect(screen.getByRole('slider')).toHaveValue('20');
});
