import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, expect, it, vi } from 'vitest';
import { KubernetesAPM } from './KubernetesAPM';
import * as api from '@/api/kubernetes';
vi.mock('@/api/kubernetes', () => ({
  listKubernetesClusters: vi.fn(), getKubernetesCluster: vi.fn(), getKubernetesClusterHealth: vi.fn(),
  getKubernetesCapture: vi.fn(), setKubernetesCapture: vi.fn(), listKubernetesWorkloads: vi.fn(), listKubernetesNodes: vi.fn(),
}));
vi.mock('@/api/integrations', () => ({ listEdgePlugins: vi.fn() }));
vi.mock('@/i18n/locale', async importOriginal => ({ ...await importOriginal<typeof import('@/i18n/locale')>(), useI18n: () => ({ tr: (_zh: string, en: string) => en }) }));
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.listKubernetesClusters).mockResolvedValue({ items: [{ id: 1, name: 'prod-cluster', mode: 'full-node', status: 'online', created_at: '', updated_at: '' }], total: 1 });
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { kubernetes: { rules: [] } }, defaults: { environment: 'production', cluster_name: 'prod-cluster' } });
  vi.mocked(api.getKubernetesClusterHealth).mockResolvedValue({ degraded_workloads: 0, pending_pods: 0, crash_loop_back_off_pods: 0, oom_killed_pods: 0, image_pull_back_off_pods: 0, not_ready_nodes: 0, namespaces: [{ namespace: 'shop', workloads: 1, pods: 2, events: 0, warnings: 0 }] });
  vi.mocked(api.listKubernetesWorkloads).mockResolvedValue({ items: [{ id: 1, cluster_id: 1, uid: 'api-uid', kind: 'Deployment', name: 'api', namespace: 'shop', desired_replicas: 2, ready_replicas: 2 }], total: 1 });
  vi.mocked(api.listKubernetesNodes).mockResolvedValue({ items: [], total: 0 });
  vi.mocked(api.setKubernetesCapture).mockImplementation(async (_id, spec) => ({ spec, defaults: { environment: 'production', cluster_name: 'prod-cluster' } }));
});
it('saves workload selectors without environment or namespace overrides', async () => {
  const user = userEvent.setup();
  render(<KubernetesAPM canEdit enabled />);
  const configure = await screen.findByRole('button', { name: 'Configure capture' });
  await act(async () => { await user.click(configure); });
  expect(await screen.findByText('production')).toBeInTheDocument();
  expect(screen.queryByRole('textbox', { name: /environment|namespace/i })).not.toBeInTheDocument();
  await act(async () => { await user.click(await screen.findByRole('checkbox', { name: 'Capture api' })); });
  await user.click(screen.getByRole('tab', { name: 'Capture settings' }));
  await user.clear(screen.getByRole('spinbutton', { name: 'Trace sample ratio' }));
  await user.type(screen.getByRole('spinbutton', { name: 'Trace sample ratio' }), '0.25');
  await user.click(screen.getByRole('tab', { name: 'Select workloads' }));
  expect(screen.getByRole('checkbox', { name: 'Capture api' })).toBeChecked();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(api.setKubernetesCapture).toHaveBeenCalledWith(1, { sample_ratio: 0.25, kubernetes: { rules: [{ namespace: 'shop', workload_kind: 'Deployment', workload_name: 'api' }] } });
  expect(await screen.findByText(/Saved. Nodes normally sync/)).toBeInTheDocument();
});
it('uses a namespace rule covering future workloads and can clear capture', async () => {
  const user = userEvent.setup();
  render(<KubernetesAPM canEdit enabled={false} />);
  const configure = await screen.findByRole('button', { name: 'Configure capture' });
  await act(async () => { await user.click(configure); });
  await screen.findByText('production');
  await act(async () => { await user.click(screen.getByRole('combobox', { name: 'Capture scope' })); });
  await act(async () => { await user.click(screen.getByRole('option', { name: 'Entire namespace' })); });
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(api.setKubernetesCapture).toHaveBeenLastCalledWith(1, { kubernetes: { rules: [{ namespace: 'shop' }] } });
  await user.click(screen.getByRole('tab', { name: 'Selected rules (1)' }));
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Remove shop' })); });
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  await waitFor(() => expect(api.setKubernetesCapture).toHaveBeenLastCalledWith(1, { kubernetes: { rules: [] } }));
});
it('retains unavailable saved workloads and reports save failures', async () => {
  const user = userEvent.setup();
  vi.mocked(api.getKubernetesCapture).mockResolvedValue({ spec: { kubernetes: { rules: [{ namespace: 'shop', workload_kind: 'StatefulSet', workload_name: 'temporarily-gone' }] } }, defaults: { environment: 'test', cluster_name: 'test' } });
  vi.mocked(api.setKubernetesCapture).mockRejectedValue(new Error('save failed'));
  render(<KubernetesAPM canEdit enabled />);
  const configure = await screen.findByRole('button', { name: 'Configure capture' });
  await act(async () => { await user.click(configure); });
  await user.click(await screen.findByRole('tab', { name: 'Selected rules (1)' }));
  expect(await screen.findByText('temporarily-gone')).toBeInTheDocument();
  await act(async () => { await user.click(screen.getByRole('button', { name: 'Save capture settings' })); });
  expect(await screen.findByRole('alert')).toHaveTextContent('save failed');
  expect(screen.getByText('temporarily-gone')).toBeInTheDocument();
});
