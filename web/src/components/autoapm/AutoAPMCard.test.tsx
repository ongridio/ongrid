import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { AutoAPMCard } from './AutoAPMCard';
import { listEdgePlugins, getAutoAPMOptions } from '@/api/integrations';
vi.mock('@/api/integrations', () => ({ listEdgePlugins: vi.fn(), getAutoAPMOptions: vi.fn() }));
vi.mock('@/api/topology', () => ({ listAllNodes: vi.fn().mockResolvedValue([{ props: { environment: 'production' } }]) }));
vi.mock('@/i18n/locale', () => ({ useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
beforeEach(() => { vi.clearAllMocks(); vi.mocked(getAutoAPMOptions).mockResolvedValue({ environments: ['test'], namespaces: ['commerce'] }); });
it('has no per-device switch and saves targets while the global gate is off', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockResolvedValue(undefined);
  const spec = { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders' }] };
  render(<AutoAPMCard edgeId={1} deviceName="Ubuntu" online onClose={vi.fn()} row={{ plugin_name: 'autoapm', enabled: false, spec }} onSave={save} />);
  expect(listEdgePlugins).not.toHaveBeenCalled();
  expect(screen.queryByRole('switch')).not.toBeInTheDocument();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(save).toHaveBeenCalledWith({ enabled: false, spec });
});
it('adds a discovered listener only after explicitly saving and retains draft on error', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockRejectedValue(new Error('save failed'));
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 12 }] } }] });
  render(<AutoAPMCard edgeId={1} deviceName="Ubuntu" online onClose={vi.fn()} row={{ plugin_name: 'autoapm', enabled: true }} onSave={save} />);
  const add = await screen.findByRole('checkbox', { name: 'Capture /opt/orders:8080' });
  await act(async () => { await user.click(add); });
  expect(save).not.toHaveBeenCalled();
  expect(screen.getByRole('checkbox', { name: 'Capture /opt/orders:8080' })).toHaveFocus();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  await waitFor(() => expect(save).toHaveBeenCalledWith({ enabled: true, spec: { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders', service_namespace: undefined, environment: undefined }], environment: undefined } }));
  expect(await screen.findByRole('alert')).toHaveTextContent('save failed');
  expect(screen.getByLabelText('Service name 1')).toHaveValue('orders');
});
it('keeps undiscovered saved targets editable while offline', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockResolvedValue(undefined);
  const spec = { environment: 'production', sample_ratio: 0, tls_insecure_skip_verify: true, targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders' }] };
  render(<AutoAPMCard edgeId={1} deviceName="Ubuntu" online={false} onClose={vi.fn()} row={{ plugin_name: 'autoapm', enabled: true, spec }} onSave={save} />);
  expect(listEdgePlugins).not.toHaveBeenCalled();
  await user.click(screen.getByRole('tab', { name: 'Selected targets (1)' }));
  expect(screen.getByLabelText('Executable path 1')).toHaveValue('/opt/orders');
  await user.clear(screen.getByLabelText('Service name 1'));
  await user.type(screen.getByLabelText('Service name 1'), 'orders-renamed');
  await user.click(screen.getByRole('button', { name: 'Save capture settings' }));
  expect(save).toHaveBeenCalledWith({ enabled: true, spec: { ...spec, targets: [{ ...spec.targets[0], service_name: 'orders-renamed' }] } });
});
it('cancels selection without persisting and does not duplicate a saved candidate', async () => {
  const user = userEvent.setup(); const save = vi.fn(); const close = vi.fn();
  const health = { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 12 }] };
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health }] });
  render(<AutoAPMCard edgeId={1} deviceName="Ubuntu" online onClose={close} row={{ plugin_name: 'autoapm', enabled: true, health, spec: { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders' }] } }} onSave={save} />);
  expect(screen.getAllByRole('checkbox', { name: 'Capture /opt/orders:8080' })).toHaveLength(1);
  await user.click(screen.getByRole('checkbox', { name: 'Capture /opt/orders:8080' }));
  await user.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(close).toHaveBeenCalled();
  expect(save).not.toHaveBeenCalled();
});

it('reveals an incomplete target hidden by search instead of submitting it', async () => {
  const user = userEvent.setup(); const save = vi.fn();
  render(<AutoAPMCard edgeId={1} deviceName="Ubuntu" online={false} onClose={vi.fn()} row={{ plugin_name: 'autoapm', enabled: false }} onSave={save} />);
  await user.click(screen.getByRole('button', { name: 'Add manually' }));
  await user.type(screen.getByRole('searchbox', { name: 'Search processes' }), 'hide-empty-target');
  await user.click(screen.getByRole('button', { name: 'Save capture settings' }));
  expect(save).not.toHaveBeenCalled();
  expect(screen.getByRole('alert')).toHaveTextContent('Complete each target');
  expect(screen.getByLabelText('Executable path 1')).toHaveValue('');
});


it('reuses saved values and restores cluster inheritance without persisting the default', async () => {
  const user = userEvent.setup(); const save = vi.fn().mockResolvedValue(undefined);
  render(<AutoAPMCard edgeId={1} deviceName="Worker" online={false} onClose={vi.fn()} row={{ plugin_name: 'autoapm', enabled: true, defaults: { environment: 'production', cluster_name: 'prod-cluster' }, spec: { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders' }] } }} onSave={save} />);
  const environment = screen.getByLabelText('Service environment 1');
  expect(environment).toHaveValue('');
  expect(environment).toHaveAttribute('placeholder', 'Inherit: production');
  await user.click(environment);
  await user.click(await screen.findByRole('option', { name: 'test' }));
  expect(environment).toHaveValue('test');
  const namespace = screen.getByLabelText('Service namespace 1');
  await user.click(namespace);
  await user.click(await screen.findByRole('option', { name: 'commerce' }));
  expect(namespace).toHaveValue('commerce');
  await user.click(screen.getByRole('button', { name: 'Save capture settings' }));
  expect(save.mock.calls[0][0].spec.targets[0]).toMatchObject({ environment: 'test', service_namespace: 'commerce' });
  await user.click(screen.getByRole('button', { name: 'Use default' }));
  await user.click(screen.getByRole('button', { name: 'Save capture settings' }));
  expect(save.mock.calls[1][0].spec.environment).toBeUndefined();
  expect(save.mock.calls[1][0].spec.targets[0].environment).toBeUndefined();
});
