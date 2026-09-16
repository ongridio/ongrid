import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { AutoAPMManagement } from './AutoAPMManagement';
import { listSettings, setSetting } from '@/api/settings';
import { loadK8sEdgeAttachments } from '@/pages/kubernetes/edgeAttachments';
import { listEdges } from '@/api/edges';
import { listEdgePlugins, setEdgePlugin } from '@/api/integrations';
vi.mock('@/pages/kubernetes/edgeAttachments', () => ({ loadK8sEdgeAttachments: vi.fn().mockResolvedValue({}) }));
vi.mock('@/api/settings', () => ({ listSettings: vi.fn(), setSetting: vi.fn() }));
vi.mock('@/api/edges', () => ({ listEdges: vi.fn() }));
vi.mock('@/api/integrations', () => ({ listEdgePlugins: vi.fn(), setEdgePlugin: vi.fn(), getAutoAPMOptions: vi.fn().mockResolvedValue({ environments: [], namespaces: [] }) }));
vi.mock('@/api/topology', () => ({ listAllNodes: vi.fn().mockResolvedValue([]) }));
vi.mock('@/i18n/locale', async importOriginal => ({ ...await importOriginal<typeof import('@/i18n/locale')>(), useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(listSettings).mockResolvedValue({ items: [], total: 0 });
  vi.mocked(listEdges).mockResolvedValue({ items: [{ id: 67, name: 'Ubuntu', device_id: 650, status: 'online', roles: [], access_key_id: '', last_seen_at: null }], total: 1 });
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 1 }] } }] });
});
it('uses one persisted global switch and configures a discovered target from Services', async () => {
  const user = userEvent.setup();
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  const toggle = screen.getByRole('switch', { name: 'Global discovery' });
  await waitFor(() => expect(toggle).not.toHaveAttribute('aria-disabled', 'true'));
  await act(async () => { await user.click(toggle); });
  expect(setSetting).toHaveBeenCalledWith('platform', 'auto_apm_enabled', 'true', false);
  expect(setEdgePlugin).not.toHaveBeenCalled();
  await screen.findByText('Discovering');
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Configure capture' })); });
  expect(screen.getAllByRole('switch', { hidden: true })).toHaveLength(1);
  await act(async () => { await user.click(await screen.findByRole('checkbox', { name: 'Capture /opt/orders:8080' })); });
  vi.mocked(setEdgePlugin).mockResolvedValue({ plugin_name: 'autoapm', enabled: true });
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(setEdgePlugin).toHaveBeenCalledWith(67, 'autoapm', { enabled: true, spec: { targets: [{ executable: '/opt/orders', port: 8080, service_name: 'orders', service_namespace: undefined, environment: undefined }], environment: undefined } });
});
it('retains the global state when saving fails', async () => {
  const user = userEvent.setup();
  vi.mocked(setSetting).mockRejectedValue(new Error('permission denied'));
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  const toggle = screen.getByRole('switch');
  await waitFor(() => expect(toggle).not.toHaveAttribute('aria-disabled', 'true'));
  await act(async () => { await user.click(toggle); });
  expect(await screen.findByRole('alert')).toHaveTextContent('permission denied');
  expect(toggle).not.toBeChecked();
});
it('distinguishes a failed device request from an empty inventory', async () => {
  vi.mocked(listEdges).mockRejectedValue(new Error('network unavailable'));
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load devices');
  expect(screen.queryByText('No devices connected')).not.toBeInTheDocument();
});
it('does not present stale discovery as current results', async () => {
  vi.mocked(listSettings).mockResolvedValue({ items: [{ key: 'auto_apm_enabled', value: 'true', category: 'platform', sensitive: false, updated_at: new Date().toISOString() }], total: 1 });
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: '2000-01-01T00:00:00Z', candidates: [{ executable: '/opt/stale', port: 8080, pid: 1 }] } }] });
  render(<AutoAPMManagement canEdit={false} initialEdgeId={null} />);
  expect(await screen.findByText('Awaiting report')).toBeInTheDocument();
  expect(screen.queryByText('/opt/stale')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Configure capture' })).toBeDisabled();
});

it('shows one preferred collector per device and retains devices that are offline', async () => {
  const base = { roles: [], access_key_id: '', last_seen_at: null };
  vi.mocked(listEdges).mockResolvedValue({ items: [
    { ...base, id: 66, device_id: 650, name: 'Retired collector', status: 'offline', last_seen_at: '2026-09-16T06:00:00Z' },
    { ...base, id: 65, device_id: 650, name: 'Older online collector', status: 'online', last_seen_at: '2026-09-14T06:00:00Z' },
    { ...base, id: 67, device_id: 650, name: 'Ubuntu', status: 'online', last_seen_at: '2026-09-15T06:00:00Z' },
    { ...base, id: 64, device_id: 122, name: 'Offline device', status: 'offline' },
    { ...base, id: 63, name: 'Controller', status: 'online' },
  ], total: 5 });
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  await screen.findByText('2 devices');
  await waitFor(() => expect(screen.getAllByRole('button', { name: 'Configure capture' }).every(button => !button.hasAttribute('disabled'))).toBe(true));
  expect(screen.getAllByRole('row')).toHaveLength(3);
  expect(screen.getByText('Offline device')).toBeInTheDocument();
  expect(screen.queryByText(/Retired collector|Older online collector|Controller/)).not.toBeInTheDocument();
  expect(listEdgePlugins).toHaveBeenCalledWith(67);
  expect(listEdgePlugins).toHaveBeenCalledWith(64);
  expect(listEdgePlugins).not.toHaveBeenCalledWith(66);
});
it('resolves an old collector deep link to the current device', async () => {
  const base = { device_id: 650, roles: [], access_key_id: '', last_seen_at: null };
  vi.mocked(listEdges).mockResolvedValue({ items: [
    { ...base, id: 66, name: 'Retired collector', status: 'offline' },
    { ...base, id: 67, name: 'Ubuntu', status: 'online' },
  ], total: 2 });
  render(<AutoAPMManagement canEdit initialEdgeId="66" />);
  expect(await screen.findByText('1 devices')).toBeInTheDocument();
  expect(screen.getByRole('searchbox', { name: 'Search devices' })).toHaveValue('650');
  expect(screen.queryByText(/Retired collector/)).not.toBeInTheDocument();
  await waitFor(() => expect(listEdgePlugins).toHaveBeenCalledWith(67));
});

it('keeps Kubernetes nodes out of host discovery', async () => {
  vi.mocked(loadK8sEdgeAttachments).mockResolvedValue({ 67: [{ kind: 'k8s-node', clusterId: 1, clusterName: 'test', clusterMode: 'full-node' }] });
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  expect(await screen.findByText('No devices connected')).toBeInTheDocument();
  expect(listEdgePlugins).not.toHaveBeenCalled();
});
