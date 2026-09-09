import userEvent from '@testing-library/user-event';
import { selectOption } from '@/test/select-option';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import KubernetesPage, { KubernetesClusterDetailPage } from './Kubernetes';
import { ClusterDetailPage } from './Clusters';
import { invalidateGrafanaRootCache } from '@/lib/drilldown';
import { server } from '@/test/msw-server';

vi.mock('@/store/me', () => ({
  usePermissions: () => ({ isAdmin: true, canMutate: true, role: 'admin' }),
}));

const cluster = {
  id: 1,
  name: 'kind-local',
  uid: 'kind-uid',
  mode: 'full-node',
  status: 'online',
  controller_edge_id: 3,
  controller_node_name: 'ongrid-k8s-control-plane',
  controller_namespace: 'ongrid-system',
  controller_pod_name: 'ongrid-edge-controller-abc',
  version: 'v1.30.0',
  last_seen_at: '2026-06-29T10:00:00Z',
  inventory_synced_at: new Date(Date.now() - 30_000).toISOString(),
  inventory_watch_lag_seconds: 2,
  inventory_sync_duration_ms: 51,
  node_edge_coverage: {
    total: 3,
    edge_linked: 2,
    device_linked: 2,
    missing: 1,
    percent: 67,
  },
  created_at: '2026-06-29T09:00:00Z',
  updated_at: '2026-06-29T10:00:00Z',
  upgrade_command: "helm upgrade ongrid-edge 'oci://helm.cnb.cool/ongridio/ongrid-edge' --version '0.10.0' --namespace 'ongrid-system' --reset-then-reuse-values --set-string manager.publicURL='https://manager.example' --set-string manager.tunnelAddr='manager.example:40012' --set-string manager.tlsInsecure=true --wait --wait-for-jobs --atomic --timeout '15m'",
};

function ChatStateProbe() {
  const location = useLocation();
  const state = location.state as { initialPrompt?: string } | null;
  return <div data-testid="initial-prompt">{state?.initialPrompt || ''}</div>;
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="current-location">{`${location.pathname}${location.search}`}</div>;
}

function renderKubernetesList() {
  return render(
    <MemoryRouter>
      <KubernetesPage />
    </MemoryRouter>,
  );
}

function renderKubernetesDetail(initialEntry = '/kubernetes/1', includeChatRoute = false) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        {includeChatRoute && <Route path="/chat/:sessionId" element={<ChatStateProbe />} />}
      </Routes>
    </MemoryRouter>,
  );
}

