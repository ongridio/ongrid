import { MemoryRouter } from 'react-router-dom';
import type { ReactElement } from 'react';
import { fireEvent, render as testingRender, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import { AutoAPMManagement } from './AutoAPMManagement';
import { loadK8sEdgeAttachments } from '@/pages/kubernetes/edgeAttachments';
import { listEdges } from '@/api/edges';
import { listDevices } from '@/api/devices';
import { listNodes, listRelations } from '@/api/topology';
import { listEdgePlugins, setEdgePlugin } from '@/api/integrations';
vi.mock('@/pages/kubernetes/edgeAttachments', () => ({ loadK8sEdgeAttachments: vi.fn().mockResolvedValue({}) }));
vi.mock('@/api/edges', () => ({ listEdges: vi.fn() }));
vi.mock('@/api/devices', () => ({ listDevices: vi.fn(), getDeviceEnvironment: vi.fn().mockResolvedValue({ environment: "", effective_environment: "production", inherited_environment: "production", cluster_name: "prod-hosts", source: "cluster" }) }));
vi.mock('@/api/integrations', () => ({ listEdgePlugins: vi.fn(), setEdgePlugin: vi.fn(), getAutoAPMOptions: vi.fn().mockResolvedValue({ environments: [], namespaces: [] }) }));
vi.mock('@/api/topology', () => ({ listNodes: vi.fn(), listRelations: vi.fn() }));
vi.mock('@/i18n/locale', async importOriginal => ({ ...await importOriginal<typeof import('@/i18n/locale')>(), useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
const render = (ui: ReactElement) => testingRender(<MemoryRouter>{ui}</MemoryRouter>);
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(loadK8sEdgeAttachments).mockResolvedValue({});
  vi.mocked(listDevices).mockResolvedValue({ items: [], total: 0 });
  vi.mocked(listNodes).mockResolvedValue({ items: [], total: 0 });
  vi.mocked(listRelations).mockResolvedValue({ items: [], total: 0 });
  vi.mocked(listEdges).mockResolvedValue({ items: [{ id: 67, name: 'Ubuntu', device_id: 650, status: 'online', roles: [], access_key_id: '', last_seen_at: null }], total: 1 });
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, defaults: { environment: 'production', cluster_name: 'prod-hosts' }, health: { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 1 }] } }] });
});
it('links to a dedicated capture page without opening a dialog', async () => {
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: new Date().toISOString(), candidates: [{ executable: '/opt/orders', port: 8080, pid: 12 }, { executable: '/opt/orders', port: 9090, pid: 12 }] } }] });
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  await screen.findByText('Discovering');
  expect(screen.getByRole('columnheader', { name: 'Default environment' })).toBeInTheDocument();
  expect(await screen.findByRole('button', { name: 'Set default environment for Ubuntu' })).toHaveTextContent('production');
  expect(screen.getByRole('link', { name: 'Configure capture' })).toHaveAttribute('href', '/apm/capture/hosts/67');
  expect(within(screen.getAllByRole('row')[1]).getAllByRole('cell')[3]).toHaveTextContent('1');
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(setEdgePlugin).not.toHaveBeenCalled();
});
it('distinguishes a failed device request from an empty inventory', async () => {
  vi.mocked(listEdges).mockRejectedValue(new Error('network unavailable'));
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not load devices');
  expect(screen.queryByText('No devices connected')).not.toBeInTheDocument();
});
it('uses device identity and topology membership independently of collector names and environment defaults', async () => {
  vi.mocked(listDevices).mockResolvedValue({ items: [{ id: 650, node_id: 900, name: 'Orders host', hostname: 'orders-01', ip_address: '10.0.0.5' }], total: 1 });
  vi.mocked(listNodes).mockResolvedValue({ items: [{ id: 95, type: 'cluster', name: 'bare-metal-prod', created_at: '', updated_at: '' }], total: 1 });
  vi.mocked(listRelations).mockResolvedValue({ items: [{ id: 1, src_id: 900, dst_id: 95, type: 'member_of', created_at: '' }], total: 1 });
  render(<AutoAPMManagement canEdit initialEdgeId={null} />);
  expect(await screen.findByRole('link', { name: 'Orders host' })).toHaveAttribute('href', '/devices/650');
  expect(screen.queryByText('orders-01 · 10.0.0.5')).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'Cluster bare-metal-prod' })).toHaveAttribute('href', '/clusters/95');
  expect(screen.queryByRole('link', { name: 'Cluster prod-hosts' })).not.toBeInTheDocument();
  fireEvent.change(screen.getByRole('searchbox', { name: 'Search devices' }), { target: { value: 'bare-metal-prod' } });
  expect(screen.getByRole('link', { name: 'Orders host' })).toBeInTheDocument();
});
it('does not present stale discovery as current results', async () => {
  vi.mocked(listEdgePlugins).mockResolvedValue({ items: [{ plugin_name: 'autoapm', enabled: true, health: { state: 'running', reported_at: '2000-01-01T00:00:00Z', candidates: [{ executable: '/opt/stale', port: 8080, pid: 1 }] } }] });
  render(<AutoAPMManagement canEdit={false} initialEdgeId={null} />);
  expect(await screen.findByText('Awaiting report')).toBeInTheDocument();
  expect(screen.queryByText('/opt/stale')).not.toBeInTheDocument();
  expect(screen.getByRole('link', { name: 'View settings' })).toHaveAttribute('href', '/apm/capture/hosts/67');
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
  await waitFor(() => expect(screen.getAllByRole('link', { name: 'Configure capture' }).every(button => !button.hasAttribute('disabled'))).toBe(true));
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
