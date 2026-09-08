import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { expect, it, vi } from 'vitest';
import { FilterField } from './FilterField';
import { Select } from './Select';
import { Input } from './Input';

it('组合筛选保留独立字段名称、标签焦点和选择事件，不意外提交查询', async () => {
  const submit = vi.fn((event: React.FormEvent) => event.preventDefault());
  function Example() {
    const [value, setValue] = useState('');
    return <form onSubmit={submit}>
      <FilterField label="角色"><Select label="角色" value={value} onValueChange={setValue} options={[{ value: '', label: '所有角色' }, { value: 'server', label: '服务器' }]} /></FilterField>
      <FilterField label="Trace ID"><Input aria-label="Trace ID" /></FilterField>
    </form>;
  }
  render(<Example />);
  const user = userEvent.setup();
  await user.click(screen.getByText('Trace ID'));
  expect(screen.getByRole('textbox', { name: 'Trace ID' })).toHaveFocus();
  await user.click(screen.getByRole('combobox', { name: '角色' }));
  await user.click(await screen.findByRole('option', { name: '服务器' }));
  expect(screen.getByRole('combobox', { name: '角色' })).toHaveTextContent('服务器');
  expect(submit).not.toHaveBeenCalled();
});
