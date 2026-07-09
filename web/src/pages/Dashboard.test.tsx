import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it } from 'vitest';

import DashboardPage from './Dashboard';
import { server } from '@/test/msw-server';

describe('DashboardPage', () => {
  beforeEach(() => {
    localStorage.setItem('ongrid-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/edges', () =>
        HttpResponse.json({
          items: [
            {
              id: 23,
              name: 'k8s:kind-local:controller',
              status: 'online',
              roles: [],
              access_key_id: 'ak-controller',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: null,
              device_id: null,
              agent_version: null,
            },
            {
              id: 24,
              name: 'k8s:kind-local:ongrid-k8s-control-plane',
              status: 'online',
              roles: [],
              access_key_id: 'ak-node',
              last_seen_at: '2026-06-29T10:00:00Z',
              host_info: { hostname: 'ongrid-k8s-control-plane', ip_address: '10.0.0.5' },
              device_id: 31,
              agent_version: 'v0.9.0',
            },
          ],
          total: 2,
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
            controller_edge_id: 23,
            controller_node_name: 'ongrid-k8s-control-plane',
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
            edge_id: 24,
            device_id: 31,
            capacity: { cpu: '8', memory: '16Gi' },
            kubelet_version: 'v1.30.0',
            last_seen_at: '2026-06-29T10:00:00Z',
          }],
          total: 1,
        }),
      ),
      http.get('/api/v1/metrics/query_range', () =>
        HttpResponse.json({
          resolution: '1h',
          from: '2026-06-28T10:00:00Z',
          to: '2026-06-29T10:00:00Z',
          matrix: [],
        }),
      ),
      http.get('/api/v1/chat/sessions', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/alerts/incidents', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/usage/today', () => HttpResponse.json({ total_tokens: 0 })),
    );
  });

  it('统计在线设备时不把 K8s Controller Edge 当作设备', async () => {
    render(
      <MemoryRouter>
        <DashboardPage />
      </MemoryRouter>,
    );

    const card = (await screen.findByText('在线设备')).closest('.rounded-xl') as HTMLElement;
    expect(card).not.toBeNull();

    await waitFor(() => {
      expect(within(card).getByText('1')).toBeInTheDocument();
      expect(within(card).getByText('/ 1')).toBeInTheDocument();
    });
    expect(screen.getByText('1 台 →')).toBeInTheDocument();
  });
});
