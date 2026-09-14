import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { AutoAPMCard } from './AutoAPMCard';
import { listEdgePlugins } from '@/api/integrations';
vi.mock('@/api/integrations', () => ({ listEdgePlugins: vi.fn() }));
vi.mock('@/i18n/locale', () => ({ useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
beforeEach(() => { vi.clearAllMocks(); });
it('has no per-device switch and saves targets while the global gate is off', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockResolvedValue(undefined);
  const spec = { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders' }] };
  render(<AutoAPMCard edgeId={1} row={{ plugin_name: 'autoapm', enabled: false, spec }} onSave={save} />);
  expect(listEdgePlugins).not.toHaveBeenCalled();
  expect(screen.queryByRole('switch')).not.toBeInTheDocument();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(save).toHaveBeenCalledWith({ enabled: false, spec });
});
it('adds a discovered listener only after explicitly saving and retains draft on error', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockRejectedValue(new Error('save failed'));
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 12 }] } }] });
  render(<AutoAPMCard edgeId={1} row={{ plugin_name: 'autoapm', enabled: true }} onSave={save} />);
  const add = await screen.findByRole('button', { name: 'Add target' });
  await act(async () => { await user.click(add); });
  expect(save).not.toHaveBeenCalled();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  await waitFor(() => expect(save).toHaveBeenCalledWith({ enabled: true, spec: { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders', service_namespace: '' }] } }));
  expect(await screen.findByRole('alert')).toHaveTextContent('save failed');
  expect(screen.getByLabelText('Executable path')).toHaveValue('/opt/orders');
});