describe('KubernetesPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    invalidateGrafanaRootCache();
    Element.prototype.scrollIntoView = vi.fn();
    server.use(
      http.get('/api/v1/k8s/clusters', () =>
        HttpResponse.json({ items: [cluster], total: 1, limit: 100, offset: 0 }),
      ),
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 3,
              name: 'ongrid-edge-controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-06-29T10:00:00Z',
              device_id: null,
              agent_version: 'v0.9.0',
            },
            {
              id: 5,
              name: 'k8s:kind-local:ongrid-k8s-control-plane',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-06-29T10:00:00Z',
              device_id: 17,
              agent_version: 'v0.9.0',
            },
          ],
          total: 2,
        }),
      ),
      http.get('/api/v1/topology/nodes', () => HttpResponse.json({
        items: [
          { id: 1, type: 'cluster', name: 'kind-local', props: { source: 'kubernetes', k8s_cluster_id: 1 }, created_at: '', updated_at: '' },
          { id: 95, type: 'cluster', name: 'vm-kubeadm-fresh-pull', props: { source: 'kubernetes', k8s_cluster_id: 48 }, created_at: '', updated_at: '' },
        ],
        total: 2,
      })),
      http.get('/api/v1/k8s/clusters/:id', () => HttpResponse.json(cluster)),
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 0,
        pending_pods: 0,
        crash_loop_back_off_pods: 1,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/nodes', () =>
        HttpResponse.json({
          items: [{
            id: 11,
            cluster_id: 1,
            node_name: 'ongrid-k8s-control-plane',
            node_uid: 'node-uid',
            edge_id: 5,
            device_id: 17,
            capacity: { cpu: '8', memory: '9203336Ki' },
            kubelet_version: 'v1.30.0',
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
        }),
      ),
      http.get('/api/v1/k8s/clusters/:id/workloads', () =>
        HttpResponse.json({
          items: [{
            id: 21,
            cluster_id: 1,
            namespace: 'ongrid-system',
            kind: 'Deployment',
            name: 'ongrid-edge-controller',
            desired_replicas: 1,
            ready_replicas: 1,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        }),
      ),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({
            items: [{
              id: 32,
              cluster_id: 1,
              namespace: 'default',
              name: 'api-crash-abc',
              uid: 'pod-crash',
              node_name: 'ongrid-k8s-control-plane',
              phase: 'Running',
              owner_kind: 'Deployment',
              owner_name: 'api',
              restart_count: 7,
              reason: 'CrashLoopBackOff',
              last_seen_at: '2026-06-29T10:00:00Z',
            }],
            total: 1,
            limit: 20,
            offset: 0,
          });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
		if (url.searchParams.get('issue_only') === 'true') {
          return HttpResponse.json({
            items: [{
              id: 42,
              cluster_id: 1,
              namespace: 'default',
              name: 'backoff',
              type: 'Warning',
              reason: 'BackOff',
              message: 'Back-off restarting failed container api',
              involved_kind: 'Pod',
              involved_namespace: 'default',
              involved_name: 'api-crash-abc',
              involved_uid: 'pod-crash',
              count: 5,
              last_timestamp: '2026-06-29T10:01:00Z',
              last_seen_at: '2026-06-29T10:01:00Z',
            }],
            total: 1,
            limit: 100,
            offset: 0,
          });
        }
        return HttpResponse.json({
          items: [{
            id: 41,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'scheduled',
            type: 'Normal',
            reason: 'Scheduled',
            message: 'Successfully assigned pod',
            involved_kind: 'Pod',
            involved_name: 'ongrid-edge-controller-abc',
            count: 1,
            last_timestamp: '2026-06-29T10:00:00Z',
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/actions', () => {
        return HttpResponse.json({
          items: [
            {
              id: 'proposal-1',
              session_id: 'session-k8s-1',
              message_id: 'message-k8s-1',
              tool_call_id: 'call-k8s-1',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 1,
                action: 'rollout_restart',
                kind: 'Deployment',
                namespace: 'default',
                name: 'api',
                reason: '修复异常 Pod',
              }),
              tool_class: 'write',
              approval_mode: 'review_gate',
              reviewer_agent: 'reviewer',
              reviewer_task_id: 'agent-1',
              decision: 'approve',
              status: 'executed',
              decision_reason: '目标资源清晰，风险可控',
              operator_user_id: 7,
              created_at: '2026-06-29T10:02:00Z',
              decided_at: '2026-06-29T10:03:00Z',
              executed_at: '2026-06-29T10:03:10Z',
            },
            {
              id: 'approval-1',
              session_id: 'session-k8s-2',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 1,
                action: 'scale',
                kind: 'Deployment',
                namespace: 'default',
                name: 'worker',
                replicas: 2,
              }),
              tool_class: 'write',
              approval_mode: 'human',
              decision: 'approve',
              status: 'executed',
              operator_user_id: 7,
              approver_user_id: 1,
              created_at: '2026-06-29T10:04:00Z',
              decided_at: '2026-06-29T10:04:10Z',
              executed_at: '2026-06-29T10:04:12Z',
            },
          ],
          total: 2,
          limit: 100,
          offset: 0,
        });
      }),
    );
  });

  it('渲染集群列表和接入状态', async () => {
    renderKubernetesList();

    expect(await screen.findByText('kind-local')).toBeInTheDocument();
    expect(screen.getByText('full-node')).toBeInTheDocument();
    expect(screen.getByText('online')).toBeInTheDocument();
    expect(screen.getByText('Controller 运行中')).toBeInTheDocument();
    expect(screen.getByText('ongrid-k8s-control-plane')).toBeInTheDocument();
    expect(screen.getByText('K8s 版本')).toBeInTheDocument();
    expect(screen.getByText('v1.30.0')).toBeInTheDocument();
    expect(screen.getByText('2 / 3')).toBeInTheDocument();
    expect(screen.getByText('1 个待接入')).toBeInTheDocument();
  });

  it('集群列表支持删除集群', async () => {
    let items = [cluster];
    let deletedID = '';
    let deleteForce = '';
    server.use(
      http.get('/api/v1/k8s/clusters', () =>
        HttpResponse.json({ items, total: items.length, limit: 100, offset: 0 }),
      ),
      http.delete('/api/v1/k8s/clusters/:id', ({ params, request }) => {
        deletedID = String(params.id);
        deleteForce = new URL(request.url).searchParams.get('force') || '';
        items = items.filter((item) => item.id !== Number(params.id));
        return new HttpResponse(null, { status: 204 });
      }),
    );

    renderKubernetesList();

    expect(await screen.findByText('kind-local')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '删除集群 kind-local' }));
    expect(await screen.findByText('删除 Kubernetes 集群 kind-local')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '确认已卸载，删除记录' }));

    await waitFor(() => {
      expect(deletedID).toBe('1');
      expect(deleteForce).toBe('');
    });
    expect(screen.queryByText('kind-local')).not.toBeInTheDocument();
  });

  it('接入命令在 localhost 页面不自动替换远端 manager 占位符', async () => {
    let payload: { name?: string; uid?: string; mode?: string } | null = null;
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });
    server.use(
      http.post('/api/v1/k8s/clusters', async ({ request }) => {
        payload = await request.json() as { name?: string; uid?: string; mode?: string };
        return HttpResponse.json({
          cluster: {
            ...cluster,
            id: 4,
            name: payload.name || 'kind-created',
            uid: payload.uid || 'created-uid',
            mode: payload.mode || 'full-node',
            status: 'offline',
            controller_edge_id: null,
            controller_node_name: '',
            controller_namespace: '',
            controller_pod_name: '',
          },
          bootstrap_token: 'g-token',
          node_bootstrap_token: 'n-token',
          install_command:
            "helm upgrade --install ongrid-edge 'oci://helm.cnb.cool/ongridio/ongrid-edge' --version '0.10.0' --namespace ongrid-system --create-namespace --set-string manager.publicURL='https://<manager>' --set-string manager.tunnelAddr='<manager>:40012' --set-string manager.tlsInsecure=true --set-string enrollment.clusterID=4 --set-string enrollment.controllerBootstrapToken='g-token' --set-string enrollment.nodeBootstrapToken='n-token' --set-string mode='full-node'",
        });
      }),
    );

    renderKubernetesList();

    fireEvent.click(await screen.findByRole('button', { name: '接入集群' }));
    fireEvent.change(screen.getByLabelText('集群名称'), { target: { value: 'kind-created' } });
    fireEvent.click(screen.getByRole('button', { name: '创建' }));

    expect(await screen.findByText('Helm 安装命令')).toBeInTheDocument();
    const command = screen.getByText(/helm upgrade --install ongrid-edge/);
    expect(payload).toEqual({ name: 'kind-created', mode: 'full-node' });
    expect(command).toHaveTextContent("manager.publicURL='https://<manager>'");
    expect(command).toHaveTextContent("manager.tunnelAddr='<manager>:40012'");
    expect(command).toHaveTextContent("enrollment.controllerBootstrapToken='g-token'");
    expect(command).toHaveTextContent("enrollment.nodeBootstrapToken='n-token'");
    expect(screen.queryByText('Controller bootstrap token')).not.toBeInTheDocument();
    expect(screen.queryByText('Node bootstrap token')).not.toBeInTheDocument();
    expect(screen.getByText('安装命令（执行一次）')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: '复制' })).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: '复制' }));
    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith(expect.stringContaining('helm upgrade --install ongrid-edge'));
    });
  });

  it('渲染集群详情里的 Pod 快照', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    expect(screen.getAllByText('ongrid-system').length).toBeGreaterThan(0);
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getAllByText('1n / 1w / 1p / 1e').length).toBeGreaterThan(0);
    expect(screen.getByText('集群健康结论')).toBeInTheDocument();
    expect(screen.getByText('Controller')).toBeInTheDocument();
    expect(screen.getByText('Agent 版本')).toBeInTheDocument();
    expect(screen.getByText('v0.9.0')).toBeInTheDocument();
    expect(screen.getByText('异常线索')).toBeInTheDocument();
    expect(screen.getByText('关键异常')).toBeInTheDocument();
    expect(screen.getByText('1 个待确认问题')).toBeInTheDocument();
    expect(screen.getByText('Warning Event 1')).toBeInTheDocument();
    expect(screen.queryByText('ImagePullBackOff 0')).not.toBeInTheDocument();
    expect(screen.queryByText('查看异常 Pod')).not.toBeInTheDocument();
    expect(screen.queryByText('快速定位')).not.toBeInTheDocument();
    expect(screen.getByText('查看拓扑')).toBeInTheDocument();
    expect(screen.getByText('Pod 资源视图')).toBeInTheDocument();
    expect(screen.getByText('异常 Pod 1')).toBeInTheDocument();
    expect(screen.getAllByText('CrashLoopBackOff 1').length).toBeGreaterThan(0);
    expect(screen.getByText('可观测入口')).toBeInTheDocument();
    expect(screen.getAllByText('查询已就绪').length).toBeGreaterThanOrEqual(3);
    expect(screen.getAllByText('Loki').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Tempo').length).toBeGreaterThan(0);
    expect(screen.getAllByText('查询详情').length).toBeGreaterThanOrEqual(3);
  });

  it('运行中的 Job 不计入异常而终止失败的 Job 保留异常', async () => {
    const activeJob = {
      id: 51,
      cluster_id: 1,
      namespace: 'jobs',
      kind: 'Job',
      name: 'schema-job-active',
      desired_replicas: 1,
      ready_replicas: 0,
      active_replicas: 1,
      failed_replicas: 0,
      conditions: [],
      last_seen_at: new Date().toISOString(),
    };
    const failedJob = {
      id: 52,
      cluster_id: 1,
      namespace: 'jobs',
      kind: 'Job',
      name: 'schema-job-failed',
      desired_replicas: 1,
      ready_replicas: 0,
      active_replicas: 0,
      failed_replicas: 1,
      conditions: [{ type: 'Failed', status: 'True', reason: 'BackoffLimitExceeded' }],
      last_seen_at: new Date().toISOString(),
    };
    const retryingJob = {
      ...failedJob,
      id: 54,
      name: 'schema-job-retrying',
      failed_replicas: 1,
      conditions: [],
    };
    const failedJobPod = {
      id: 53,
      cluster_id: 1,
      namespace: 'jobs',
      name: 'schema-job-failed-pod',
      uid: 'schema-job-failed-pod',
      node_name: 'worker-a',
      phase: 'Failed',
      owner_kind: 'Job',
      owner_name: 'schema-job-failed',
      restart_count: 0,
      reason: 'Unknown',
      last_seen_at: new Date().toISOString(),
    };
    let issueOnlyRequests = 0;
    let includeFailedJob = true;
    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 1,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const issueOnly = new URL(request.url).searchParams.get('issue_only') === 'true';
        if (issueOnly) issueOnlyRequests += 1;
        const items = issueOnly
          ? includeFailedJob ? [failedJob] : []
          : includeFailedJob ? [activeJob, retryingJob, failedJob] : [activeJob, retryingJob];
        return HttpResponse.json({ items, total: items.length, limit: 100, offset: 0 });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const reason = new URL(request.url).searchParams.get('reason');
        if (reason === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        return HttpResponse.json({ items: [failedJobPod], total: 1, limit: 20, offset: 0 });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=workloads');

    const activeCell = await screen.findByText('schema-job-active');
    const activeRow = activeCell.closest('tr');
    expect(activeRow).not.toBeNull();
    expect(within(activeRow as HTMLElement).getByText('运行中')).toBeInTheDocument();
    expect(within(activeRow as HTMLElement).getByText('0/1 完成 · 1 运行')).toBeInTheDocument();

    const retryingCell = screen.getByText('schema-job-retrying');
    const retryingRow = retryingCell.closest('tr');
    expect(retryingRow).not.toBeNull();
    expect(within(retryingRow as HTMLElement).getByText('等待中')).toBeInTheDocument();
    expect(within(retryingRow as HTMLElement).getByText('0/1 完成 · 1 失败')).toBeInTheDocument();

    const failedCell = screen.getByText('schema-job-failed');
    const failedRow = failedCell.closest('tr');
    expect(failedRow).not.toBeNull();
    expect(within(failedRow as HTMLElement).getByText('失败')).toBeInTheDocument();
    expect(screen.queryByText('Job/schema-job-active')).not.toBeInTheDocument();
    expect(screen.queryByText('Job/schema-job-retrying')).not.toBeInTheDocument();
    expect(screen.getByText('Job/schema-job-failed')).toBeInTheDocument();
    expect(screen.getByText('1 个待确认问题')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '只看异常' }));
    await waitFor(() => {
      expect(issueOnlyRequests).toBeGreaterThan(0);
      expect(screen.queryByText('schema-job-active')).not.toBeInTheDocument();
    });
    expect(screen.getByText('schema-job-failed')).toBeInTheDocument();

    const issueOnlyRequestsBeforeRefresh = issueOnlyRequests;
    includeFailedJob = false;
    fireEvent.click(screen.getByRole('button', { name: '刷新' }));
    await waitFor(() => {
      expect(issueOnlyRequests).toBeGreaterThan(issueOnlyRequestsBeforeRefresh);
      expect(screen.queryByText('schema-job-failed')).not.toBeInTheDocument();
    });
  });

  it('集群级异常和 Namespace 汇总不受首屏 100 条限制', async () => {
    const lateWorkload = {
      id: 1501,
      cluster_id: 1,
      namespace: 'late-page',
      kind: 'Deployment',
      name: 'late-api',
      uid: 'late-api-uid',
      desired_replicas: 3,
      ready_replicas: 1,
      conditions: [],
      last_seen_at: new Date().toISOString(),
    };
    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 1,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
        namespaces: [
          { namespace: 'first-page', workloads: 100, pods: 100, events: 100, warnings: 0 },
          { namespace: 'late-page', workloads: 1, pods: 1400, events: 12, warnings: 0 },
        ],
      })),
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const issueOnly = new URL(request.url).searchParams.get('issue_only') === 'true';
        if (issueOnly) return HttpResponse.json({ items: [lateWorkload], total: 1, limit: 100, offset: 0 });
        return HttpResponse.json({
          items: [{ ...lateWorkload, id: 1, namespace: 'first-page', name: 'healthy-api', ready_replicas: 3 }],
          total: 1501,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff' || url.searchParams.get('issue_only') === 'true') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
        return HttpResponse.json({ items: [], total: 1500, limit: 100, offset: 0 });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=namespaces');

    expect(await screen.findByText('Deployment/late-api')).toBeInTheDocument();
    const namespaceCell = (await screen.findAllByText('late-page')).find((item) => item.closest('td'));
    const row = namespaceCell?.closest('tr');
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText('1400')).toBeInTheDocument();
    expect(within(row as HTMLElement).getByText('12')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('combobox', { name: '命名空间过滤' }));
    expect((await screen.findAllByRole('option', { name: 'late-page' })).length).toBeGreaterThan(0);
  });

  it('关键异常先按严重程度排序再截断展示', async () => {
    const degradedDeployments = Array.from({ length: 8 }, (_, index) => ({
      id: 1600 + index,
      cluster_id: 1,
      namespace: 'apps',
      kind: 'Deployment',
      name: `degraded-${index + 1}`,
      uid: `degraded-${index + 1}-uid`,
      desired_replicas: 2,
      ready_replicas: 1,
      conditions: [],
      last_seen_at: new Date().toISOString(),
    }));
    const failedJob = {
      id: 1700,
      cluster_id: 1,
      namespace: 'apps',
      kind: 'Job',
      name: 'critical-failed',
      uid: 'critical-failed-uid',
      desired_replicas: 1,
      ready_replicas: 0,
      active_replicas: 0,
      failed_replicas: 1,
      conditions: [{ type: 'Failed', status: 'True', reason: 'BackoffLimitExceeded' }],
      last_seen_at: new Date().toISOString(),
    };
    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 9,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const issueOnly = new URL(request.url).searchParams.get('issue_only') === 'true';
        const items = issueOnly ? [...degradedDeployments, failedJob] : [];
        return HttpResponse.json({ items, total: items.length, limit: 100, offset: 0 });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=workloads');

    expect(await screen.findByText('Job/critical-failed')).toBeInTheDocument();
    expect(screen.getByText('9 个待确认问题')).toBeInTheDocument();
    expect(screen.queryByText('Deployment/degraded-8')).not.toBeInTheDocument();
  });

  it('异常 Pod 通过 ReplicaSet 合并到 Deployment 根因', async () => {
    const deployment = {
      id: 54,
      cluster_id: 1,
      namespace: 'apps',
      kind: 'Deployment',
      name: 'api',
      uid: 'deployment-api',
      desired_replicas: 1,
      ready_replicas: 0,
      conditions: [],
      replica_sets: [{
        id: 55,
        cluster_id: 1,
        namespace: 'apps',
        kind: 'ReplicaSet',
        name: 'api-7d8f9',
        owner_kind: 'Deployment',
        owner_name: 'api',
        desired_replicas: 1,
        ready_replicas: 0,
        conditions: [],
        last_seen_at: new Date().toISOString(),
      }],
      last_seen_at: new Date().toISOString(),
    };
    const pod = {
      id: 56,
      cluster_id: 1,
      namespace: 'apps',
      name: 'api-7d8f9-broken',
      uid: 'api-pod-broken',
      node_name: 'worker-a',
      phase: 'Running',
      owner_kind: 'ReplicaSet',
      owner_name: 'api-7d8f9',
      restart_count: 6,
      reason: 'CrashLoopBackOff',
      last_seen_at: new Date().toISOString(),
    };

    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 1,
        pending_pods: 0,
        crash_loop_back_off_pods: 1,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/workloads', () => HttpResponse.json({
        items: [deployment], total: 1, limit: 20, offset: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const reason = new URL(request.url).searchParams.get('reason');
        if (reason === 'CrashLoopBackOff' || reason == null) {
          return HttpResponse.json({ items: [pod], total: 1, limit: 20, offset: 0 });
        }
        return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=workloads');

    expect(await screen.findByText('Deployment/api')).toBeInTheDocument();
    expect(screen.getByText('1 个待确认问题')).toBeInTheDocument();
    expect(screen.getAllByText('CrashLoopBackOff').length).toBeGreaterThan(0);
  });

  it('在 Deployment 行内展开关联的 ReplicaSet 发布版本', async () => {
    const groupedWorkloads = [
      {
        id: 61,
        cluster_id: 1,
        namespace: 'apps',
        kind: 'Deployment',
        name: 'api',
        desired_replicas: 1,
        ready_replicas: 1,
        revision: 3,
        replica_sets: [
          {
            id: 62,
            cluster_id: 1,
            namespace: 'apps',
            kind: 'ReplicaSet',
            name: 'api-current-7d8f9',
            desired_replicas: 1,
            ready_replicas: 1,
            owner_kind: 'Deployment',
            owner_name: 'api',
            revision: 3,
            creation_timestamp: '2026-07-28T08:09:10Z',
            last_seen_at: new Date().toISOString(),
          },
          {
            id: 63,
            cluster_id: 1,
            namespace: 'apps',
            kind: 'ReplicaSet',
            name: 'api-history-6c7e8',
            desired_replicas: 0,
            ready_replicas: 0,
            owner_kind: 'Deployment',
            owner_name: 'api',
            revision: 2,
            creation_timestamp: '2026-07-27T08:09:10Z',
            last_seen_at: new Date().toISOString(),
          },
        ],
        last_seen_at: new Date().toISOString(),
      },
      {
        id: 64,
        cluster_id: 1,
        namespace: 'staging',
        kind: 'Deployment',
        name: 'worker',
        desired_replicas: 1,
        ready_replicas: 1,
        revision: 7,
        replica_sets: [{
          id: 65,
          cluster_id: 1,
          namespace: 'staging',
          kind: 'ReplicaSet',
          name: 'worker-current-5b6d7',
          desired_replicas: 1,
          ready_replicas: 1,
          owner_kind: 'Deployment',
          owner_name: 'worker',
          revision: 7,
          creation_timestamp: '2026-07-26T08:09:10Z',
          last_seen_at: new Date().toISOString(),
        }],
        last_seen_at: new Date().toISOString(),
      },
      {
        id: 66,
        cluster_id: 1,
        namespace: 'apps',
        kind: 'ReplicaSet',
        name: 'standalone-zero',
        desired_replicas: 0,
        ready_replicas: 0,
        last_seen_at: new Date().toISOString(),
      },
    ];
    const groupReplicaSetParams: string[] = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const url = new URL(request.url);
        const namespace = url.searchParams.get('namespace');
        groupReplicaSetParams.push(url.searchParams.get('group_replica_sets') || 'unset');
        const matchedItems = namespace
          ? groupedWorkloads.filter((item) => item.namespace === namespace)
          : groupedWorkloads;
        return HttpResponse.json({
          items: matchedItems,
          total: matchedItems.length,
          limit: 100,
          offset: Number(url.searchParams.get('offset') || 0),
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=workloads');

    expect(await screen.findByText('api')).toBeInTheDocument();
    expect(screen.getByText('standalone-zero')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /显示发布历史/ })).not.toBeInTheDocument();
    expect(screen.queryByText('api-current-7d8f9')).not.toBeInTheDocument();
    expect(screen.queryByText('api-history-6c7e8')).not.toBeInTheDocument();
    expect(groupReplicaSetParams).toContain('true');

    const expand = screen.getByRole('button', { name: '展开 api 的发布版本' });
    expect(expand).toHaveAttribute('aria-expanded', 'false');
    expect(expand).toHaveTextContent('2 个版本');
    fireEvent.click(expand);

    expect(await screen.findByText('api-history-6c7e8')).toBeInTheDocument();
    expect(screen.getByText('api-current-7d8f9')).toBeInTheDocument();
    expect(screen.getByText('Revision 3')).toBeInTheDocument();
    expect(screen.getByText('Revision 2')).toBeInTheDocument();
    expect(screen.getByText('当前版本')).toBeInTheDocument();
    expect(screen.getByText('历史版本')).toBeInTheDocument();
    expect(expand).toHaveAttribute('aria-expanded', 'true');
    expect(screen.queryByText('worker-current-5b6d7')).not.toBeInTheDocument();

    await selectOption(screen.getByRole('combobox', { name: '命名空间过滤' }), 'apps');
    await waitFor(() => {
      expect(screen.queryByText('worker')).not.toBeInTheDocument();
      expect(screen.getByText('api')).toBeInTheDocument();
    });
  });

  it('K8s 指标使用 Grafana 11 Explore 深链打开 Prometheus 查询', async () => {
    let launchPayload: { expr?: string } | null = null;
    const replace = vi.fn();
    const open = vi.spyOn(window, 'open').mockReturnValue({
      closed: false,
      location: { replace },
    } as unknown as Window);
    server.use(
      http.get('/api/v1/system-settings', () => HttpResponse.json({
        items: [{
          category: 'grafana',
          key: 'root_url',
          value: 'http://grafana:3000/grafana',
          sensitive: false,
          updated_at: '2026-06-29T10:00:00Z',
        }],
        total: 1,
      })),
      http.post('/api/v1/prometheus/launch', async ({ request }) => {
        launchPayload = await request.json() as { expr?: string };
        return HttpResponse.json({ url: '/prometheus/graph?g0.expr=up' });
      }),
    );

    renderKubernetesDetail('/kubernetes/1');

    const openButtons = await screen.findAllByRole('button', { name: '打开图表' });
    fireEvent.click(openButtons[0]);

    await waitFor(() => {
      expect(launchPayload).toEqual({ expr: 'up' });
      expect(replace).toHaveBeenCalledOnce();
    });
    expect(open).toHaveBeenCalledWith('about:blank', '_blank');

    const target = new URL(String(replace.mock.calls[0][0]));
    expect(target.pathname).toBe('/grafana/explore');
    expect(target.searchParams.get('schemaVersion')).toBe('1');
    const panes = JSON.parse(target.searchParams.get('panes') || '{}');
    expect(panes.og.datasource).toBe('ongrid-prometheus');
    expect(panes.og.queries[0].datasource).toEqual({
      type: 'prometheus',
      uid: 'ongrid-prometheus',
    });
    expect(panes.og.queries[0].queryType).toBe('range');
    expect(panes.og.queries[0].expr).toBe(
      'sum by (namespace, phase) (kube_pod_status_phase{cluster_id="1",ongrid_source=~"k8s:.*"} == 1)',
    );

    open.mockRestore();
  });

  it('K8s 日志只进入平台日志中心', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/48']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/logs" element={<LocationProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    const logsEntry = await screen.findByRole('group', { name: 'K8s 日志' });
    expect(within(logsEntry).getByRole('button', { name: '查看日志' })).toBeInTheDocument();
    expect(within(logsEntry).queryByRole('button', { name: '在 Grafana 中打开' })).not.toBeInTheDocument();

    fireEvent.click(within(logsEntry).getByRole('button', { name: '查看日志' }));

    expect(screen.getByTestId('current-location')).toHaveTextContent('/logs?cluster_id=95&range=1h');
  });

  it('Workload 资源日志进入平台日志中心并携带资源筛选', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/48?tab=workloads']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/logs" element={<LocationProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    const workloadCells = await screen.findAllByText('ongrid-edge-controller');
    const row = workloadCells.map((cell) => cell.closest('tr')).find(Boolean);
    expect(row).toBeTruthy();
    await userEvent.click(within(row as HTMLElement).getByRole('button', { name: '排障' }));
    fireEvent.click(within(await screen.findByRole('menu', { name: '资源排障' })).getByRole('menuitem', { name: '日志' }));

    expect(screen.getByTestId('current-location')).toHaveTextContent(
      '/logs?cluster_id=95&range=1h&namespace=ongrid-system&workload=ongrid-edge-controller',
    );
  });

  it('统一集群详情使用 Kubernetes 接入 ID，并保留生命周期操作与集群返回入口', async () => {
    let requested = '';
    server.use(
      http.get('/api/v1/topology/nodes/901', () => HttpResponse.json({ id: 901, type: 'cluster', name: 'kind-local', props: { source: 'kubernetes', k8s_cluster_id: 48 } })),
      http.get('/api/v1/k8s/clusters/:id', ({ params }) => { requested = String(params.id); return HttpResponse.json({ ...cluster, id: 48 }); }),
    );
    render(<MemoryRouter initialEntries={['/clusters/901']}><Routes>
      <Route path="/clusters/:clusterId" element={<ClusterDetailPage />} />
      <Route path="/clusters" element={<LocationProbe />} />
    </Routes></MemoryRouter>);
    await screen.findByRole('heading', { name: 'kind-local' });
    expect(requested).toBe('48');
    expect(screen.getByRole('link', { name: '集群' })).toHaveAttribute('href', '/clusters');
    await userEvent.click(screen.getByRole('button', { name: '集群操作' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: '卸载命令' }));
    expect(await screen.findByRole('dialog', { name: 'Helm 卸载命令' })).toBeInTheDocument();
  });

  it('集群详情提供 Helm 升级命令', async () => {
    renderKubernetesDetail('/kubernetes/1');

    fireEvent.click(await screen.findByRole('button', { name: '升级命令' }));

    expect(screen.getByText('一键 Helm 升级')).toBeInTheDocument();
    const command = screen.getByText(/helm upgrade ongrid-edge/);
    expect(command).toHaveTextContent("'oci://helm.cnb.cool/ongridio/ongrid-edge'");
    expect(command).toHaveTextContent("--version '0.10.0'");
    expect(command).toHaveTextContent("--namespace 'ongrid-system'");
    expect(command).toHaveTextContent('--reset-then-reuse-values');
    expect(command).toHaveTextContent("manager.tunnelAddr='manager.example:40012'");
    expect(command).toHaveTextContent('--wait-for-jobs');
    expect(command).toHaveTextContent('--atomic');
  });

  it('已恢复的 Warning Event 不进入健康结论和异常线索', async () => {
    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 0,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            uid: 'pod-healthy',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            reason: '',
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('issue_only') === 'true') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
        return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('当前快照未发现需要处置的异常')).toBeInTheDocument();
    expect(screen.queryByText('Warning Event 1')).not.toBeInTheDocument();
    expect(screen.queryByText('1 个待确认问题')).not.toBeInTheDocument();
    expect(screen.queryByText('Unhealthy')).not.toBeInTheDocument();
    expect(screen.queryByText('Readiness probe failed: HTTP probe failed with statuscode: 500')).not.toBeInTheDocument();
  });

  it('同一 HPA 的多条 Warning Event 合并为一个异常线索', async () => {
    server.use(
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 0,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            uid: 'pod-healthy',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            reason: '',
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('issue_only') !== 'true') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
        return HttpResponse.json({
          items: [
            {
              id: 71,
              cluster_id: 1,
              namespace: 'ongrid-system',
              name: 'hpa-failed-compute',
              type: 'Warning',
              reason: 'FailedComputeMetricsReplicas',
              message: 'invalid metrics',
              involved_kind: 'HorizontalPodAutoscaler',
              involved_namespace: 'ongrid-system',
              involved_name: 'ongrid-edge-telemetry-gateway',
              count: 4,
            },
            {
              id: 72,
              cluster_id: 1,
              namespace: 'ongrid-system',
              name: 'hpa-failed-resource',
              type: 'Warning',
              reason: 'FailedGetResourceMetric',
              message: 'did not receive metrics for targeted pods',
              involved_kind: 'HorizontalPodAutoscaler',
              involved_namespace: 'ongrid-system',
              involved_name: 'ongrid-edge-telemetry-gateway',
              count: 4,
            },
          ],
          total: 2,
          limit: 100,
          offset: 0,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1');

    expect(await screen.findByText('1 个待确认问题')).toBeInTheDocument();
    expect(screen.getByText('Warning Event 2')).toBeInTheDocument();
    expect(screen.queryByText('2 个待确认问题')).not.toBeInTheDocument();
    expect(screen.getByText('FailedComputeMetricsReplicas')).toBeInTheDocument();
    expect(screen.getByText('FailedGetResourceMetric')).toBeInTheDocument();
  });

  it('Agent 版本只统计节点 Edge，不把 Controller 纳入覆盖率', async () => {
    server.use(
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 3,
              name: 'ongrid-edge-controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-06-29T10:00:00Z',
              device_id: null,
              agent_version: '',
            },
            {
              id: 5,
              name: 'k8s:kind-local:ongrid-k8s-control-plane',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-06-29T10:00:00Z',
              device_id: 17,
              agent_version: 'v0.9.0',
            },
          ],
          total: 2,
        }),
      ),
    );

    renderKubernetesDetail('/kubernetes/1?tab=nodes');

    expect(await screen.findByText('Agent 版本')).toBeInTheDocument();
    expect(screen.getAllByText('v0.9.0').length).toBeGreaterThan(0);
    expect(screen.getByText('1 个 agent 一致')).toBeInTheDocument();
    expect(screen.queryByText('已上报 1/2')).not.toBeInTheDocument();
  });

  it('Pod 资源表使用 offset 分页访问 1500 条结果', async () => {
    const podPages: Array<{ limit: number; offset: number }> = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        const limit = Number(url.searchParams.get('limit') || 100);
        const offset = Number(url.searchParams.get('offset') || 0);
        podPages.push({ limit, offset });
        const count = Math.max(0, Math.min(limit, 1500 - offset));
        return HttpResponse.json({
          items: Array.from({ length: count }, (_, index) => {
            const n = offset + index + 1;
            return {
              id: n,
              cluster_id: 1,
              namespace: 'default',
              name: `pod-${String(n).padStart(3, '0')}`,
              node_name: 'ongrid-k8s-control-plane',
              phase: 'Running',
              owner_kind: 'Deployment',
              owner_name: 'api',
              restart_count: 0,
              last_seen_at: '2026-06-29T10:00:00Z',
            };
          }),
          total: 1500,
          limit,
          offset,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('pod-001')).toBeInTheDocument();
    expect(screen.queryByText('pod-101')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '下一页' }));

    await waitFor(() => {
      expect(podPages).toContainEqual({ limit: 100, offset: 100 });
    });
    expect(await screen.findByText('pod-101')).toBeInTheDocument();
    expect(screen.getByText('pod-200')).toBeInTheDocument();
    expect(screen.queryByText('pod-001')).not.toBeInTheDocument();

    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled();
  });

  it('Pod 资源表筛选后从第一页开始按 offset 分页', async () => {
    const podPages: Array<{ limit: number; offset: number }> = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        const query = url.searchParams.get('q') || '';
        const limit = Number(url.searchParams.get('limit') || 100);
        const offset = Number(url.searchParams.get('offset') || 0);
        if (query === 'api') {
          podPages.push({ limit, offset });
          const count = Math.max(0, Math.min(limit, 150 - offset));
          return HttpResponse.json({
            items: Array.from({ length: count }, (_, index) => {
              const n = offset + index + 1;
              return {
                id: n,
                cluster_id: 1,
                namespace: 'default',
                name: `api-pod-${String(n).padStart(3, '0')}`,
                node_name: 'ongrid-k8s-control-plane',
                phase: 'Running',
                owner_kind: 'Deployment',
                owner_name: 'api',
                restart_count: 0,
                last_seen_at: '2026-06-29T10:00:00Z',
              };
            }),
            total: 150,
            limit,
            offset,
          });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'api' } });

    expect(await screen.findByText('api-pod-001')).toBeInTheDocument();
    expect(screen.getByText('1-100 / 共 150 条匹配')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '下一页' }));

    await waitFor(() => {
      expect(podPages).toContainEqual({ limit: 100, offset: 100 });
    });
    expect(await screen.findByText('api-pod-101')).toBeInTheDocument();
    expect(await screen.findByText('api-pod-150')).toBeInTheDocument();
    expect(screen.queryByText('api-pod-001')).not.toBeInTheDocument();
    expect(screen.getByText('101-150 / 共 150 条匹配')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '下一页' })).toBeDisabled();
  });

  it('Pod 资源搜索防抖后再请求服务端', async () => {
    const podQueries: string[] = [];
    const nonPodQueries: string[] = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const url = new URL(request.url);
        const query = url.searchParams.get('q');
        if (query) nonPodQueries.push(`workloads:${query}`);
        return HttpResponse.json({
          items: [{
            id: 21,
            cluster_id: 1,
            namespace: 'ongrid-system',
            kind: 'Deployment',
            name: 'ongrid-edge-controller',
            desired_replicas: 1,
            ready_replicas: 1,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        const query = url.searchParams.get('q') || '';
        podQueries.push(query);
        if (query === 'api') {
          return HttpResponse.json({
            items: [{
              id: 131,
              cluster_id: 1,
              namespace: 'default',
              name: 'api-search-result',
              node_name: 'ongrid-k8s-control-plane',
              phase: 'Running',
              owner_kind: 'Deployment',
              owner_name: 'api',
              restart_count: 0,
              last_seen_at: '2026-06-29T10:00:00Z',
            }],
            total: 1,
            limit: 100,
            offset: 0,
          });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
        const query = url.searchParams.get('q');
        if (query) nonPodQueries.push(`events:${query}`);
        return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'api' } });
    expect(podQueries).not.toContain('api');
    expect(screen.queryByText('ongrid-edge-controller-abc')).not.toBeInTheDocument();

    await new Promise((resolve) => window.setTimeout(resolve, 150));
    expect(podQueries).not.toContain('api');

    await waitFor(() => {
      expect(podQueries).toContain('api');
    });
    expect(nonPodQueries).toEqual([]);

    expect(await screen.findByText('api-search-result')).toBeInTheDocument();
    expect(screen.getByText('1 条匹配')).toBeInTheDocument();
    expect(screen.queryByText('显示前 1 条，共 24 条')).not.toBeInTheDocument();
  });

  it('Pod 资源筛选无结果时展示当前筛选条件', async () => {
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        if (url.searchParams.get('q') === 'missing') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'missing' } });

    expect(await screen.findByText('暂无匹配 Pod')).toBeInTheDocument();
    expect(screen.getByText('当前条件：搜索 "missing"')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));

    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('');
    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
  });

  it('Pod 资源表行内提供排障入口并能发起资源分析', async () => {
    let sessionPayload: { title?: string; agent_id?: string } | null = null;
    server.use(
      http.post('/api/v1/chat/sessions', async ({ request }) => {
        sessionPayload = await request.json() as { title?: string; agent_id?: string };
        return HttpResponse.json({
          id: 'session-resource-analyze',
          user_id: 1,
          title: sessionPayload.title || 'resource analyze',
          agent_id: sessionPayload.agent_id || 'default',
          created_at: '2026-06-29T10:04:00Z',
          updated_at: '2026-06-29T10:04:00Z',
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods', true);

    const podCell = await screen.findByText('ongrid-edge-controller-abc');
    const row = podCell.closest('tr');
    expect(row).not.toBeNull();
    await userEvent.click(within(row as HTMLElement).getByRole('button', { name: '排障' }));

    const menu = await screen.findByRole('menu', { name: '资源排障' });
    expect(within(menu).getByRole('menuitem', { name: '日志' })).toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'describe' })).toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: '链路' })).toBeInTheDocument();

    fireEvent.click(within(menu).getByRole('menuitem', { name: 'AI 分析' }));

    await waitFor(() => {
      expect(sessionPayload).toEqual({ title: 'analyze ongrid-edge-controller-abc', agent_id: 'default' });
    });
    expect(await screen.findByTestId('initial-prompt')).toHaveTextContent('请分析 Kubernetes 资源状态');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('Pod/ongrid-edge-controller-abc');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('必须先 dry-run 并走审批');
  });

  it('Event 资源表只给 Warning Event 提供排障入口', async () => {
    let sessionPayload: { title?: string; agent_id?: string } | null = null;
    const warningEvent = {
      id: 42,
      cluster_id: 1,
      namespace: 'default',
      name: 'backoff',
      type: 'Warning',
      reason: 'BackOff',
      message: 'Back-off restarting failed container api',
      involved_kind: 'Pod',
      involved_namespace: 'default',
      involved_name: 'api-crash-abc',
      involved_uid: 'pod-crash',
      count: 5,
      last_timestamp: '2026-06-29T10:01:00Z',
      last_seen_at: '2026-06-29T10:01:00Z',
    };
    const normalEvent = {
      id: 41,
      cluster_id: 1,
      namespace: 'ongrid-system',
      name: 'scheduled',
      type: 'Normal',
      reason: 'Scheduled',
      message: 'Successfully assigned pod',
      involved_kind: 'Pod',
      involved_name: 'ongrid-edge-controller-abc',
      count: 1,
      last_timestamp: '2026-06-29T10:00:00Z',
      last_seen_at: '2026-06-29T10:00:00Z',
    };
    server.use(
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
		if (url.searchParams.get('issue_only') === 'true') {
          return HttpResponse.json({ items: [warningEvent], total: 1, limit: 100, offset: 0 });
        }
        return HttpResponse.json({ items: [warningEvent, normalEvent], total: 2, limit: 100, offset: 0 });
      }),
      http.post('/api/v1/chat/sessions', async ({ request }) => {
        sessionPayload = await request.json() as { title?: string; agent_id?: string };
        return HttpResponse.json({
          id: 'session-event-analyze',
          user_id: 1,
          title: sessionPayload.title || 'event analyze',
          agent_id: sessionPayload.agent_id || 'default',
          created_at: '2026-06-29T10:04:00Z',
          updated_at: '2026-06-29T10:04:00Z',
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=events', true);

    const warningObject = await screen.findByText('Pod/api-crash-abc');
    const warningRow = warningObject.closest('tr');
    expect(warningRow).not.toBeNull();
    await userEvent.click(within(warningRow as HTMLElement).getByRole('button', { name: '排障' }));

    const menu = await screen.findByRole('menu', { name: '资源排障' });
    expect(within(menu).getByRole('menuitem', { name: '日志' })).toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'describe' })).toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: '链路' })).toBeInTheDocument();

    const normalObject = await screen.findByText('Pod/ongrid-edge-controller-abc');
    const normalRow = normalObject.closest('tr');
    expect(normalRow).not.toBeNull();
    expect(within(normalRow as HTMLElement).queryByRole('button', { name: '排障' })).not.toBeInTheDocument();

    fireEvent.click(within(menu).getByRole('menuitem', { name: 'AI 分析' }));

    await waitFor(() => {
      expect(sessionPayload).toEqual({ title: 'analyze BackOff', agent_id: 'default' });
    });
    expect(await screen.findByTestId('initial-prompt')).toHaveTextContent('请分析 Kubernetes 资源状态');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('Pod/api-crash-abc');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('必须先 dry-run 并走审批');
  });

  it('Pod 服务端筛选失败时展示快照回退和重试入口', async () => {
    let filterAttempts = 0;
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        if (url.searchParams.get('q') === 'api') {
          filterAttempts += 1;
          if (filterAttempts === 1) {
            return HttpResponse.json({ message: 'snapshot index timeout' }, { status: 503 });
          }
          return HttpResponse.json({
            items: [{
              id: 131,
              cluster_id: 1,
              namespace: 'default',
              name: 'api-search-result',
              node_name: 'ongrid-k8s-control-plane',
              phase: 'Running',
              owner_kind: 'Deployment',
              owner_name: 'api',
              restart_count: 0,
              last_seen_at: '2026-06-29T10:00:00Z',
            }],
            total: 1,
            limit: 100,
            offset: 0,
          });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'api' } });

    expect(await screen.findByText(/服务端筛选失败，已回退到当前快照过滤/)).toBeInTheDocument();
    expect(screen.getByText(/snapshot index timeout/)).toBeInTheDocument();
    expect(screen.getByText('暂无匹配 Pod')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '重试' }));

    await waitFor(() => {
      expect(filterAttempts).toBe(2);
    });
    expect(await screen.findByText('api-search-result')).toBeInTheDocument();
    expect(screen.queryByText(/服务端筛选失败/)).not.toBeInTheDocument();
  });

  it('Nodes 本地搜索不触发 Workloads Pods Events 服务端筛选', async () => {
    const serverQueries: string[] = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/workloads', ({ request }) => {
        const url = new URL(request.url);
        const query = url.searchParams.get('q');
        if (query) serverQueries.push(`workloads:${query}`);
        return HttpResponse.json({
          items: [{
            id: 21,
            cluster_id: 1,
            namespace: 'ongrid-system',
            kind: 'Deployment',
            name: 'ongrid-edge-controller',
            desired_replicas: 1,
            ready_replicas: 1,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        const query = url.searchParams.get('q');
        if (query) serverQueries.push(`pods:${query}`);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', ({ request }) => {
        const url = new URL(request.url);
        const query = url.searchParams.get('q');
        if (query) serverQueries.push(`events:${query}`);
        return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=nodes');

    expect((await screen.findAllByText('ongrid-k8s-control-plane')).length).toBeGreaterThan(0);
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'ongrid' } });

    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 450));
    });
    expect(serverQueries).toEqual([]);
  });

  it('资源快照过期时标记健康结论和异常线索', async () => {
    const staleCluster = {
      ...cluster,
      inventory_synced_at: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
      inventory_watch_lag_seconds: 2,
      inventory_sync_duration_ms: 51,
    };
    server.use(
      http.get('/api/v1/k8s/clusters/:id', () => HttpResponse.json(staleCluster)),
      http.get('/api/v1/k8s/clusters/:id/health', () => HttpResponse.json({
        degraded_workloads: 0,
        pending_pods: 0,
        crash_loop_back_off_pods: 0,
        oom_killed_pods: 0,
        image_pull_back_off_pods: 0,
        not_ready_nodes: 0,
      })),
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        return HttpResponse.json({
          items: [{
            id: 31,
            cluster_id: 1,
            namespace: 'ongrid-system',
            name: 'ongrid-edge-controller-abc',
            node_name: 'ongrid-k8s-control-plane',
            phase: 'Running',
            owner_kind: 'Deployment',
            owner_name: 'ongrid-edge-controller',
            restart_count: 0,
            last_seen_at: staleCluster.inventory_synced_at,
          }],
          total: 1,
          limit: 100,
          offset: 0,
        });
      }),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
    );

    renderKubernetesDetail('/kubernetes/1?tab=nodes');

    expect(await screen.findByText('集群数据可信度需要确认')).toBeInTheDocument();
    expect(screen.queryByText('下一步')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '查看同步信号' })).not.toBeInTheDocument();
    expect(screen.getByText('快照同步 1')).toBeInTheDocument();
    expect(screen.getByText('快照同步异常')).toBeInTheDocument();
    expect(screen.getAllByText(/快照 .* 未更新/).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'AI 分析' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '查看 Events' })).toBeInTheDocument();
    expect(screen.queryByText('更多')).not.toBeInTheDocument();
  });

  it('初始化未接入集群展示待接入状态并隐藏资源排障入口', async () => {
    const pendingCluster = {
      ...cluster,
      id: 99,
      name: '椒子',
      status: 'offline',
      controller_edge_id: null,
      controller_node_name: '',
      controller_namespace: '',
      controller_pod_name: '',
      last_seen_at: null,
      inventory_synced_at: null,
      inventory_resource_version: '',
      inventory_watch_lag_seconds: undefined,
      inventory_sync_duration_ms: undefined,
      capabilities: [
        { key: 'inventory', status: 'unavailable' },
        { key: 'events', status: 'unavailable' },
      ],
    };
    server.use(
      http.get('/api/v1/k8s/clusters/:id', () => HttpResponse.json(pendingCluster)),
      http.get('/api/v1/k8s/clusters/:id/nodes', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/k8s/clusters/:id/workloads', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
      http.get('/api/v1/k8s/clusters/:id/pods', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
      http.get('/api/v1/k8s/clusters/:id/events', () => HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 })),
      http.get('/api/v1/k8s/clusters/:id/actions', () => HttpResponse.json({ items: [], total: 0 })),
    );

    renderKubernetesDetail('/kubernetes/99');

    expect((await screen.findAllByText('等待集群完成接入')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('待接入').length).toBeGreaterThan(0);
    expect(screen.getByText('尚未收到 Controller 首次上报')).toBeInTheDocument();
    expect(screen.queryByText('Critical')).not.toBeInTheDocument();
    expect(screen.queryByText('异常线索')).not.toBeInTheDocument();
    expect(screen.queryByText('写动作')).not.toBeInTheDocument();
    expect(screen.queryByText('Node 资源视图')).not.toBeInTheDocument();
    expect(screen.queryByText('能力状态')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Nodes\s+0$/ })).not.toBeInTheDocument();
  });

  it('点击顶部资源分类后滚动到资源视图', async () => {
    renderKubernetesDetail('/kubernetes/1');

    await screen.findByText('集群健康结论');
    const podTabs = screen.getAllByRole('tab', { name: /Pods/ });
    fireEvent.click(podTabs[0]);

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
  });

  it('在 Nodes 视图把 Node Edge 作为设备入口展示', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=nodes');

    expect((await screen.findAllByText('ongrid-k8s-control-plane')).length).toBeGreaterThan(0);
    expect(screen.getByText('Node Edge #5')).toBeInTheDocument();
    expect(screen.getByText('接入实例')).toBeInTheDocument();
    expect(screen.getByText('Agent')).toBeInTheDocument();
    expect(screen.getByText('8.8 GiB')).toBeInTheDocument();
    expect(screen.getAllByText('v0.9.0').length).toBeGreaterThan(0);
  });

  it('Node 资源表行内提供排障入口并能发起资源分析', async () => {
    let sessionPayload: { title?: string; agent_id?: string } | null = null;
    server.use(
      http.post('/api/v1/chat/sessions', async ({ request }) => {
        sessionPayload = await request.json() as { title?: string; agent_id?: string };
        return HttpResponse.json({
          id: 'session-node-analyze',
          user_id: 1,
          title: sessionPayload.title || 'node analyze',
          agent_id: sessionPayload.agent_id || 'default',
          created_at: '2026-06-29T10:04:00Z',
          updated_at: '2026-06-29T10:04:00Z',
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=nodes', true);

    const nodeCells = await screen.findAllByText('ongrid-k8s-control-plane');
    const row = nodeCells.map((cell) => cell.closest('tr')).find(Boolean);
    expect(row).toBeTruthy();
    await userEvent.click(within(row as HTMLElement).getByRole('button', { name: '排障' }));

    const menu = await screen.findByRole('menu', { name: '资源排障' });
    expect(row).not.toContainElement(menu);
    expect(menu).toHaveClass('og-menu');
    expect(within(menu).getByRole('menuitem', { name: '日志' })).toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'describe' })).toBeInTheDocument();
    expect(within(menu).queryByRole('menuitem', { name: '链路' })).not.toBeInTheDocument();

    fireEvent.click(within(menu).getByRole('menuitem', { name: 'AI 分析' }));

    await waitFor(() => {
      expect(sessionPayload).toEqual({ title: 'analyze ongrid-k8s-control-plane', agent_id: 'default' });
    });
    expect(await screen.findByTestId('initial-prompt')).toHaveTextContent('请分析 Kubernetes 资源状态');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('Node/ongrid-k8s-control-plane');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('必须先 dry-run 并走审批');
  });

  it('Namespace 行可以直接跳转到对应命名空间的资源视图', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=namespaces');

    const namespaceCell = (await screen.findAllByText('ongrid-system'))
      .find((item) => item.closest('td'));
    const row = namespaceCell?.closest('tr');
    expect(row).not.toBeNull();

    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'Pods' }));

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
    expect(screen.getByText('Pod 资源视图')).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: '命名空间过滤' })).toHaveTextContent('ongrid-system');
    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
  });

  it('渲染 CrashLoopBackOff 诊断区和关联 Warning Event', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=pods');

    expect(await screen.findByText('api-crash-abc')).toBeInTheDocument();
    expect(screen.getAllByText('CrashLoopBackOff').length).toBeGreaterThan(0);
    expect(screen.getByText('7 次重启')).toBeInTheDocument();
    expect(screen.getByText(/Back-off restarting failed container api/)).toBeInTheDocument();
    expect(screen.getAllByText('查看日志').length).toBeGreaterThan(0);
    await userEvent.click(screen.getAllByRole('button', { name: '更多' })[0]);
    expect(await screen.findByRole('menuitem', { name: 'describe' })).toBeInTheDocument();
    expect(await screen.findByRole('menuitem', { name: '关联链路' })).toBeInTheDocument();
    await userEvent.keyboard('{Escape}');
    expect(screen.getAllByRole('button', { name: 'AI 分析' }).length).toBeGreaterThan(0);
    expect(screen.queryByText('default · Pod/api-crash-abc')).not.toBeInTheDocument();
    expect(screen.getAllByText('更多').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole('button', { name: '查看 Pod' }));

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('api-crash-abc');
    expect(screen.getByRole('combobox', { name: '命名空间过滤' })).toHaveTextContent('default');
    expect(screen.getByRole('button', { name: '只看异常' })).toHaveClass('border-amber-500/50');

    fireEvent.click(screen.getAllByRole('tab', { name: /Nodes/ })[0]);

    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('');
    expect(screen.getByRole('button', { name: '只看异常' })).not.toHaveClass('border-amber-500/50');
  });

  it('异常线索只保留一个更多菜单并可点击外部关闭', async () => {
    server.use(
      http.get('/api/v1/k8s/clusters/:id/nodes', () => HttpResponse.json({
        items: [{
          id: 12,
          cluster_id: 1,
          node_name: 'worker-not-ready',
          node_uid: 'node-not-ready',
          edge_id: 6,
          device_id: 18,
          conditions: [{ type: 'Ready', status: 'False' }],
          kubelet_version: 'v1.30.0',
          last_seen_at: '2026-06-29T10:00:00Z',
        }],
        total: 1,
      })),
    );
    renderKubernetesDetail('/kubernetes/1?tab=pods');

    const moreButtons = await screen.findAllByText('更多');
    expect(moreButtons.length).toBeGreaterThan(1);
    const firstTrigger = moreButtons[0].closest('button')!;
    const secondTrigger = moreButtons[1].closest('button')!;
    await userEvent.click(firstTrigger);
    await waitFor(() => expect(firstTrigger).toHaveAttribute('aria-expanded', 'true'));
    await userEvent.keyboard('{Escape}');
    await userEvent.click(secondTrigger);
    expect(firstTrigger).toHaveAttribute('aria-expanded', 'false');
    await waitFor(() => expect(secondTrigger).toHaveAttribute('aria-expanded', 'true'));
    await userEvent.click(screen.getByText('异常线索'));
    await waitFor(() => expect(secondTrigger).toHaveAttribute('aria-expanded', 'false'));

  });

  it('异常线索内联展示并发起匹配的写动作建议', async () => {
    let sessionPayload: { title?: string; agent_id?: string } | null = null;
    server.use(
      http.post('/api/v1/chat/sessions', async ({ request }) => {
        sessionPayload = await request.json() as { title?: string; agent_id?: string };
        return HttpResponse.json({
          id: 'session-action',
          user_id: 1,
          title: sessionPayload.title || 'k8s action',
          agent_id: sessionPayload.agent_id || 'default',
          created_at: '2026-06-29T10:04:00Z',
          updated_at: '2026-06-29T10:04:00Z',
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=pods', true);

    expect(await screen.findByText('api-crash-abc')).toBeInTheDocument();
    expect(screen.getAllByText('建议动作').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Deployment/api namespace=default').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole('button', { name: '建议动作' }));

    await waitFor(() => {
      expect(sessionPayload).toEqual({ title: 'restart rollout kind-local', agent_id: 'default' });
    });
    expect(await screen.findByTestId('initial-prompt')).toHaveTextContent('必须先 dry-run');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('ReviewGate');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('Deployment/api namespace=default');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('回滚方案');
  });

  it('渲染当前集群的 K8s 写动作审计记录', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=actions');

    expect(await screen.findByText('写动作')).toBeInTheDocument();
    expect(screen.getByText('安全处置建议')).toBeInTheDocument();
    expect(screen.getByText('建议 restart rollout')).toBeInTheDocument();
    expect(screen.queryByText('备选 delete pod')).not.toBeInTheDocument();
    expect(screen.getByText('建议 1')).toBeInTheDocument();
    expect(screen.getAllByText('Deployment/api namespace=default').length).toBeGreaterThan(0);
    expect(screen.getByText('api-crash-abc · CrashLoopBackOff · 7 次重启 · BackOff')).toBeInTheDocument();
    expect(screen.getAllByText('scale deployment').length).toBeGreaterThan(0);
    expect(screen.getAllByText('restart rollout').length).toBeGreaterThan(0);
    expect(screen.getAllByText('delete pod').length).toBeGreaterThan(0);
    expect(screen.queryByText('apply patch')).not.toBeInTheDocument();
    expect(await screen.findByText('K8s 写动作审计')).toBeInTheDocument();
    expect(screen.getByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getAllByText('已执行').length).toBeGreaterThan(0);
    expect(screen.getAllByText('请求已记录').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Dry run 已验证').length).toBeGreaterThan(0);
    expect(screen.getAllByText('审批通过').length).toBeGreaterThan(0);
    expect(screen.getAllByText('执行完成').length).toBeGreaterThan(0);
    expect(screen.getByText(/回滚到上一 revision/)).toBeInTheDocument();
    expect(screen.getAllByText(/请求/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/审批/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/工具已返回/).length).toBeGreaterThan(0);
    expect(screen.getByText('会话 session-k8s-1')).toBeInTheDocument();
    expect(screen.getByText('消息 message-k8')).toBeInTheDocument();
    expect(screen.getByText('调用 call-k8s-1')).toBeInTheDocument();
    expect(screen.getByText('agent-1')).toBeInTheDocument();
    expect(screen.getByText('人工审批')).toBeInTheDocument();
    expect(screen.getByText('目标资源清晰，风险可控')).toBeInTheDocument();
    expect(screen.getByText('scale · default · Deployment/worker → 2')).toBeInTheDocument();
  });

  it('Actions 资源支持按审批状态和动作类型筛选', async () => {
    server.use(
      http.get('/api/v1/k8s/clusters/:id/actions', () => {
        return HttpResponse.json({
          items: [
            {
              id: 'proposal-executed',
              session_id: 'session-k8s-1',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 1,
                action: 'rollout_restart',
                kind: 'Deployment',
                namespace: 'default',
                name: 'api',
              }),
              tool_class: 'write',
              approval_mode: 'review_gate',
              reviewer_agent: 'reviewer',
              decision: 'approve',
              status: 'executed',
              decision_reason: '已完成 rollout restart',
              operator_user_id: 7,
              created_at: '2026-06-29T10:02:00Z',
              decided_at: '2026-06-29T10:03:00Z',
              executed_at: '2026-06-29T10:03:10Z',
            },
            {
              id: 'proposal-pending',
              session_id: 'session-k8s-2',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 1,
                action: 'delete_pod',
                kind: 'Pod',
                namespace: 'default',
                name: 'api-crash-abc',
              }),
              tool_class: 'write',
              approval_mode: 'human',
              decision: 'pending',
              status: 'pending',
              operator_user_id: 7,
              created_at: '2026-06-29T10:04:00Z',
            },
            {
              id: 'proposal-rejected',
              session_id: 'session-k8s-3',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 1,
                action: 'scale',
                kind: 'Deployment',
                namespace: 'default',
                name: 'api',
                replicas: 2,
              }),
              tool_class: 'write',
              approval_mode: 'review_gate',
              reviewer_agent: 'reviewer',
              decision: 'reject',
              status: 'rejected',
              decision_reason: '副本风险未确认',
              operator_user_id: 7,
              created_at: '2026-06-29T10:05:00Z',
              decided_at: '2026-06-29T10:05:30Z',
            },
          ],
          total: 3,
          limit: 100,
          offset: 0,
        });
      }),
    );

    renderKubernetesDetail('/kubernetes/1?tab=actions');

    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();

    await selectOption(screen.getByRole('combobox', { name: '审批状态过滤' }), '待审批');

    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.queryByText('rollout_restart · default · Deployment/api')).not.toBeInTheDocument();
    expect(screen.queryByText('scale · default · Deployment/api → 2')).not.toBeInTheDocument();
    expect(screen.getByText('1 条匹配')).toBeInTheDocument();

    await selectOption(screen.getByRole('combobox', { name: '审批状态过滤' }), '全部状态');
    await selectOption(screen.getByRole('combobox', { name: '动作类型过滤' }), 'scale deployment');

    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();
    expect(screen.queryByText('delete_pod · default · Pod/api-crash-abc')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '清除' }));

    expect(screen.getByRole('combobox', { name: '审批状态过滤' })).toHaveTextContent('全部状态');
    expect(screen.getByRole('combobox', { name: '动作类型过滤' })).toHaveTextContent('全部动作');
    expect(screen.getByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();
  });

  it('Actions 资源筛选无结果时展示当前筛选条件', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=actions');

    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'missing-action' } });

    expect(screen.getByText('暂无匹配写动作审计记录')).toBeInTheDocument();
    expect(screen.getByText('当前条件：搜索 "missing-action"')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));

    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('');
    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
  });

  it('渲染 Namespaces 资源页签', async () => {
    renderKubernetesDetail('/kubernetes/1?tab=namespaces');

    expect((await screen.findAllByText('Namespaces')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('ongrid-system').length).toBeGreaterThan(0);
    expect(screen.getByText('Warnings')).toBeInTheDocument();
  });

});
