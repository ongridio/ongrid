import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ModelDropdown } from './ChatModelControls';
import type { LLMProvider, ModelSelection } from '@/api/chat';

const providers: LLMProvider[] = [{
  id: 'openai', label: 'OpenAI', models: ['gpt-6.1-terra', 'gpt-4o'],
  reasoning_efforts: { 'gpt-6.1-terra': ['none', 'low', 'medium', 'high', 'xhigh', 'max'] },
}, {
  id: 'deepseek', label: 'DeepSeek', models: ['deepseek-v4-flash'],
  reasoning_efforts: { 'deepseek-v4-flash': ['none', 'low', 'high', 'max'] },
}, {
  id: 'xiaomi', label: 'MiMo', models: ['mimo-v2.6-flash'],
  reasoning_efforts: { 'mimo-v2.6-flash': ['none', 'enabled'] },
}];
beforeEach(() => localStorage.setItem('ongrid-locale', 'en-US'));
afterEach(cleanup);

it('shows model-specific efforts without the off choice', async () => {
  const user = userEvent.setup();
  const onPick = vi.fn();
  const view = (selected: ModelSelection) => <MemoryRouter><ModelDropdown providers={providers} selected={selected} onPick={onPick}/></MemoryRouter>;
  render(view({ provider: 'openai', model: 'gpt-6.1-terra' }));
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' })); });
  expect(screen.queryByRole('option', { name: 'Off' })).not.toBeInTheDocument();
  await act(async () => { await user.click(await screen.findByRole('option', { name: 'High' })); });
  expect(onPick).toHaveBeenLastCalledWith({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'high' });

  cleanup();
  render(view({ provider: 'deepseek', model: 'deepseek-v4-flash' }));
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' })); });
  expect(screen.queryByRole('option', { name: 'Medium' })).not.toBeInTheDocument();
  expect(screen.queryByRole('option', { name: 'Off' })).not.toBeInTheDocument();
  await act(async () => { await user.click(await screen.findByRole('option', { name: 'Low' })); });
  expect(onPick).toHaveBeenLastCalledWith({ provider: 'deepseek', model: 'deepseek-v4-flash', reasoning_effort: 'low' });

  cleanup();
  render(view({ provider: 'xiaomi', model: 'mimo-v2.6-flash' }));
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' })); });
  expect(screen.getAllByRole('option').map(option => option.textContent)).toEqual(['Default', 'On']);

  cleanup();
  render(view({ provider: 'openai', model: 'gpt-4o' }));
  expect(screen.queryByRole('combobox', { name: 'Reasoning effort' })).not.toBeInTheDocument();
});

it('shows a legacy off preference as the provider default', async () => {
  const user = userEvent.setup();
  const onPick = vi.fn();
  render(<MemoryRouter><ModelDropdown providers={providers} selected={{ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'none' }} onPick={onPick}/></MemoryRouter>);
  expect(screen.getByRole('combobox', { name: 'Reasoning effort' })).toHaveTextContent('Default');
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Reasoning effort' })); });
  expect(screen.queryByRole('option', { name: 'Off' })).not.toBeInTheDocument();
  await act(async () => { await user.click(await screen.findByRole('option', { name: 'High' })); });
  expect(onPick).toHaveBeenCalledWith({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'high' });
});

it('keeps effort labels in English in the Chinese interface', async () => {
  const user = userEvent.setup();
  localStorage.setItem('ongrid-locale', 'zh-CN');
  render(<MemoryRouter><ModelDropdown providers={providers} selected={{ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'high' }}/></MemoryRouter>);
  const picker = screen.getByRole('combobox', { name: '推理级别' });
  expect(picker).toHaveTextContent('High');
  await act(async () => { await user.click(picker); });
  expect(screen.getAllByRole('option').map(option => option.textContent)).toEqual(['Default', 'Low', 'Medium', 'High', 'Extra high', 'Max']);
});
