import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import KubernetesPage, { KubernetesClusterDetailPage } from './Kubernetes';
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
  bootstrap_token_expires_at: '2026-06-30T10:00:00Z',
  created_at: '2026-06-29T09:00:00Z',
  updated_at: '2026-06-29T10:00:00Z',
};

function ChatStateProbe() {
  const location = useLocation();
  const state = location.state as { initialPrompt?: string } | null;
  return <div data-testid="initial-prompt">{state?.initialPrompt || ''}</div>;
}

describe('KubernetesPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    Element.prototype.scrollIntoView = vi.fn();
    server.use(
      http.get('/api/v1/k8s/clusters', () =>
        HttpResponse.json({ items: [cluster], total: 1, limit: 100, offset: 0 }),
      ),
      http.get('/api/v1/k8s/clusters/:id', () => HttpResponse.json(cluster)),
      http.get('/api/v1/k8s/clusters/:id/nodes', () =>
        HttpResponse.json({
          items: [{
            id: 11,
            cluster_id: 1,
            node_name: 'ongrid-k8s-control-plane',
            node_uid: 'node-uid',
            edge_id: 5,
            device_id: 17,
            capacity: { cpu: '8', memory: '16Gi' },
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
        if (url.searchParams.get('type') === 'Warning') {
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
      http.get('/api/v1/aiops/mutating-proposals', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('tool_name') !== 'execute_k8s_action') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
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
              reviewer_agent: 'reviewer',
              reviewer_task_id: 'agent-1',
              decision: 'approve',
              decision_reason: '目标资源清晰，风险可控',
              operator_user_id: 7,
              created_at: '2026-06-29T10:02:00Z',
              decided_at: '2026-06-29T10:03:00Z',
              executed_at: '2026-06-29T10:03:10Z',
            },
            {
              id: 'proposal-2',
              session_id: 'session-k8s-2',
              tool_name: 'execute_k8s_action',
              args_json: JSON.stringify({
                cluster_id: 2,
                action: 'scale',
                kind: 'Deployment',
                namespace: 'default',
                name: 'other',
                replicas: 2,
              }),
              tool_class: 'write',
              reviewer_agent: 'reviewer',
              reviewer_task_id: 'agent-2',
              decision: 'pending',
              operator_user_id: 7,
              created_at: '2026-06-29T10:02:00Z',
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
    render(
      <MemoryRouter>
        <KubernetesPage />
      </MemoryRouter>,
    );

    expect(await screen.findByText('kind-local')).toBeInTheDocument();
    expect(screen.getByText('full-node')).toBeInTheDocument();
    expect(screen.getByText('online')).toBeInTheDocument();
    expect(screen.getByText('Controller 运行中')).toBeInTheDocument();
    expect(screen.getByText('ongrid-k8s-control-plane')).toBeInTheDocument();
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

    render(
      <MemoryRouter>
        <KubernetesPage />
      </MemoryRouter>,
    );

    expect(await screen.findByText('kind-local')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '删除集群 kind-local' }));
    expect(await screen.findByText('删除 Kubernetes 集群 kind-local')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '我已卸载，删除记录' }));

    await waitFor(() => {
      expect(deletedID).toBe('1');
      expect(deleteForce).toBe('true');
    });
    expect(screen.queryByText('kind-local')).not.toBeInTheDocument();
  });

  it('接入命令在 localhost 页面不自动替换远端 manager 占位符', async () => {
    let payload: { name?: string; uid?: string; mode?: string } | null = null;
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
          install_command:
            "helm upgrade --install ongrid-edge 'https://<manager>/edge/k8s/ongrid-edge.tgz' --insecure-skip-tls-verify --namespace ongrid-system --create-namespace --set namespace.create=false --set-string manager.publicURL='https://<manager>' --set-string manager.tunnelAddr='<manager>:40012' --set-string manager.tlsInsecure=true --set-string enrollment.clusterID=4 --set-string enrollment.bootstrapToken='g-token' --set-string mode='full-node'",
        });
      }),
    );

    render(
      <MemoryRouter>
        <KubernetesPage />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole('button', { name: '接入集群' }));
    fireEvent.change(screen.getByLabelText('集群名称'), { target: { value: 'kind-created' } });
    fireEvent.click(screen.getByRole('button', { name: '创建' }));

    expect(await screen.findByText('Helm 安装命令')).toBeInTheDocument();
    const command = screen.getByText(/helm upgrade --install ongrid-edge/);
    expect(payload).toEqual({ name: 'kind-created', mode: 'full-node' });
    expect(command).toHaveTextContent("manager.publicURL='https://<manager>'");
    expect(command).toHaveTextContent("manager.tunnelAddr='<manager>:40012'");
  });

  it('渲染集群详情里的 Pod 快照', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    expect(screen.getAllByText('ongrid-system').length).toBeGreaterThan(0);
    expect(screen.getByText('Running')).toBeInTheDocument();
    expect(screen.getAllByText('1n / 1w / 1p / 1e').length).toBeGreaterThan(0);
    expect(screen.getByText('集群健康结论')).toBeInTheDocument();
    expect(screen.getByText('Controller')).toBeInTheDocument();
    expect(screen.getByText('Node agent 覆盖')).toBeInTheDocument();
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

  it('已恢复的 Warning Event 不进入健康结论和异常线索', async () => {
    server.use(
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
        if (url.searchParams.get('type') === 'Warning') {
          return HttpResponse.json({
            items: [{
              id: 43,
              cluster_id: 1,
              namespace: 'ongrid-system',
              name: 'recovered-readiness',
              type: 'Warning',
              reason: 'Unhealthy',
              message: 'Readiness probe failed: HTTP probe failed with statuscode: 500',
              involved_kind: 'Pod',
              involved_namespace: 'ongrid-system',
              involved_name: 'ongrid-edge-controller-abc',
              involved_uid: 'pod-healthy',
              count: 1,
              last_timestamp: '2026-06-29T10:01:00Z',
              last_seen_at: '2026-06-29T10:01:00Z',
            }],
            total: 1,
            limit: 100,
            offset: 0,
          });
        }
        return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
      }),
    );

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('当前快照未发现需要处置的异常')).toBeInTheDocument();
    expect(screen.queryByText('Warning Event 1')).not.toBeInTheDocument();
    expect(screen.queryByText('1 个待确认问题')).not.toBeInTheDocument();
    expect(screen.queryByText('Unhealthy')).not.toBeInTheDocument();
    expect(screen.queryByText('Readiness probe failed: HTTP probe failed with statuscode: 500')).not.toBeInTheDocument();
  });

  it('Pod 资源表支持加载更多快照结果', async () => {
    const podLimits: number[] = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        const limit = Number(url.searchParams.get('limit') || 100);
        podLimits.push(limit);
        const count = Math.min(limit, 150);
        return HttpResponse.json({
          items: Array.from({ length: count }, (_, index) => {
            const n = index + 1;
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
          total: 150,
          limit,
          offset: 0,
        });
      }),
    );

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('pod-001')).toBeInTheDocument();
    expect(screen.getByText('显示前 100 条，共 150 条')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '加载更多' }));

    await waitFor(() => {
      expect(podLimits).toContain(200);
    });
    expect(await screen.findByText('pod-150')).toBeInTheDocument();
  });

  it('Pod 资源表筛选分页明确展示匹配结果', async () => {
    const podLimits: number[] = [];
    server.use(
      http.get('/api/v1/k8s/clusters/:id/pods', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('reason') === 'CrashLoopBackOff') {
          return HttpResponse.json({ items: [], total: 0, limit: 20, offset: 0 });
        }
        const query = url.searchParams.get('q') || '';
        const limit = Number(url.searchParams.get('limit') || 100);
        if (query === 'api') {
          podLimits.push(limit);
          const count = Math.min(limit, 150);
          return HttpResponse.json({
            items: Array.from({ length: count }, (_, index) => {
              const n = index + 1;
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'api' } });

    expect(await screen.findByText('api-pod-001')).toBeInTheDocument();
    expect(screen.getByText('显示前 100 条匹配，共 150 条匹配')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '加载更多匹配' }));

    await waitFor(() => {
      expect(podLimits).toContain(200);
    });
    expect(await screen.findByText('api-pod-150')).toBeInTheDocument();
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/chat/:sessionId" element={<ChatStateProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    const podCell = await screen.findByText('ongrid-edge-controller-abc');
    const row = podCell.closest('tr');
    expect(row).not.toBeNull();
    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: '排障' }));

    expect(within(row as HTMLElement).getByRole('button', { name: '日志' })).toBeInTheDocument();
    expect(within(row as HTMLElement).getByRole('button', { name: 'describe' })).toBeInTheDocument();
    expect(within(row as HTMLElement).getByRole('button', { name: '链路' })).toBeInTheDocument();

    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'AI 分析' }));

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
        if (url.searchParams.get('type') === 'Warning') {
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=events']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/chat/:sessionId" element={<ChatStateProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    const warningObject = await screen.findByText('Pod/api-crash-abc');
    const warningRow = warningObject.closest('tr');
    expect(warningRow).not.toBeNull();
    fireEvent.click(within(warningRow as HTMLElement).getByRole('button', { name: '排障' }));

    expect(within(warningRow as HTMLElement).getByRole('button', { name: '日志' })).toBeInTheDocument();
    expect(within(warningRow as HTMLElement).getByRole('button', { name: 'describe' })).toBeInTheDocument();
    expect(within(warningRow as HTMLElement).getByRole('button', { name: '链路' })).toBeInTheDocument();

    const normalObject = await screen.findByText('Pod/ongrid-edge-controller-abc');
    const normalRow = normalObject.closest('tr');
    expect(normalRow).not.toBeNull();
    expect(within(normalRow as HTMLElement).queryByRole('button', { name: '排障' })).not.toBeInTheDocument();

    fireEvent.click(within(warningRow as HTMLElement).getByRole('button', { name: 'AI 分析' }));

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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=nodes']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=nodes']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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
      http.get('/api/v1/aiops/mutating-proposals', () => HttpResponse.json({ items: [], total: 0 })),
    );

    render(
      <MemoryRouter initialEntries={['/kubernetes/99']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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
    render(
      <MemoryRouter initialEntries={['/kubernetes/1']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    await screen.findByText('集群健康结论');
    const podTabs = screen.getAllByRole('button', { name: /Pods/ });
    fireEvent.click(podTabs[0]);

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
  });

  it('在 Nodes 视图把 Node Edge 作为设备入口展示', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=nodes']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect((await screen.findAllByText('ongrid-k8s-control-plane')).length).toBeGreaterThan(0);
    expect(screen.getByText('Node Edge #5')).toBeInTheDocument();
    expect(screen.getByText('接入实例')).toBeInTheDocument();
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=nodes']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/chat/:sessionId" element={<ChatStateProbe />} />
        </Routes>
      </MemoryRouter>,
    );

    const nodeCells = await screen.findAllByText('ongrid-k8s-control-plane');
    const row = nodeCells.map((cell) => cell.closest('tr')).find(Boolean);
    expect(row).toBeTruthy();
    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: '排障' }));

    expect(within(row as HTMLElement).getByRole('button', { name: '日志' })).toBeInTheDocument();
    expect(within(row as HTMLElement).getByRole('button', { name: 'describe' })).toBeInTheDocument();
    expect(within(row as HTMLElement).queryByRole('button', { name: '链路' })).not.toBeInTheDocument();

    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'AI 分析' }));

    await waitFor(() => {
      expect(sessionPayload).toEqual({ title: 'analyze ongrid-k8s-control-plane', agent_id: 'default' });
    });
    expect(await screen.findByTestId('initial-prompt')).toHaveTextContent('请分析 Kubernetes 资源状态');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('Node/ongrid-k8s-control-plane');
    expect(screen.getByTestId('initial-prompt')).toHaveTextContent('必须先 dry-run 并走审批');
  });

  it('Namespace 行可以直接跳转到对应命名空间的资源视图', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=namespaces']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    const namespaceCell = (await screen.findAllByText('ongrid-system'))
      .find((item) => item.closest('td'));
    const row = namespaceCell?.closest('tr');
    expect(row).not.toBeNull();

    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'Pods' }));

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
    expect(screen.getByText('Pod 资源视图')).toBeInTheDocument();
    expect(screen.getByRole('combobox', { name: '命名空间过滤' })).toHaveValue('ongrid-system');
    expect(await screen.findByText('ongrid-edge-controller-abc')).toBeInTheDocument();
  });

  it('渲染 CrashLoopBackOff 诊断区和关联 Warning Event', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('api-crash-abc')).toBeInTheDocument();
    expect(screen.getAllByText('CrashLoopBackOff').length).toBeGreaterThan(0);
    expect(screen.getByText('7 次重启')).toBeInTheDocument();
    expect(screen.getByText(/Back-off restarting failed container api/)).toBeInTheDocument();
    expect(screen.getAllByText('查看日志').length).toBeGreaterThan(0);
    expect(screen.getAllByText('describe').length).toBeGreaterThan(0);
    expect(screen.getAllByText('关联链路').length).toBeGreaterThan(0);
    expect(screen.getAllByRole('button', { name: 'AI 分析' }).length).toBeGreaterThan(0);
    expect(screen.queryByText('default · Pod/api-crash-abc')).not.toBeInTheDocument();
    expect(screen.getAllByText('更多').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole('button', { name: '查看 Pod' }));

    await waitFor(() => {
      expect(Element.prototype.scrollIntoView).toHaveBeenCalledWith({ block: 'start', behavior: 'smooth' });
    });
    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('api-crash-abc');
    expect(screen.getByRole('combobox', { name: '命名空间过滤' })).toHaveValue('default');
    expect(screen.getByRole('button', { name: '只看异常' })).toHaveClass('border-amber-500/50');

    fireEvent.click(screen.getAllByRole('button', { name: /Nodes/ })[0]);

    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('');
    expect(screen.getByRole('button', { name: '只看异常' })).not.toHaveClass('border-amber-500/50');
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=pods']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
          <Route path="/chat/:sessionId" element={<ChatStateProbe />} />
        </Routes>
      </MemoryRouter>,
    );

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
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=actions']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

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
    expect(screen.getAllByText('apply patch').length).toBeGreaterThan(0);
    expect(await screen.findByText('K8s 写动作审计')).toBeInTheDocument();
    expect(screen.getByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getAllByText('已执行').length).toBeGreaterThan(0);
    expect(screen.getByText('请求已记录')).toBeInTheDocument();
    expect(screen.getByText('Dry run 要求')).toBeInTheDocument();
    expect(screen.getByText('审批通过')).toBeInTheDocument();
    expect(screen.getByText('执行完成')).toBeInTheDocument();
    expect(screen.getByText(/回滚到上一 revision/)).toBeInTheDocument();
    expect(screen.getAllByText(/请求/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/审批/).length).toBeGreaterThan(0);
    expect(screen.getByText(/工具已返回/)).toBeInTheDocument();
    expect(screen.getByText('会话 session-k8s-1')).toBeInTheDocument();
    expect(screen.getByText('消息 message-k8')).toBeInTheDocument();
    expect(screen.getByText('调用 call-k8s-1')).toBeInTheDocument();
    expect(screen.getByText('agent-1')).toBeInTheDocument();
    expect(screen.getByText('目标资源清晰，风险可控')).toBeInTheDocument();
    expect(screen.queryByText('scale · default · Deployment/other → 2')).not.toBeInTheDocument();
  });

  it('Actions 资源支持按审批状态和动作类型筛选', async () => {
    server.use(
      http.get('/api/v1/aiops/mutating-proposals', ({ request }) => {
        const url = new URL(request.url);
        if (url.searchParams.get('tool_name') !== 'execute_k8s_action') {
          return HttpResponse.json({ items: [], total: 0, limit: 100, offset: 0 });
        }
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
              reviewer_agent: 'reviewer',
              decision: 'approve',
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
              reviewer_agent: 'reviewer',
              decision: 'pending',
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
              reviewer_agent: 'reviewer',
              decision: 'reject',
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

    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=actions']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('combobox', { name: '审批状态过滤' }), { target: { value: 'pending' } });

    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.queryByText('rollout_restart · default · Deployment/api')).not.toBeInTheDocument();
    expect(screen.queryByText('scale · default · Deployment/api → 2')).not.toBeInTheDocument();
    expect(screen.getByText('1 条匹配')).toBeInTheDocument();

    fireEvent.change(screen.getByRole('combobox', { name: '审批状态过滤' }), { target: { value: 'all' } });
    fireEvent.change(screen.getByRole('combobox', { name: '动作类型过滤' }), { target: { value: 'scale' } });

    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();
    expect(screen.queryByText('delete_pod · default · Pod/api-crash-abc')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '清除' }));

    expect(screen.getByRole('combobox', { name: '审批状态过滤' })).toHaveValue('all');
    expect(screen.getByRole('combobox', { name: '动作类型过滤' })).toHaveValue('all');
    expect(screen.getByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    expect(screen.getByText('delete_pod · default · Pod/api-crash-abc')).toBeInTheDocument();
    expect(screen.getByText('scale · default · Deployment/api → 2')).toBeInTheDocument();
  });

  it('Actions 资源筛选无结果时展示当前筛选条件', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=actions']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
    fireEvent.change(screen.getByRole('textbox', { name: '搜索资源' }), { target: { value: 'missing-action' } });

    expect(screen.getByText('暂无匹配写动作审计记录')).toBeInTheDocument();
    expect(screen.getByText('当前条件：搜索 "missing-action"')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '清除筛选' }));

    expect(screen.getByRole('textbox', { name: '搜索资源' })).toHaveValue('');
    expect(await screen.findByText('rollout_restart · default · Deployment/api')).toBeInTheDocument();
  });

  it('渲染 Namespaces 资源页签', async () => {
    render(
      <MemoryRouter initialEntries={['/kubernetes/1?tab=namespaces']}>
        <Routes>
          <Route path="/kubernetes/:clusterId" element={<KubernetesClusterDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect((await screen.findAllByText('Namespaces')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('ongrid-system').length).toBeGreaterThan(0);
    expect(screen.getByText('Warnings')).toBeInTheDocument();
  });

});
