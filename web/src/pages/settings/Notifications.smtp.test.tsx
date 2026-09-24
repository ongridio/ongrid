import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import Notifications from './Notifications';

const api = vi.hoisted(() => ({ listChannels: vi.fn(), createChannel: vi.fn(), updateChannel: vi.fn(), deleteChannel: vi.fn(), testChannel: vi.fn() }));
vi.mock('@/api/alerts', () => api);

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.setItem('ongrid-locale', 'en-US');
  api.listChannels.mockResolvedValue({ items: [], total: 0 });
  api.createChannel.mockResolvedValue({ id: 1 });
  api.updateChannel.mockResolvedValue({ id: 1 });
});

it('creates an SMTP channel with multiple recipients and no webhook endpoint', async () => {
  const user = userEvent.setup();
  render(<Notifications />);
  await user.click(await screen.findByText('Add notification channel'));
  await user.click(screen.getByRole('button', { name: /Email \(SMTP\)/ }));
  await user.type(screen.getByPlaceholderText('primary-smtp'), 'ops-email');
  await user.type(screen.getByPlaceholderText('smtp.example.com'), 'smtp.example.test');
  await user.type(screen.getByPlaceholderText('alerts@example.com'), 'alerts@example.test');
  await user.type(screen.getByPlaceholderText('ops@example.com, oncall@example.com'), 'ops@example.test; second@example.test');
  await user.type(screen.getByPlaceholderText('SMTP password or app password'), 'test-password');
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(api.createChannel).toHaveBeenCalledWith(expect.objectContaining({
    type: 'smtp', endpoint: '', secret: 'test-password',
    smtp: { host: 'smtp.example.test', port: 587, username: '', from: 'alerts@example.test', to: ['ops@example.test', 'second@example.test'], tls_mode: 'starttls' },
  })));
});

it('prefills SMTP configuration for editing while leaving the password empty', async () => {
  const smtp = { host: 'smtp.example.test', port: 465, from: 'alerts@example.test', to: ['ops@example.test'], username: 'user', tls_mode: 'tls' };
  api.listChannels.mockResolvedValue({ items: [{ id: 1, name: 'ops-email', type: 'smtp', enabled: true, smtp }], total: 1 });
  const user = userEvent.setup();
  render(<Notifications />);
  await user.click(await screen.findByRole('button', { name: 'Edit' }));
  expect(screen.getByPlaceholderText('smtp.example.com')).toHaveValue(smtp.host);
  expect(screen.getByPlaceholderText('Leave empty to keep the existing value; enter - to clear')).toHaveValue('');
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(api.updateChannel).toHaveBeenCalledWith(1, expect.objectContaining({ smtp, secret: '' })));
});
