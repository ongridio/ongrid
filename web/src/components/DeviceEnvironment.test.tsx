import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { DeviceEnvironment } from './DeviceEnvironment';
import { getDeviceEnvironment, setDeviceEnvironment } from '@/api/devices';

vi.mock('@/api/devices', () => ({ getDeviceEnvironment: vi.fn(), setDeviceEnvironment: vi.fn() }));
vi.mock('@/components/autoapm/useAutoAPMOptions', () => ({ useAutoAPMOptions: () => ({ options: { environments: ['production', 'staging'], namespaces: [] }, error: '' }) }));
vi.mock('@/i18n/locale', () => ({ useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
const inherited = { environment: '', effective_environment: 'production', inherited_environment: 'production', cluster_name: 'prod-hosts', source: 'cluster' as const };
beforeEach(() => { vi.clearAllMocks(); vi.mocked(getDeviceEnvironment).mockResolvedValue(inherited); });

it('shares the device setting, preserves a failed draft and can restore inheritance', async () => {
  const user = userEvent.setup();
  vi.mocked(setDeviceEnvironment).mockRejectedValueOnce(new Error('save failed')).mockResolvedValueOnce({ ...inherited, environment: 'staging', effective_environment: 'staging', source: 'device' }).mockResolvedValueOnce(inherited);
  render(<DeviceEnvironment deviceId={650} deviceName="Orders" canEdit />);
  const trigger = await screen.findByRole('button', { name: 'Set default environment for Orders' });
  expect(trigger).toHaveTextContent('production');
  expect(trigger).toHaveAttribute('title', 'Inherited from prod-hosts');
  await user.click(trigger);
  const field = screen.getByLabelText('Device default environment');
  expect(field).toHaveValue('');
  await user.click(field);
  await user.click(await screen.findByRole('option', { name: 'staging' }));
  await user.click(screen.getByRole('button', { name: 'Save' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('save failed');
  expect(field).toHaveValue('staging');
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
  expect(trigger).toHaveTextContent('staging');
  expect(setDeviceEnvironment).toHaveBeenLastCalledWith(650, 'staging');
  await user.click(trigger);
  await user.click(screen.getByRole('button', { name: 'Use cluster default' }));
  await user.click(screen.getByRole('button', { name: 'Save' }));
  await waitFor(() => expect(trigger).toHaveTextContent('production'));
  expect(setDeviceEnvironment).toHaveBeenLastCalledWith(650, '');
});

it('shows an unset independent host without exposing write controls to readers', async () => {
  vi.mocked(getDeviceEnvironment).mockResolvedValue({ environment: '', effective_environment: '', inherited_environment: '', cluster_name: '', source: 'unset' });
  render(<DeviceEnvironment deviceId={650} deviceName="Orders" canEdit={false} />);
  expect(await screen.findByText('Not set')).toBeInTheDocument();
  expect(screen.queryByRole('button')).not.toBeInTheDocument();
});
