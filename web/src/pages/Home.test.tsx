import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { createSession, listModels, type ModelSelection } from '@/api/chat';
import { saveLLMDefault } from '@/api/settings';
import { useModelSelection } from '@/store/modelSelection';
import Home from './Home';

const permissions = vi.hoisted(() => ({ isAdmin: true }));
vi.mock('@/store/me', () => ({ usePermissions: () => permissions }));
vi.mock('@/lib/assistantPage', () => ({ useAssistantPage: vi.fn() }));
vi.mock('@/api/chat', () => ({ createSession: vi.fn(), listModels: vi.fn() }));
vi.mock('@/api/settings', () => ({ saveLLMDefault: vi.fn() }));
vi.mock('@/api/devices', () => ({ listDevices: () => Promise.resolve({ items: [], total: 1 }) }));
vi.mock('@/components/StatusRow', () => ({ StatusRow: () => null }));
vi.mock('@/components/ChatInput', () => ({
  ChatInput: ({ selectedModel, onModelChange, disabled, onSubmit }: {
    selectedModel: ModelSelection | null; onModelChange(value: ModelSelection): void; disabled: boolean; onSubmit(value: { text: string; attachments: File[] }): void;
  }) => <div>
    <output data-testid="selection">{JSON.stringify(selectedModel)}</output>
    <button disabled={disabled} onClick={() => onModelChange({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'high' })}>Pick High</button>
    <button disabled={disabled} onClick={() => onModelChange({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: '' })}>Pick Default</button>
    <button disabled={disabled} onClick={() => onModelChange({ provider: 'openai', model: 'gpt-4o', reasoning_effort: '' })}>Pick plain model</button>
    <button disabled={disabled} onClick={() => onSubmit({ text: 'test', attachments: [] })}>Start</button>
  </div>,
}));

let saved: ModelSelection;
beforeEach(() => {
  vi.clearAllMocks(); permissions.isAdmin = true;
  localStorage.setItem('ongrid-locale', 'en-US');
  useModelSelection.getState().setSelected(null);
  saved = { provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'low' };
  vi.mocked(listModels).mockImplementation(async () => ({ providers: [{ id: 'openai', label: 'OpenAI', models: ['gpt-6.1-terra', 'gpt-4o'] }], default: { ...saved } }));
  vi.mocked(saveLLMDefault).mockImplementation(async value => { saved = value; return value; });
  vi.mocked(createSession).mockResolvedValue({ id: 'created' } as Awaited<ReturnType<typeof createSession>>);
});
afterEach(cleanup);
const show = () => render(<MemoryRouter><Home /></MemoryRouter>);
async function loaded() { await waitFor(() => expect(screen.getByRole('button', { name: 'Pick High' })).toBeEnabled()); }

it('persists an effort-only change and reloads the server default over stale browser state', async () => {
  const user = userEvent.setup(); show(); await loaded();
  await user.click(screen.getByRole('button', { name: 'Pick High' }));
  await waitFor(() => expect(saveLLMDefault).toHaveBeenCalledWith({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: 'high' }));
  await loaded(); cleanup();
  act(() => useModelSelection.getState().setSelected({ ...saved, reasoning_effort: 'low' }));
  show(); await loaded();
  expect(screen.getByTestId('selection')).toHaveTextContent('"reasoning_effort":"high"');
  await user.click(screen.getByRole('button', { name: 'Start' }));
  expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ reasoning_effort: 'high' }));
});

it('restores the saved selection and reports a failed save', async () => {
  vi.mocked(saveLLMDefault).mockRejectedValueOnce(new Error('Save failed'));
  const user = userEvent.setup(); show(); await loaded();
  await user.click(screen.getByRole('button', { name: 'Pick High' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Save failed');
  expect(screen.getByTestId('selection')).toHaveTextContent('"reasoning_effort":"low"');
});

it('clears the effort when switching to an ordinary model', async () => {
  const user = userEvent.setup(); show(); await loaded();
  await user.click(screen.getByRole('button', { name: 'Pick plain model' }));
  await waitFor(() => expect(saveLLMDefault).toHaveBeenCalledWith({ provider: 'openai', model: 'gpt-4o', reasoning_effort: '' }));
});

it('keeps non-admin choices scoped to their next conversation', async () => {
  permissions.isAdmin = false;
  const user = userEvent.setup(); show(); await loaded();
  await user.click(screen.getByRole('button', { name: 'Pick High' }));
  await user.click(screen.getByRole('button', { name: 'Start' }));
  expect(saveLLMDefault).not.toHaveBeenCalled();
  expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ reasoning_effort: 'high' }));
});

it('sends an explicit default for non-admin session creation without changing the server default', async () => {
  permissions.isAdmin = false;
  saved.reasoning_effort = 'high';
  const user = userEvent.setup(); show(); await loaded();
  await user.click(screen.getByRole('button', { name: 'Pick plain model' }));
  await user.click(screen.getByRole('button', { name: 'Pick Default' }));
  await user.click(screen.getByRole('button', { name: 'Start' }));
  expect(saveLLMDefault).not.toHaveBeenCalled();
  expect(createSession).toHaveBeenCalledWith(expect.objectContaining({ provider: 'openai', model: 'gpt-6.1-terra', reasoning_effort: '' }));
});
