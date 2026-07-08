import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import EdgesPage from './Edges';
import { server } from '@/test/msw-server';

vi.mock('@/store/me', () => ({
  usePermissions: () => ({ isAdmin: true, canMutate: true, role: 'admin' }),
}));

describe('EdgesPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/version', () => HttpResponse.json({ manager_version: 'dev' })),
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 3,
              name: 'kind-controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: { hostname: 'controller-pod', ip_address: '10.0.0.3' },
              device_id: null,
              agent_version: 'dev',
            },
            {
              id: 5,
              name: 'k8s:kind-local:ongrid-k8s-control-plane',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: { hostname: 'ongrid-k8s-control-plane', ip_address: '10.0.0.5' },
              device_id: 17,
              agent_version: 'dev',
            },
            {
              id: 9,
              name: 'bare-metal-1',
              status: 'online',
              roles: ['server'],
              access_key_id: 'ak-host',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: { hostname: 'bm-1', ip_address: '10.0.0.9' },
              device_id: 19,
              agent_version: 'dev',
            },
          ],
          total: 3,
        }),
      ),
      http.get('/api/v1/k8s/clusters', () =>
        HttpResponse.json({
          items: [{
            id: 1,
            name: 'kind-local',
            uid: 'kind-uid',
            mode: 'full-node',
            status: 'online',
            controller_edge_id: 3,
            controller_node_name: 'ongrid-k8s-control-plane',
            controller_namespace: 'ongrid-system',
            controller_pod_name: 'ongrid-edge-controller-abc',
            bootstrap_token_expires_at: '2026-06-30T10:00:00Z',
            created_at: '2026-06-29T09:00:00Z',
            updated_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
          limit: 100,
          offset: 0,
        }),
      ),
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
    );
  });

  it('隐藏 Controller Edge，并把 controller runtime 标到所在 Node Edge', async () => {
    render(
      <MemoryRouter>
        <EdgesPage />
      </MemoryRouter>,
    );

    expect(await screen.findAllByText('ongrid-k8s-control-plane')).toHaveLength(2);
    expect(screen.queryByText('k8s:kind-local:ongrid-k8s-control-plane')).not.toBeInTheDocument();
    expect(screen.queryByText('kind-controller')).not.toBeInTheDocument();
    expect(screen.getByText('K8s Node')).toBeInTheDocument();
    expect(screen.getByText('K8s Controller')).toBeInTheDocument();
    expect(screen.queryByText('Controller Runtime')).not.toBeInTheDocument();
    expect(screen.getByText('kind-local')).toBeInTheDocument();
    expect(screen.getByText('bare-metal-1')).toBeInTheDocument();
    expect(screen.getByText('bm-1')).toBeInTheDocument();
    expect(screen.getByText('Host Edge')).toBeInTheDocument();
  });
});
