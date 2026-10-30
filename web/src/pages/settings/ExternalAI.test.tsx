import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import ExternalAIPage from './ExternalAI';
import { createMcpToken, listMcpTokens, deleteMcpToken, updateMcpToken } from '@/api/mcpTokens';

vi.mock('@/api/mcpTokens', () => ({ createMcpToken: vi.fn(), listMcpTokens: vi.fn(), deleteMcpToken: vi.fn(), updateMcpToken: vi.fn() }));
const token = { public_id: 'test-public-id', name: 'Existing client', scopes: ['devices:read'], created_at: '2026-01-01T00:00:00Z', expires_at: null };
const list = { items: [token], total: 1, scopes: ['devices:read', 'devices:write', 'alerts:read', 'host:execute'], enabled: true, writes_enabled: true, endpoint: 'https://example.test/api/v1/integrations/mcp' };

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.setItem('ongrid-locale', 'en-US');
  vi.mocked(listMcpTokens).mockResolvedValue(list);
  vi.mocked(createMcpToken).mockResolvedValue({ item: token, token: 'fixture-only-secret' });
  vi.mocked(deleteMcpToken).mockResolvedValue();
  vi.mocked(updateMcpToken).mockResolvedValue({ item: token });
});

it('creates a never-expiring scoped token and clears the one-time secret when closed', async () => {
  const user = userEvent.setup();
  render(<ExternalAIPage />);
  await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Create token' }));
  await user.type(screen.getByLabelText('Name'), 'My client');
  await user.click(screen.getByRole('combobox', { name: 'Expiry' }));
  await user.click(await screen.findByRole('option', { name: 'Never' }));
  await user.click(screen.getByRole('checkbox', { name: 'Devices: Read' }));
  await user.click(screen.getByRole('checkbox', { name: 'Host services: Execute' }));
  expect(screen.getByRole('checkbox', { name: 'Devices: Read' })).toBeChecked();
  await user.click(screen.getByRole('button', { name: 'Create' }));
  await waitFor(() => expect(createMcpToken).toHaveBeenCalledWith({ name: 'My client', scopes: ['alerts:read', 'host:execute', 'devices:read'], expires_at: null }));
  expect(await screen.findByLabelText('One-time token')).toHaveValue('fixture-only-secret');
  expect(JSON.stringify(localStorage)).not.toContain('fixture-only-secret');
  await user.click(screen.getByRole('button', { name: 'Saved, close' }));
  expect(screen.queryByLabelText('One-time token')).not.toBeInTheDocument();
});

it('rejects past custom expiry and keeps the form when creation fails', async () => {
  const user = userEvent.setup();
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Create token' }));
  await user.type(screen.getByLabelText('Name'), 'Retained name');
  await user.click(screen.getByRole('combobox', { name: 'Expiry' }));
  await user.click(await screen.findByRole('option', { name: 'Custom' }));
  fireEvent.change(screen.getByLabelText('Expiry time (local timezone)'), { target: { value: '2020-01-01T12:00' } });
  await user.click(screen.getByRole('button', { name: 'Create' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Choose a future expiry time.');
  expect(createMcpToken).not.toHaveBeenCalled();
  vi.mocked(createMcpToken).mockRejectedValue(new Error('offline'));
  fireEvent.change(screen.getByLabelText('Expiry time (local timezone)'), { target: { value: '2099-01-01T12:00' } });
  await user.click(screen.getByRole('button', { name: 'Create' }));
  await waitFor(() => expect(createMcpToken).toHaveBeenCalledOnce());
  expect(screen.getByLabelText('Name')).toHaveValue('Retained name');
  expect(screen.getByRole('alert')).toHaveTextContent('Could not create token');
});

it('does not delete on cancel and removes the token after confirmation', async () => {
  const user = userEvent.setup();
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Delete' }));
  await user.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(deleteMcpToken).not.toHaveBeenCalled();
  await user.click(screen.getByRole('button', { name: 'Delete' }));
  vi.mocked(listMcpTokens).mockResolvedValue({ ...list, items: [], total: 0 });
  await user.click(screen.getByRole('button', { name: 'Confirm' }));
  await waitFor(() => expect(deleteMcpToken).toHaveBeenCalledWith('test-public-id'));
  expect(await screen.findByText('No access tokens yet')).toBeInTheDocument();
  expect(screen.queryByText('Existing client')).not.toBeInTheDocument();
});

it('edits permissions using the same token and preserves its exact expiry', async () => {
  const user = userEvent.setup();
  const expiresAt = '2099-01-01T12:34:56.123Z';
  vi.mocked(listMcpTokens).mockResolvedValue({ ...list, items: [{ ...token, expires_at: expiresAt }] });
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Edit' }));
  expect(screen.getByLabelText('Name')).toHaveValue('Existing client');
  expect(screen.getByRole('checkbox', { name: 'Devices: Read' })).toBeChecked();
  await user.click(screen.getByRole('checkbox', { name: 'Devices: Write' }));
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(updateMcpToken).toHaveBeenCalledWith('test-public-id', { name: 'Existing client', scopes: ['devices:read', 'devices:write'], expires_at: expiresAt }));
  expect(createMcpToken).not.toHaveBeenCalled();
  expect(screen.queryByLabelText('One-time token')).not.toBeInTheDocument();
});

it('preserves editing input on failure and cancels without saving', async () => {
  const user = userEvent.setup();
  vi.mocked(updateMcpToken).mockRejectedValue(new Error('offline'));
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Edit' }));
  await user.type(screen.getByLabelText('Name'), ' edited');
  await user.click(screen.getByRole('checkbox', { name: 'Devices: Write' }));
  await user.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not save token');
  expect(screen.getByLabelText('Name')).toHaveValue('Existing client edited');
  expect(screen.getByRole('checkbox', { name: 'Devices: Write' })).toBeChecked();
  await user.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(updateMcpToken).toHaveBeenCalledTimes(1);
});

it('shows platform modules and removes dependent write scopes when read access is removed', async () => {
  const user = userEvent.setup();
  vi.mocked(listMcpTokens).mockResolvedValue({ ...list, scopes: [...list.scopes, 'flows:read', 'flows:execute', 'iam:read', 'iam:write', 'credentials:read'] });
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: 'Edit' }));
  await user.click(screen.getByRole('checkbox', { name: 'Workflows: Execute' }));
  expect(screen.getByRole('checkbox', { name: 'Workflows: Read' })).toBeChecked();
  await user.click(screen.getByRole('checkbox', { name: 'Workflows: Read' }));
  expect(screen.getByRole('checkbox', { name: 'Workflows: Execute' })).not.toBeChecked();
  await user.click(screen.getByRole('checkbox', { name: 'Users and organizations: Write' }));
  expect(screen.getByRole('checkbox', { name: 'Credentials: Read' })).not.toBeChecked();
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(updateMcpToken).toHaveBeenCalledWith('test-public-id', { name: 'Existing client', scopes: ['devices:read', 'iam:write', 'iam:read'], expires_at: null }));
});

