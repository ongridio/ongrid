import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, expect, it, vi } from 'vitest';
import { KubernetesAPM, KubernetesCapture } from './KubernetesAPM';
import * as api from '@/api/kubernetes';
vi.mock('@/api/kubernetes', () => ({
  listKubernetesClusters: vi.fn(), getKubernetesCluster: vi.fn(), getKubernetesClusterHealth: vi.fn(),
  getKubernetesCapture: vi.fn(), setKubernetesCapture: vi.fn(), listKubernetesWorkloads: vi.fn(),
}));
vi.mock('@/i18n/locale', async importOriginal => ({ ...await importOriginal<typeof import('@/i18n/locale')>(), useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
const cluster = { id: 1, name: 'prod-cluster', mode: 'full-node', status: 'online', created_at: '', updated_at: '' };
const defaults = { environment: 'production', cluster_name: 'prod-cluster' };
const rule = { namespace: 'shop', workload_kind: 'Deployment', workload_name: 'api' };
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.listKubernetesClusters).mockResolvedValue({ items: [{ id: 1, name: 'prod-cluster', mode: 'full-node', status: 'online', created_at: '', updated_at: '' }], total: 1 });
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { kubernetes: { rules: [] } }, defaults });
  vi.mocked(api.getKubernetesClusterHealth).mockResolvedValue({ degraded_workloads: 0, pending_pods: 0, crash_loop_back_off_pods: 0, oom_killed_pods: 0, image_pull_back_off_pods: 0, not_ready_nodes: 0, namespaces: [{ namespace: 'shop', workloads: 1, pods: 2, events: 0, warnings: 0 }] });
  vi.mocked(api.listKubernetesWorkloads).mockResolvedValue({ items: [{ id: 1, cluster_id: 1, uid: 'api-uid', kind: 'Deployment', name: 'api', namespace: 'shop', desired_replicas: 2, ready_replicas: 2 }], total: 1 });
  vi.mocked(api.setKubernetesCapture).mockImplementation(async (_id, spec) => ({ spec, defaults }));
});
it('shows inherited environments without treating a failed request as an unset environment', async () => {
  vi.mocked(api.listKubernetesClusters).mockResolvedValue({ items: [cluster, { ...cluster, id: 2, name: 'unconfigured' }, { ...cluster, id: 3, name: 'unavailable' }], total: 3 });
  vi.mocked(api.getKubernetesCapture).mockImplementation(async id => {
    if (id === 3) throw new Error('environment unavailable');
    return { spec: { kubernetes: { rules: [] } }, defaults: { ...defaults, environment: id === 1 ? 'production' : '' } };
  });
  render(<MemoryRouter><KubernetesAPM canEdit /></MemoryRouter>);
  expect(await screen.findByRole('cell', { name: 'production' })).toBeInTheDocument();
  expect(screen.getByRole('cell', { name: 'Not set' })).toBeInTheDocument();
  expect(screen.getByRole('cell', { name: 'Failed to load' })).toHaveAttribute('title', 'environment unavailable');
  expect(screen.getAllByRole('link', { name: 'Configure capture' })).toHaveLength(3);
});
it('opens read-only, cancels without writes, then saves a workload with fixed defaults', async () => {
  const user = userEvent.setup();
  render(<KubernetesCapture cluster={cluster} canEdit />);
  expect(await screen.findByText('production')).toBeInTheDocument();
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(screen.queryByRole('tab')).not.toBeInTheDocument();
  expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Node status' })).not.toBeInTheDocument();
  expect(api.listKubernetesWorkloads).not.toHaveBeenCalled();
  await user.click(screen.getByRole('button', { name: 'Add capture target' }));
  await user.click(within(await screen.findByRole('radiogroup', { name: 'Discovered workloads' })).getByText('api'));
  expect(screen.getByRole('radio', { name: 'Select api' })).toBeChecked();
  await user.click(screen.getByRole('button', { name: 'Cancel' }));
  expect(api.setKubernetesCapture).not.toHaveBeenCalled();
  await user.click(screen.getByRole('button', { name: 'Add capture target' }));
  await user.click(await screen.findByRole('radio', { name: 'Select api' }));
  expect(screen.queryByLabelText('Container name (optional)')).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Save target' }));
  expect(api.setKubernetesCapture).toHaveBeenCalledWith(1, { sample_ratio: 1, tls_insecure_skip_verify: true, kubernetes: { rules: [rule] } });
  expect(await screen.findByRole('button', { name: 'Edit api' })).toBeInTheDocument();
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
});
it('adds an entire namespace and removes it through edit without changing other targets', async () => {
  const user = userEvent.setup();
  const other = { namespace: 'other' };
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { sample_ratio: 0.1, tls_insecure_skip_verify: false, kubernetes: { rules: [other] } }, defaults });
  render(<KubernetesCapture cluster={cluster} canEdit />);
  await user.click(await screen.findByRole('button', { name: 'Add capture target' }));
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Capture scope' })); });
  await act(async () => { await user.click(await screen.findByRole('option', { name: 'Entire namespace' })); });
  await user.click(screen.getByRole('button', { name: 'Save target' }));
  expect(api.setKubernetesCapture).toHaveBeenLastCalledWith(1, { sample_ratio: 1, tls_insecure_skip_verify: true, kubernetes: { rules: [other, { namespace: 'shop' }] } });
  await user.click(await screen.findByRole('button', { name: 'Edit shop' }));
  await user.click(screen.getByRole('button', { name: 'Remove target' }));
  await waitFor(() => expect(api.setKubernetesCapture).toHaveBeenLastCalledWith(1, { sample_ratio: 1, tls_insecure_skip_verify: true, kubernetes: { rules: [other] } }));
});
it('retains unavailable saved workloads and input after a save failure', async () => {
  const user = userEvent.setup();
  const missing = { ...rule, workload_kind: 'StatefulSet', workload_name: 'temporarily-gone', container: 'old' };
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { kubernetes: { rules: [missing] } }, defaults });
  vi.mocked(api.setKubernetesCapture).mockRejectedValue(new Error('save failed'));
  render(<KubernetesCapture cluster={cluster} canEdit />);
  await user.click(await screen.findByRole('button', { name: 'Edit temporarily-gone' }));
  expect(screen.queryByLabelText('Container name (optional)')).not.toBeInTheDocument();
  await user.click(screen.getByRole('button', { name: 'Save target' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('save failed');
  expect(screen.getByRole('button', { name: 'Save target' })).toBeInTheDocument();
  expect(vi.mocked(api.setKubernetesCapture).mock.calls[0][1].kubernetes.rules).toEqual([{ namespace: missing.namespace, workload_kind: missing.workload_kind, workload_name: missing.workload_name }]);
});
it('does not expose mutation controls to a read-only user, even if inventory fails', async () => {
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { kubernetes: { rules: [rule] } }, defaults });
  vi.mocked(api.getKubernetesClusterHealth).mockRejectedValue(new Error('inventory unavailable'));
  render(<KubernetesCapture cluster={cluster} canEdit={false} />);
  expect(await screen.findByText('api')).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Edit api' })).not.toBeInTheDocument();
  expect(screen.queryByRole('button', { name: 'Add capture target' })).not.toBeInTheDocument();
});