it.each(['create', 'edit'])('selects and clears all visible capabilities in the %s form without submitting', async (mode) => {
  const user = userEvent.setup();
  const visibleScopes = [...list.scopes, 'approvals:read', 'audit:read'];
  vi.mocked(listMcpTokens).mockResolvedValue({ ...list, scopes: [...visibleScopes, 'future-module:read'] });
  render(<ExternalAIPage />); await screen.findByText('Existing client');
  await user.click(screen.getByRole('button', { name: mode === 'create' ? 'Create token' : 'Edit' }));
  if (mode === 'create') await user.type(screen.getByLabelText('Name'), 'All capabilities');
  expect(screen.getAllByRole('checkbox', { name: 'Operation approvals: Read' })).toHaveLength(1);
  expect(screen.getAllByRole('checkbox', { name: 'Audit logs: Read' })).toHaveLength(1);
  const selectAll = screen.getByRole('checkbox', { name: 'Select all' });
  expect(selectAll).toBePartiallyChecked();
  await user.click(selectAll);
  screen.getAllByRole('checkbox').forEach((checkbox) => expect(checkbox).toBeChecked());
  await user.click(screen.getByRole('checkbox', { name: 'Audit logs: Read' }));
  expect(selectAll).toBePartiallyChecked();
  await user.click(selectAll);
  expect(screen.getByRole('checkbox', { name: 'Audit logs: Read' })).toBeChecked();
  selectAll.focus();
  await user.keyboard('[Space]');
  screen.getAllByRole('checkbox').forEach((checkbox) => expect(checkbox).not.toBeChecked());
  const submitName = mode === 'create' ? 'Create' : 'Save';
  expect(screen.getByRole('button', { name: submitName })).toBeDisabled();
  expect(selectAll).not.toBePartiallyChecked();
  expect(createMcpToken).not.toHaveBeenCalled();
  expect(updateMcpToken).not.toHaveBeenCalled();
  await user.click(selectAll);
  await user.click(screen.getByRole('button', { name: submitName }));
  await waitFor(() => expect(mode === 'create' ? createMcpToken : updateMcpToken).toHaveBeenCalledOnce());
  const input = mode === 'create' ? vi.mocked(createMcpToken).mock.calls[0][0] : vi.mocked(updateMcpToken).mock.calls[0][1];
  expect(input.scopes.slice().sort()).toEqual(visibleScopes.slice().sort());
});


it.each(['https://example.test/api/v1/integrations/mcp', '/api/v1/integrations/mcp'])('copies a usable configuration with a token placeholder for %s', async (endpoint) => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, 'writeText');
  vi.mocked(listMcpTokens).mockResolvedValue({ ...list, endpoint });
  render(<ExternalAIPage />);
  await screen.findByText('Existing client');
  expect(screen.queryByRole('heading', { name: 'WorkBuddy' })).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Connection guide' }));
  expect(screen.getByRole('dialog', { name: 'Connection guide' })).toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Copy WorkBuddy configuration' }));
  expect(JSON.parse(writeText.mock.calls[0][0])).toEqual({ mcpServers: { ongrid: { type: 'streamableHttp', url: new URL(endpoint, window.location.origin).href, headers: { Authorization: 'Bearer <Token>' } } } });
  expect(screen.getByRole('status')).toHaveTextContent('Copied');
  expect(screen.getByRole('heading', { name: 'WorkBuddy' })).toBeInTheDocument();
  expect(screen.getByRole('dialog', { name: 'Connection guide' })).not.toHaveTextContent(/豆包|Doubao/);
  expect(screen.queryByRole('tab')).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'WorkBuddy documentation' })).toHaveAttribute('href', 'https://www.codebuddy.cn/docs/workbuddy/From-Beginner-to-Expert-Guide/Function-Description/Connector');
  await user.click(screen.getByRole('button', { name: 'Close' }));
  expect(screen.queryByRole('dialog', { name: 'Connection guide' })).not.toBeInTheDocument();
  expect(screen.queryByRole('heading', { name: 'WorkBuddy' })).not.toBeInTheDocument();
  expect(createMcpToken).not.toHaveBeenCalled();
  expect(updateMcpToken).not.toHaveBeenCalled();
});
